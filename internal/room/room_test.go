package room

import (
	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/collab"
	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/permission"
	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/protocol"
	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/storage"
	"testing"
)

func TestMatchingStaleAndFutureVersions(t *testing.T) {
	store := storage.NewMemory()
	if _, err := store.CreateDocument("doc", "ab", "owner"); err != nil {
		t.Fatal(err)
	}
	manager := NewManager(store)
	room, err := manager.Room("doc")
	if err != nil {
		t.Fatal(err)
	}
	owner := joinTestClient(t, room, "owner")

	if err := room.SubmitEdit(owner, "op-1", collab.Operation{
		BaseVersion: 0,
		Kind:        collab.OperationInsert,
		Position:    1,
		Text:        "X",
	}); err != nil {
		t.Fatalf("matching edit: %v", err)
	}
	<-owner.Send

	if err := room.SubmitEdit(owner, "op-2", collab.Operation{
		BaseVersion: 0,
		Kind:        collab.OperationInsert,
		Position:    2,
		Text:        "Y",
	}); err != nil {
		t.Fatalf("stale edit: %v", err)
	}
	message := <-owner.Send
	if message.Operation == nil || message.Operation.Position != 3 {
		t.Fatalf("stale edit was not transformed: %#v", message)
	}

	err = room.SubmitEdit(owner, "op-3", collab.Operation{
		BaseVersion: 3,
		Kind:        collab.OperationInsert,
		Position:    0,
		Text:        "Z",
	})
	assertCode(t, err, protocol.CodeVersionAhead)
	<-owner.Send

	document := room.Snapshot()
	if document.Content != "aXbY" || document.Version != 2 {
		t.Fatalf("document = %#v, want content aXbY version 2", document)
	}
}

func TestStaleOperationTransformsOverFullHistory(t *testing.T) {
	room, owner := newTestRoom(t, "abcd")

	edits := []struct {
		requestID string
		operation collab.Operation
		wantPos   int
		wantVer   uint64
	}{
		{
			requestID: "op-1",
			operation: collab.Operation{BaseVersion: 0, Kind: collab.OperationInsert, Position: 1, Text: "X"},
			wantPos:   1,
			wantVer:   1,
		},
		{
			requestID: "op-2",
			operation: collab.Operation{BaseVersion: 1, Kind: collab.OperationInsert, Position: 3, Text: "Y"},
			wantPos:   3,
			wantVer:   2,
		},
		{
			requestID: "op-stale",
			operation: collab.Operation{BaseVersion: 0, Kind: collab.OperationInsert, Position: 4, Text: "Z"},
			wantPos:   6,
			wantVer:   3,
		},
	}

	for _, edit := range edits {
		if err := room.SubmitEdit(owner, edit.requestID, edit.operation); err != nil {
			t.Fatalf("SubmitEdit(%s) error = %v", edit.requestID, err)
		}
		message := <-owner.Send
		if message.Version != edit.wantVer || message.Operation == nil || message.Operation.Position != edit.wantPos {
			t.Fatalf("SubmitEdit(%s) message = %#v", edit.requestID, message)
		}
	}

	document := room.Snapshot()
	if document.Content != "aXbYcdZ" || document.Version != 3 {
		t.Fatalf("document = %#v, want content aXbYcdZ version 3", document)
	}
}

