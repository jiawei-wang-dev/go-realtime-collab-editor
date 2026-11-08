package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/authentication"
	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/collab"
	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/permission"
	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/protocol"
)

func TestBrowserRESTWebSocketEditAndResume(t *testing.T) {
	fixture := newAuthFixture(t)
	pair := fixture.registerLogin(t, "browser-editor")
	response := authRequest(t, http.MethodPost, fixture.server.URL+"/api/documents", pair.AccessToken, []byte(`{"id":"browser.go","content":"abc"}`))
	response.Body.Close()
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("create status = %d", response.StatusCode)
	}

	connection := dialBrowserTestConnection(t, fixture.server.URL, pair.AccessToken)
	writeBrowserMessage(t, connection, protocol.Message{Type: protocol.TypeJoin, DocID: "browser.go"})
	if joined := readBrowserMessage(t, connection, protocol.TypeJoined); joined.Content != "abc" || joined.Role != "owner" {
		t.Fatalf("joined = %#v", joined)
	}
	operation := collab.Operation{Kind: collab.OperationInsert, Position: 1, Text: "X"}
	writeBrowserMessage(t, connection, protocol.Message{Type: protocol.TypeEdit, DocID: "browser.go", RequestID: "browser-request", BaseVersion: 0, Operation: &operation})
	if applied := readBrowserMessage(t, connection, protocol.TypeEditApplied); applied.Version != 1 {
		t.Fatalf("applied = %#v", applied)
	}
	connection.CloseNow()

	resumed := dialBrowserTestConnection(t, fixture.server.URL, pair.AccessToken)
	defer resumed.CloseNow()
	writeBrowserMessage(t, resumed, protocol.Message{Type: protocol.TypeResume, DocID: "browser.go", LastVersion: 0})
	sync := readBrowserMessage(t, resumed, protocol.TypeSync)
	if sync.ToVersion != 1 || len(sync.Operations) != 1 || sync.Operations[0].RequestID != "browser-request" {
		t.Fatalf("sync = %#v", sync)
	}
}

