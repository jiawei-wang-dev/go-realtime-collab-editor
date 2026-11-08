package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/cluster"
	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/collab"
	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/permission"
	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/protocol"
	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/room"
)

type fixedRateLimiter struct {
	allowed bool
	err     error
}

func (limiter fixedRateLimiter) Allow(context.Context, string, int, time.Duration) (bool, error) {
	return limiter.allowed, limiter.err
}

func TestRateLimitResponses(t *testing.T) {
	tests := []struct {
		name       string
		limiter    fixedRateLimiter
		wantStatus int
		wantCode   string
	}{
		{name: "limit exceeded", limiter: fixedRateLimiter{}, wantStatus: http.StatusTooManyRequests, wantCode: codeRateLimited},
		{name: "backend unavailable", limiter: fixedRateLimiter{err: errors.New("redis unavailable")}, wantStatus: http.StatusServiceUnavailable, wantCode: protocol.CodeServiceUnavailable},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := NewServerWithConfig(Config{RateLimiter: test.limiter})
			defer server.Shutdown(context.Background())
			request := httptest.NewRequest(http.MethodPost, "/api/auth/login", nil)
			request.RemoteAddr = "192.0.2.10:1234"
			response := httptest.NewRecorder()
			if server.allowRequest(response, request, "login", 1) {
				t.Fatal("request was allowed")
			}
			if response.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d", response.Code, test.wantStatus)
			}
			var message protocol.Message
			if err := json.Unmarshal(response.Body.Bytes(), &message); err != nil {
				t.Fatal(err)
			}
			if message.Code != test.wantCode {
				t.Fatalf("code = %q, want %q", message.Code, test.wantCode)
			}
		})
	}
}

func TestCORSUsesExactAllowlist(t *testing.T) {
	server := NewServerWithConfig(Config{AllowedOrigins: []string{"https://editor.example.com"}})
	defer server.Shutdown(context.Background())

	disallowed := httptest.NewRequest(http.MethodGet, "/api/documents/doc", nil)
	disallowed.Header.Set("Origin", "https://evil.example.com")
	disallowedResponse := httptest.NewRecorder()
	server.ServeHTTP(disallowedResponse, disallowed)
	if disallowedResponse.Code != http.StatusForbidden {
		t.Fatalf("disallowed status = %d", disallowedResponse.Code)
	}

	preflight := httptest.NewRequest(http.MethodOptions, "/api/documents/doc", nil)
	preflight.Header.Set("Origin", "https://editor.example.com")
	preflightResponse := httptest.NewRecorder()
	server.ServeHTTP(preflightResponse, preflight)
	if preflightResponse.Code != http.StatusNoContent || preflightResponse.Header().Get("Access-Control-Allow-Origin") != "https://editor.example.com" {
		t.Fatalf("preflight status=%d headers=%v", preflightResponse.Code, preflightResponse.Header())
	}
}

func TestServiceUnavailableHTTPMapping(t *testing.T) {
	tests := []struct {
		name  string
		write func(http.ResponseWriter)
	}{
		{
			name: "local room error",
			write: func(writer http.ResponseWriter) {
				writeRoomError(writer, &room.CodedError{Code: protocol.CodeServiceUnavailable, Message: "room ownership is no longer valid"})
			},
		},
		{
			name: "forwarded owner error",
			write: func(writer http.ResponseWriter) {
				server := &Server{}
				server.writeForwardError(writer, &cluster.RemoteError{Status: http.StatusServiceUnavailable, Code: protocol.CodeServiceUnavailable, Message: "room ownership is no longer valid"})
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			test.write(response)
			if response.Code != http.StatusServiceUnavailable {
				t.Fatalf("status = %d, want %d", response.Code, http.StatusServiceUnavailable)
			}
			var message protocol.Message
			if err := json.Unmarshal(response.Body.Bytes(), &message); err != nil {
				t.Fatal(err)
			}
			if message.Type != protocol.TypeError || message.Code != protocol.CodeServiceUnavailable || message.Message != "room ownership is no longer valid" {
				t.Fatalf("error response = %#v", message)
			}
		})
	}
}

