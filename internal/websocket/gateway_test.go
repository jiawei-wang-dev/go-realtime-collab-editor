package websocket

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/authentication"
	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/collab"
	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/protocol"
	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/room"
	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/storage"
)

type testAuthenticator struct{}

func (testAuthenticator) AuthenticateToken(context.Context, string) (authentication.Principal, error) {
	return authentication.Principal{UserID: "owner", SessionID: "session", Trusted: true}, nil
}
func (testAuthenticator) AuthenticateHTTP(*http.Request) (authentication.Principal, error) {
	return authentication.Principal{}, nil
}
func (testAuthenticator) ValidatePrincipal(context.Context, authentication.Principal) error {
	return nil
}

func TestGatewayUsesRoomProtocolAndSessionRegistry(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemory()
	if _, err := store.CreateDocument(ctx, "doc", "abc", "owner"); err != nil {
		t.Fatal(err)
	}
	manager := room.NewManager(store)
	t.Cleanup(func() {
		shutdownCtx, cancel := context.WithTimeout(ctx, time.Second)
		defer cancel()
		_ = manager.Shutdown(shutdownCtx)
	})
	registry := authentication.NewConnectionRegistry()
	gateway, err := New(Config{Manager: manager, Authenticator: testAuthenticator{}, ConnectionRegistry: registry})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(gateway)
	t.Cleanup(server.Close)

	started := time.Now()
	defer func() { t.Logf("PERF_SAMPLE_NS=%d", time.Since(started).Nanoseconds()) }()
	connection, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.CloseNow()
	write := func(message protocol.Message) {
		t.Helper()
		if err := wsjson.Write(ctx, connection, message); err != nil {
			t.Fatal(err)
		}
	}
	read := func() protocol.Message {
		t.Helper()
		var message protocol.Message
		if err := wsjson.Read(ctx, connection, &message); err != nil {
			t.Fatal(err)
		}
		return message
	}

	write(protocol.Message{Type: protocol.TypeAuth, Token: "access"})
	if message := read(); message.Type != protocol.TypeAuthOK {
		t.Fatalf("auth response = %#v", message)
	}
	write(protocol.Message{Type: protocol.TypeJoin, DocID: "doc"})
	if message := read(); message.Type != protocol.TypeJoined || message.Content != "abc" {
		t.Fatalf("join response = %#v", message)
	}
	if message := read(); message.Type != protocol.TypePresenceChanged || len(message.UserIDs) != 1 {
		t.Fatalf("presence response = %#v", message)
	}
	operation := collab.Operation{Kind: collab.OperationInsert, Position: 1, Text: "X"}
	write(protocol.Message{Type: protocol.TypeEdit, DocID: "doc", RequestID: "request-1", BaseVersion: 0, Operation: &operation})
	if message := read(); message.Type != protocol.TypeEditApplied || message.Version != 1 {
		t.Fatalf("edit response = %#v", message)
	}

	registry.CloseSession("session")
	readCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	var ignored protocol.Message
	if err := wsjson.Read(readCtx, connection, &ignored); err == nil {
		t.Fatal("revoked session kept the websocket open")
	}
}

func TestGatewayClosesMalformedOversizedAndUnauthenticatedConnections(t *testing.T) {
	store := storage.NewMemory()
	manager := room.NewManager(store)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = manager.Shutdown(ctx)
	})
	gateway, err := New(Config{Manager: manager, Authenticator: testAuthenticator{}, MaxMessageSize: 64})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(gateway)
	t.Cleanup(server.Close)
	endpoint := "ws" + strings.TrimPrefix(server.URL, "http")

	tests := []struct {
		name    string
		payload []byte
	}{
		{name: "malformed JSON", payload: []byte("{")},
		{name: "oversized message", payload: []byte(`{"type":"auth","token":"` + strings.Repeat("x", 80) + `"}`)},
		{name: "message before authentication", payload: []byte(`{"type":"join","doc_id":"doc"}`)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			connection, _, err := websocket.Dial(ctx, endpoint, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer connection.CloseNow()
			if err := connection.Write(ctx, websocket.MessageText, test.payload); err != nil {
				t.Fatal(err)
			}
			for {
				if _, _, err := connection.Read(ctx); err != nil {
					return
				}
			}
		})
	}
}