func TestPermissionRules(t *testing.T) {
	room, owner := newTestRoom(t, "abc")
	if err := room.ChangePermission("owner", "writer", permission.RoleWrite); err != nil {
		t.Fatal(err)
	}
	if err := room.ChangePermission("owner", "reader", permission.RoleRead); err != nil {
		t.Fatal(err)
	}
	writer := joinTestClient(t, room, "writer")
	reader := joinTestClient(t, room, "reader")

	tests := []struct {
		name    string
		client  *Client
		base    uint64
		wantErr string
	}{
		{name: "owner edit accepted", client: owner, base: 0},
		{name: "writer edit accepted", client: writer, base: 1},
		{name: "reader edit rejected", client: reader, base: 2, wantErr: protocol.CodePermissionDenied},
	}

	for index, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := room.SubmitEdit(tt.client, tt.name, collab.Operation{
				BaseVersion: tt.base,
				Kind:        collab.OperationInsert,
				Position:    index,
				Text:        "x",
			})
			if tt.wantErr != "" {
				assertCode(t, err, tt.wantErr)
				return
			}
			if err != nil {
				t.Fatalf("SubmitEdit() error = %v", err)
			}
			drainBroadcasts(owner, writer, reader)
		})
	}

	if err := room.ChangePermission("writer", "reader", permission.RoleWrite); err == nil {
		t.Fatal("writer permission change unexpectedly succeeded")
	} else {
		assertCode(t, err, protocol.CodePermissionDenied)
	}

	if err := room.ChangePermission("owner", "writer", permission.RoleRead); err != nil {
		t.Fatalf("owner permission change: %v", err)
	}
	changed := <-writer.Send
	if changed.Type != protocol.TypePermissionChanged || changed.Role != permission.RoleRead {
		t.Fatalf("permission message = %#v", changed)
	}

	err := room.SubmitEdit(writer, "after-downgrade", collab.Operation{
		BaseVersion: room.Snapshot().Version,
		Kind:        collab.OperationInsert,
		Position:    0,
		Text:        "blocked",
	})
	assertCode(t, err, protocol.CodePermissionDenied)
	if message := <-writer.Send; message.Code != protocol.CodePermissionDenied {
		t.Fatalf("error message = %#v", message)
	}

	if err := room.SubmitEdit(owner, "owner-after-downgrade", collab.Operation{
		BaseVersion: room.Snapshot().Version,
		Kind:        collab.OperationInsert,
		Position:    0,
		Text:        "y",
	}); err != nil {
		t.Fatalf("owner edit after writer downgrade: %v", err)
	}
	if message := <-writer.Send; message.Type != protocol.TypeEditApplied {
		t.Fatalf("downgraded writer did not receive update: %#v", message)
	}
}

func newTestRoom(t *testing.T, content string) (*Room, *Client) {
	t.Helper()
	store := storage.NewMemory()
	if _, err := store.CreateDocument("doc", content, "owner"); err != nil {
		t.Fatal(err)
	}
	manager := NewManager(store)
	room, err := manager.Room("doc")
	if err != nil {
		t.Fatal(err)
	}
	return room, joinTestClient(t, room, "owner")
}

func joinTestClient(t *testing.T, room *Room, userID string) *Client {
	t.Helper()
	client := &Client{UserID: userID, Send: make(chan protocol.Message, 16)}
	if err := room.Join(client); err != nil {
		t.Fatal(err)
	}
	if message := <-client.Send; message.Type != protocol.TypeJoined {
		t.Fatalf("join message = %#v", message)
	}
	return client
}

func assertCode(t *testing.T, err error, code string) {
	t.Helper()
	coded, ok := err.(*CodedError)
	if !ok || coded.Code != code {
		t.Fatalf("error = %#v, want code %s", err, code)
	}
}

func drainBroadcasts(clients ...*Client) {
	for _, client := range clients {
		select {
		case <-client.Send:
		default:
		}
	}
}

func TestUserWithoutReadPermissionCannotReadDocument(t *testing.T) {
	room, _ := newTestRoom(t, "secret")

	if _, err := room.ReadDocument("unknown"); err == nil {
		t.Fatal("unauthorized document read unexpectedly succeeded")
	} else {
		assertCode(t, err, protocol.CodePermissionDenied)
	}

	document, err := room.ReadDocument("owner")
	if err != nil {
		t.Fatalf("owner read: %v", err)
	}
	if document.Content != "secret" {
		t.Fatalf("document = %#v", document)
	}
}

func TestUserWithoutReadPermissionCannotJoin(t *testing.T) {
	store := storage.NewMemory()
	if _, err := store.CreateDocument("private", "secret", "owner"); err != nil {
		t.Fatal(err)
	}
	manager := NewManager(store)
	targetRoom, err := manager.Room("private")
	if err != nil {
		t.Fatal(err)
	}

	unknown := &Client{UserID: "unknown", Send: make(chan protocol.Message, 1)}
	err = targetRoom.Join(unknown)
	assertCode(t, err, protocol.CodePermissionDenied)

	select {
	case message := <-unknown.Send:
		t.Fatalf("unauthorized user unexpectedly received message: %#v", message)
	default:
	}
}