func TestProtectedDocumentAndPermissionREST(t *testing.T) {
	application := NewServer()
	server := httptest.NewServer(application)
	defer server.Close()
	defer application.Shutdown(context.Background())

	createDocument(t, server.URL, "doc", "abc", "owner")
	setPermission(t, server.URL, "doc", "owner", "writer", permission.RoleWrite, http.StatusOK)
	getDocumentStatus(t, server.URL, "doc", "", http.StatusUnauthorized)
	getDocumentStatus(t, server.URL, "doc", "unknown", http.StatusForbidden)
	document := getDocument(t, server.URL, "doc", "owner")
	if document.Content != "abc" || document.Version != 0 {
		t.Fatalf("document = %#v", document)
	}
	getPermissionStatus(t, server.URL, "doc", "owner", "writer", http.StatusOK)
	getPermissionStatus(t, server.URL, "doc", "writer", "writer", http.StatusForbidden)
	setPermission(t, server.URL, "doc", "writer", "reader", permission.RoleRead, http.StatusForbidden)

	listRequest, _ := http.NewRequest(http.MethodGet, server.URL+"/api/documents", nil)
	listRequest.Header.Set("X-User-ID", "writer")
	listResponse, err := http.DefaultClient.Do(listRequest)
	if err != nil {
		t.Fatal(err)
	}
	defer listResponse.Body.Close()
	var listed struct {
		Documents []struct {
			ID   string          `json:"id"`
			Role permission.Role `json:"role"`
		} `json:"documents"`
	}
	if err := json.NewDecoder(listResponse.Body).Decode(&listed); err != nil {
		t.Fatal(err)
	}
	if listResponse.StatusCode != http.StatusOK || len(listed.Documents) != 1 || listed.Documents[0].ID != "doc" || listed.Documents[0].Role != permission.RoleWrite {
		t.Fatalf("document list = %#v status=%d", listed, listResponse.StatusCode)
	}

	permissionsRequest, _ := http.NewRequest(http.MethodGet, server.URL+"/api/documents/doc/permissions", nil)
	permissionsRequest.Header.Set("X-User-ID", "owner")
	permissionsResponse, err := http.DefaultClient.Do(permissionsRequest)
	if err != nil {
		t.Fatal(err)
	}
	permissionsResponse.Body.Close()
	if permissionsResponse.StatusCode != http.StatusOK {
		t.Fatalf("permissions status = %d", permissionsResponse.StatusCode)
	}

	deleteRequest, _ := http.NewRequest(http.MethodDelete, server.URL+"/api/documents/doc", nil)
	deleteRequest.Header.Set("X-User-ID", "owner")
	deleteResponse, err := http.DefaultClient.Do(deleteRequest)
	if err != nil {
		t.Fatal(err)
	}
	deleteResponse.Body.Close()
	if deleteResponse.StatusCode != http.StatusNoContent {
		t.Fatalf("delete status = %d", deleteResponse.StatusCode)
	}
	getDocumentStatus(t, server.URL, "doc", "owner", http.StatusNotFound)
}

