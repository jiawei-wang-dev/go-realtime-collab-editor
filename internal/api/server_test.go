package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/collab"
	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/permission"
	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/protocol"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestDemoAcceptance(t *testing.T) {
	testServer := httptest.NewServer(NewServer())
	defer testServer.Close()

	createDocument(t, testServer.URL, "demo", "abcdef", "user-a")
	setPermission(t, testServer.URL, "demo", "user-a", "user-b", permission.RoleWrite, http.StatusOK)
	setPermission(t, testServer.URL, "demo", "user-a", "user-c", permission.RoleRead, http.StatusOK)

	userA := connectAndJoin(t, testServer.URL, "demo", "user-a")
	defer userA.Close()
	userB := connectAndJoin(t, testServer.URL, "demo", "user-b")
	defer userB.Close()

	// Demo 1: an edit from A is broadcast to B.
	sendEdit(t, userA, "demo-1", 0, collab.Operation{
		Kind:     collab.OperationInsert,
		Position: 2,
		Text:     "X",
	})
	assertApplied(t, readMessage(t, userA), "demo-1", 1, 2)
	assertApplied(t, readMessage(t, userB), "demo-1", 1, 2)

	// Demo 2: B's stale edit is transformed over A's edit at the same position.
	sendEdit(t, userA, "demo-2-a", 1, collab.Operation{
		Kind:     collab.OperationInsert,
		Position: 0,
		Text:     "A",
	})
	assertApplied(t, readMessage(t, userA), "demo-2-a", 2, 0)
	assertApplied(t, readMessage(t, userB), "demo-2-a", 2, 0)

	sendEdit(t, userB, "demo-2-b", 1, collab.Operation{
		Kind:     collab.OperationInsert,
		Position: 0,
		Text:     "B",
	})
	assertApplied(t, readMessage(t, userA), "demo-2-b", 3, 1)
	assertApplied(t, readMessage(t, userB), "demo-2-b", 3, 1)

	// Demo 3: a read-only user is rejected by the server.
	userC := connectAndJoin(t, testServer.URL, "demo", "user-c")
	defer userC.Close()
	sendEdit(t, userC, "demo-3", 3, collab.Operation{
		Kind:     collab.OperationInsert,
		Position: 0,
		Text:     "blocked",
	})
	assertError(t, readMessage(t, userC), protocol.CodePermissionDenied)

	// Demo 4: a connected writer receives the downgrade and its next edit fails.
	setPermission(t, testServer.URL, "demo", "user-a", "user-b", permission.RoleRead, http.StatusOK)
	changed := readMessage(t, userB)
	if changed.Type != protocol.TypePermissionChanged || changed.Role != permission.RoleRead {
		t.Fatalf("permission_changed = %#v", changed)
	}
	sendEdit(t, userB, "demo-4", 3, collab.Operation{
		Kind:     collab.OperationInsert,
		Position: 0,
		Text:     "blocked",
	})
	assertError(t, readMessage(t, userB), protocol.CodePermissionDenied)

	// The downgraded user remains connected and receives later updates.
	sendEdit(t, userA, "demo-4-owner", 3, collab.Operation{
		Kind:     collab.OperationInsert,
		Position: 9,
		Text:     "!",
	})
	assertApplied(t, readMessage(t, userA), "demo-4-owner", 4, 9)
	assertApplied(t, readMessage(t, userB), "demo-4-owner", 4, 9)

	document := getDocument(t, testServer.URL, "demo", "user-a")
	if document.Content != "ABabXcdef!" || document.Version != 4 {
		t.Fatalf("final document = %#v", document)
	}
}

func TestUnauthorizedUserCannotReadDocument(t *testing.T) {
	testServer := httptest.NewServer(NewServer())
	defer testServer.Close()

	createDocument(t, testServer.URL, "private", "secret", "owner")
	setPermission(t, testServer.URL, "private", "owner", "reader", permission.RoleRead, http.StatusOK)

	getDocumentStatus(t, testServer.URL, "private", "", http.StatusBadRequest)
	getDocumentStatus(t, testServer.URL, "private", "unknown", http.StatusForbidden)
	getDocumentStatus(t, testServer.URL, "private", "reader", http.StatusOK)
}

func TestUnauthorizedUserCannotJoin(t *testing.T) {
	testServer := httptest.NewServer(NewServer())
	defer testServer.Close()

	createDocument(t, testServer.URL, "private", "secret", "owner")

	connection := dialWebSocket(t, testServer.URL, "private", "unknown")
	defer connection.Close()
	if err := connection.WriteJSON(protocol.Message{Type: protocol.TypeJoin, DocID: "private"}); err != nil {
		t.Fatal(err)
	}
	assertError(t, readMessage(t, connection), protocol.CodePermissionDenied)
}

func TestMalformedWebSocketMessageReturnsBadMessage(t *testing.T) {
	testServer := httptest.NewServer(NewServer())
	defer testServer.Close()

	createDocument(t, testServer.URL, "private", "secret", "owner")
	connection := dialWebSocket(t, testServer.URL, "private", "owner")
	defer connection.Close()

	if err := connection.WriteMessage(websocket.TextMessage, []byte(`{"type":`)); err != nil {
		t.Fatal(err)
	}
	assertError(t, readMessage(t, connection), protocol.CodeBadMessage)

	if err := connection.WriteJSON(protocol.Message{Type: protocol.TypeJoin, DocID: "private"}); err != nil {
		t.Fatal(err)
	}
	if joined := readMessage(t, connection); joined.Type != protocol.TypeJoined {
		t.Fatalf("joined = %#v", joined)
	}
}