func TestTwoBrowserConnectionsConcurrentEditPermissionAndLogout(t *testing.T) {
	fixture := newAuthFixture(t)
	owner := fixture.registerLogin(t, "browser-owner")
	writer := fixture.registerLogin(t, "browser-writer")
	response := authRequest(t, http.MethodPost, fixture.server.URL+"/api/documents", owner.AccessToken, []byte(`{"id":"shared.go","content":"abc"}`))
	response.Body.Close()
	permissionBody, _ := json.Marshal(map[string]permission.Role{"role": permission.RoleWrite})
	response = authRequest(t, http.MethodPut, fixture.server.URL+"/api/documents/shared.go/permissions/"+principalUserID(t, fixture.service, writer.AccessToken), owner.AccessToken, permissionBody)
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("grant status = %d", response.StatusCode)
	}

	ownerConnection := dialBrowserTestConnection(t, fixture.server.URL, owner.AccessToken)
	defer ownerConnection.CloseNow()
	writerConnection := dialBrowserTestConnection(t, fixture.server.URL, writer.AccessToken)
	defer writerConnection.CloseNow()
	writeBrowserMessage(t, ownerConnection, protocol.Message{Type: protocol.TypeJoin, DocID: "shared.go"})
	readBrowserMessage(t, ownerConnection, protocol.TypeJoined)
	writeBrowserMessage(t, writerConnection, protocol.Message{Type: protocol.TypeJoin, DocID: "shared.go"})
	if joined := readBrowserMessage(t, writerConnection, protocol.TypeJoined); joined.Role != permission.RoleWrite {
		t.Fatalf("writer join = %#v", joined)
	}

	ownerOperation := collab.Operation{Kind: collab.OperationInsert, Position: 0, Text: "A"}
	writerOperation := collab.Operation{Kind: collab.OperationInsert, Position: 0, Text: "B"}
	writeBrowserMessage(t, ownerConnection, protocol.Message{Type: protocol.TypeEdit, DocID: "shared.go", RequestID: "owner-edit", BaseVersion: 0, Operation: &ownerOperation})
	writeBrowserMessage(t, writerConnection, protocol.Message{Type: protocol.TypeEdit, DocID: "shared.go", RequestID: "writer-edit", BaseVersion: 0, Operation: &writerOperation})
	ownerOrder := []string{readBrowserMessage(t, ownerConnection, protocol.TypeEditApplied).RequestID, readBrowserMessage(t, ownerConnection, protocol.TypeEditApplied).RequestID}
	writerOrder := []string{readBrowserMessage(t, writerConnection, protocol.TypeEditApplied).RequestID, readBrowserMessage(t, writerConnection, protocol.TypeEditApplied).RequestID}
	if ownerOrder[0] != writerOrder[0] || ownerOrder[1] != writerOrder[1] {
		t.Fatalf("clients observed different operation order: owner=%v writer=%v", ownerOrder, writerOrder)
	}

	writerID := principalUserID(t, fixture.service, writer.AccessToken)
	permissionBody, _ = json.Marshal(map[string]permission.Role{"role": permission.RoleRead})
	response = authRequest(t, http.MethodPut, fixture.server.URL+"/api/documents/shared.go/permissions/"+writerID, owner.AccessToken, permissionBody)
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("downgrade status = %d", response.StatusCode)
	}
	if changed := readBrowserMessage(t, writerConnection, protocol.TypePermissionChanged); changed.Role != permission.RoleRead {
		t.Fatalf("permission change = %#v", changed)
	}
	blocked := collab.Operation{Kind: collab.OperationInsert, Position: 0, Text: "blocked"}
	writeBrowserMessage(t, writerConnection, protocol.Message{Type: protocol.TypeEdit, DocID: "shared.go", RequestID: "blocked", BaseVersion: 2, Operation: &blocked})
	if rejected := readBrowserMessage(t, writerConnection, protocol.TypeError); rejected.Code != protocol.CodePermissionDenied {
		t.Fatalf("rejection = %#v", rejected)
	}

	response = authRequest(t, http.MethodPost, fixture.server.URL+"/api/auth/logout", owner.AccessToken, nil)
	response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("logout status = %d", response.StatusCode)
	}
	readCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	var message protocol.Message
	if err := wsjson.Read(readCtx, ownerConnection, &message); err == nil {
		t.Fatal("logout did not close the browser connection")
	}
}

func principalUserID(t *testing.T, service interface {
	AuthenticateToken(context.Context, string) (authentication.Principal, error)
}, token string) string {
	t.Helper()
	principal, err := service.AuthenticateToken(context.Background(), token)
	if err != nil {
		t.Fatal(err)
	}
	return principal.UserID
}

func dialBrowserTestConnection(t *testing.T, serverURL, token string) *websocket.Conn {
	t.Helper()
	connection, _, err := websocket.Dial(context.Background(), "ws"+strings.TrimPrefix(serverURL, "http")+"/api/realtime", nil)
	if err != nil {
		t.Fatal(err)
	}
	writeBrowserMessage(t, connection, protocol.Message{Type: protocol.TypeAuth, Token: token})
	if response := readBrowserMessage(t, connection, protocol.TypeAuthOK); response.Type != protocol.TypeAuthOK {
		t.Fatalf("auth = %#v", response)
	}
	return connection
}

func writeBrowserMessage(t *testing.T, connection *websocket.Conn, message protocol.Message) {
	t.Helper()
	if err := wsjson.Write(context.Background(), connection, message); err != nil {
		t.Fatal(err)
	}
}

func readBrowserMessage(t *testing.T, connection *websocket.Conn, messageType string) protocol.Message {
	t.Helper()
	for {
		var message protocol.Message
		if err := wsjson.Read(context.Background(), connection, &message); err != nil {
			t.Fatal(err)
		}
		if message.Type == messageType {
			return message
		}
	}
}