func TestRESTRejectsMalformedJSONAndDuplicateDocument(t *testing.T) {
	application := NewServer()
	server := httptest.NewServer(application)
	defer server.Close()
	createDocument(t, server.URL, "doc", "", "owner")

	request, _ := http.NewRequest(http.MethodPost, server.URL+"/api/documents", bytes.NewBufferString(`{"id":`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-User-ID", "owner")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("malformed status = %d", response.StatusCode)
	}

	request, _ = http.NewRequest(http.MethodPost, server.URL+"/api/documents", bytes.NewBufferString(`{"id":"doc"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-User-ID", "owner")
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusConflict {
		t.Fatalf("duplicate status = %d", response.StatusCode)
	}
}

func TestDocumentAndPermissionReadsForwardToAuthoritativeOwner(t *testing.T) {
	remoteOwner := cluster.Owner{DocumentID: "doc", NodeID: "node-b", Incarnation: "process-b", State: cluster.OwnerActive}
	forwarder := &recordingForwarder{
		document: collab.Document{ID: "doc", Content: "remote", Version: 7},
		role:     permission.RoleWrite,
	}
	application := NewServerWithConfig(Config{
		OwnerResolver:       staticOwnerResolver{owner: remoteOwner},
		LocalNode:           cluster.Node{NodeID: "node-a", Incarnation: "process-a"},
		PermissionForwarder: forwarder,
	})
	defer application.Shutdown(context.Background())

	documentRequest := httptest.NewRequest(http.MethodGet, "/api/documents/doc", nil)
	documentRequest.Header.Set("X-User-ID", "reader")
	documentResponse := httptest.NewRecorder()
	application.ServeHTTP(documentResponse, documentRequest)
	if documentResponse.Code != http.StatusOK {
		t.Fatalf("document status = %d body=%s", documentResponse.Code, documentResponse.Body.String())
	}
	var document collab.Document
	if err := json.Unmarshal(documentResponse.Body.Bytes(), &document); err != nil {
		t.Fatal(err)
	}
	if document != forwarder.document || forwarder.documentCalls != 1 {
		t.Fatalf("document = %#v, calls = %d", document, forwarder.documentCalls)
	}

	permissionRequest := httptest.NewRequest(http.MethodGet, "/api/documents/doc/permissions/writer", nil)
	permissionRequest.Header.Set("X-User-ID", "owner")
	permissionResponse := httptest.NewRecorder()
	application.ServeHTTP(permissionResponse, permissionRequest)
	if permissionResponse.Code != http.StatusOK || forwarder.permissionReadCalls != 1 {
		t.Fatalf("permission status = %d calls = %d body=%s", permissionResponse.Code, forwarder.permissionReadCalls, permissionResponse.Body.String())
	}
}

func TestDocumentReadFailsClosedWhenOwnerCannotBeResolved(t *testing.T) {
	forwarder := &recordingForwarder{}
	application := NewServerWithConfig(Config{
		OwnerResolver:       staticOwnerResolver{err: cluster.ErrOwnerNotFound},
		LocalNode:           cluster.Node{NodeID: "node-a", Incarnation: "process-a"},
		PermissionForwarder: forwarder,
	})
	defer application.Shutdown(context.Background())
	request := httptest.NewRequest(http.MethodGet, "/api/documents/doc", nil)
	request.Header.Set("X-User-ID", "reader")
	response := httptest.NewRecorder()
	application.ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d body=%s", response.Code, response.Body.String())
	}
	if forwarder.documentCalls != 0 {
		t.Fatal("read was forwarded without an authoritative owner")
	}
}

type staticOwnerResolver struct {
	owner cluster.Owner
	err   error
}

func (resolver staticOwnerResolver) Resolve(context.Context, string) (cluster.Owner, error) {
	return resolver.owner, resolver.err
}

func (resolver staticOwnerResolver) EnsureOwner(context.Context, string) (cluster.Owner, error) {
	return resolver.owner, resolver.err
}

type recordingForwarder struct {
	document            collab.Document
	role                permission.Role
	documentCalls       int
	permissionReadCalls int
	err                 error
}

func (forwarder *recordingForwarder) ForwardPermission(context.Context, cluster.Owner, string, string, string, permission.Role) error {
	return forwarder.err
}

func (forwarder *recordingForwarder) ForwardDocument(context.Context, cluster.Owner, string, string) (collab.Document, error) {
	forwarder.documentCalls++
	return forwarder.document, forwarder.err
}

func (forwarder *recordingForwarder) ForwardGetPermission(context.Context, cluster.Owner, string, string, string) (permission.Role, error) {
	forwarder.permissionReadCalls++
	return forwarder.role, forwarder.err
}

func (forwarder *recordingForwarder) ForwardDeleteDocument(context.Context, cluster.Owner, string, string) error {
	return forwarder.err
}

var _ cluster.PermissionForwarder = (*recordingForwarder)(nil)

func createDocument(t *testing.T, baseURL, docID, content, ownerID string) {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"id": docID, "content": content})
	request, _ := http.NewRequest(http.MethodPost, baseURL+"/api/documents", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-User-ID", ownerID)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("create status = %d body=%s", response.StatusCode, readBody(response.Body))
	}
}

func setPermission(t *testing.T, baseURL, docID, actorID, userID string, role permission.Role, want int) {
	t.Helper()
	body, _ := json.Marshal(map[string]permission.Role{"role": role})
	request, _ := http.NewRequest(http.MethodPut, baseURL+"/api/documents/"+docID+"/permissions/"+userID, bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-User-ID", actorID)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != want {
		t.Fatalf("permission status = %d want=%d body=%s", response.StatusCode, want, readBody(response.Body))
	}
}

func getDocument(t *testing.T, baseURL, docID, userID string) collab.Document {
	t.Helper()
	request, _ := http.NewRequest(http.MethodGet, baseURL+"/api/documents/"+docID, nil)
	request.Header.Set("X-User-ID", userID)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var document collab.Document
	if err := json.NewDecoder(response.Body).Decode(&document); err != nil {
		t.Fatal(err)
	}
	return document
}

func getDocumentStatus(t *testing.T, baseURL, docID, userID string, want int) {
	t.Helper()
	request, _ := http.NewRequest(http.MethodGet, baseURL+"/api/documents/"+docID, nil)
	if userID != "" {
		request.Header.Set("X-User-ID", userID)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != want {
		t.Fatalf("document status = %d want=%d", response.StatusCode, want)
	}
}

func getPermissionStatus(t *testing.T, baseURL, docID, actorID, userID string, want int) {
	t.Helper()
	request, _ := http.NewRequest(http.MethodGet, baseURL+"/api/documents/"+docID+"/permissions/"+userID, nil)
	request.Header.Set("X-User-ID", actorID)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != want {
		t.Fatalf("permission query status = %d want=%d", response.StatusCode, want)
	}
}

func readBody(reader io.Reader) string {
	payload, _ := io.ReadAll(reader)
	return string(payload)
}