func TestOnlyOwnerCanQueryAndChangePermission(t *testing.T) {
	testServer := httptest.NewServer(NewServer())
	defer testServer.Close()

	createDocument(t, testServer.URL, "doc", "", "owner")
	setPermission(t, testServer.URL, "doc", "owner", "writer", permission.RoleWrite, http.StatusOK)
	getPermissionStatus(t, testServer.URL, "doc", "owner", "writer", http.StatusOK)
	getPermissionStatus(t, testServer.URL, "doc", "writer", "writer", http.StatusForbidden)
	getPermissionStatus(t, testServer.URL, "doc", "", "writer", http.StatusBadRequest)
	setPermission(t, testServer.URL, "doc", "writer", "reader", permission.RoleRead, http.StatusForbidden)
}

func createDocument(t *testing.T, baseURL, id, content, ownerID string) {
	t.Helper()
	body := fmt.Sprintf(`{"id":%q,"content":%q,"owner_id":%q}`, id, content, ownerID)
	response, err := http.Post(baseURL+"/api/documents", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("create status = %d, body = %s", response.StatusCode, readBody(response.Body))
	}
}

func setPermission(
	t *testing.T,
	baseURL, docID, actorID, userID string,
	role permission.Role,
	wantStatus int,
) {
	t.Helper()
	body, err := json.Marshal(map[string]any{"role": role})
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequest(
		http.MethodPut,
		baseURL+"/api/documents/"+docID+"/permissions/"+userID,
		bytes.NewReader(body),
	)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-User-ID", actorID)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != wantStatus {
		t.Fatalf("permission status = %d, want %d, body = %s", response.StatusCode, wantStatus, readBody(response.Body))
	}
}

func connectAndJoin(t *testing.T, baseURL, docID, userID string) *websocket.Conn {
	t.Helper()
	connection := dialWebSocket(t, baseURL, docID, userID)
	if err := connection.WriteJSON(protocol.Message{Type: protocol.TypeJoin, DocID: docID}); err != nil {
		t.Fatal(err)
	}
	joined := readMessage(t, connection)
	if joined.Type != protocol.TypeJoined || joined.DocID != docID {
		t.Fatalf("joined = %#v", joined)
	}
	return connection
}

func dialWebSocket(t *testing.T, baseURL, docID, userID string) *websocket.Conn {
	t.Helper()
	wsURL := "ws" + strings.TrimPrefix(baseURL, "http") + "/ws?doc_id=" + docID + "&user_id=" + userID
	connection, response, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		if response != nil {
			t.Fatalf("dial: %v, status: %s", err, response.Status)
		}
		t.Fatal(err)
	}
	return connection
}

func sendEdit(t *testing.T, connection *websocket.Conn, requestID string, baseVersion uint64, operation collab.Operation) {
	t.Helper()
	if err := connection.WriteJSON(protocol.Message{
		Type:        protocol.TypeEdit,
		RequestID:   requestID,
		DocID:       "demo",
		BaseVersion: baseVersion,
		Operation:   &operation,
	}); err != nil {
		t.Fatal(err)
	}
}

func readMessage(t *testing.T, connection *websocket.Conn) protocol.Message {
	t.Helper()
	if err := connection.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	var message protocol.Message
	if err := connection.ReadJSON(&message); err != nil {
		t.Fatal(err)
	}
	return message
}

func assertApplied(t *testing.T, message protocol.Message, requestID string, version uint64, position int) {
	t.Helper()
	if message.Type != protocol.TypeEditApplied ||
		message.RequestID != requestID ||
		message.Version != version ||
		message.Operation == nil ||
		message.Operation.Position != position {
		t.Fatalf("edit_applied = %#v", message)
	}
}

func assertError(t *testing.T, message protocol.Message, code string) {
	t.Helper()
	if message.Type != protocol.TypeError || message.Code != code {
		t.Fatalf("error = %#v, want code %s", message, code)
	}
}

func getDocument(t *testing.T, baseURL, docID, actorID string) collab.Document {
	t.Helper()
	request, err := http.NewRequest(http.MethodGet, baseURL+"/api/documents/"+docID, nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("X-User-ID", actorID)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("get document status = %d, body = %s", response.StatusCode, readBody(response.Body))
	}
	var document collab.Document
	if err := json.NewDecoder(response.Body).Decode(&document); err != nil {
		t.Fatal(err)
	}
	return document
}

func getDocumentStatus(t *testing.T, baseURL, docID, actorID string, wantStatus int) {
	t.Helper()
	request, err := http.NewRequest(http.MethodGet, baseURL+"/api/documents/"+docID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if actorID != "" {
		request.Header.Set("X-User-ID", actorID)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != wantStatus {
		t.Fatalf("get document status = %d, want %d, body = %s", response.StatusCode, wantStatus, readBody(response.Body))
	}
}

func getPermissionStatus(t *testing.T, baseURL, docID, actorID, userID string, wantStatus int) {
	t.Helper()
	request, err := http.NewRequest(
		http.MethodGet,
		baseURL+"/api/documents/"+docID+"/permissions/"+userID,
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if actorID != "" {
		request.Header.Set("X-User-ID", actorID)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != wantStatus {
		t.Fatalf("get permission status = %d, want %d, body = %s", response.StatusCode, wantStatus, readBody(response.Body))
	}
}

func readBody(reader io.Reader) string {
	body, _ := io.ReadAll(reader)
	return string(body)
}
