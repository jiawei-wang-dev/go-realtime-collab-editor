package room

import (
	"context"
	"errors"
	"fmt"
	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/collab"
	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/permission"
	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/protocol"
	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/storage"
	"sync"
	"testing"
	"time"
)

func TestMatchingStaleAndFutureVersions(t *testing.T) {
	store := storage.NewMemory()
	if _, err := store.CreateDocument(context.Background(), "doc", "ab", "owner"); err != nil {
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
	if _, err := store.CreateDocument(context.Background(), "doc", content, "owner"); err != nil {
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
	if _, err := store.CreateDocument(context.Background(), "private", "secret", "owner"); err != nil {
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

func TestSlowClientDoesNotBlockRoomAndIsDisconnected(t *testing.T) {
	store := storage.NewMemory()
	if _, err := store.CreateDocument(context.Background(), "doc", "abc", "owner"); err != nil {
		t.Fatal(err)
	}
	if err := store.ActivateOwnership(context.Background(), "doc", storage.LocalAuthority); err != nil {
		t.Fatal(err)
	}
	if err := store.SetPermission(context.Background(), permissionCommit("doc", "owner", "slow", permission.RoleRead)); err != nil {
		t.Fatal(err)
	}
	manager := NewManager(store)
	target, err := manager.Room("doc")
	if err != nil {
		t.Fatal(err)
	}
	owner := joinTestClient(t, target, "owner")
	slow := &Client{UserID: "slow", Send: make(chan protocol.Message, 1)}
	if err := target.Join(slow); err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() {
		done <- target.SubmitEdit(owner, "fast-edit", collab.Operation{
			Kind: collab.OperationInsert, Position: 0, Text: "X",
		})
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("room was blocked by slow client")
	}
	if message := <-owner.Send; message.Type != protocol.TypeEditApplied {
		t.Fatalf("owner message = %#v", message)
	}

	slow.mu.RLock()
	closed := slow.closed
	closeErr := slow.closeErr
	slow.mu.RUnlock()
	if !closed {
		t.Fatal("slow client was not disconnected")
	}
	if !errors.Is(closeErr, ErrSlowClient) {
		t.Fatalf("slow client close reason = %v, want ErrSlowClient", closeErr)
	}
	if document := target.Snapshot(); document.Content != "Xabc" || document.Version != 1 {
		t.Fatalf("document = %#v", document)
	}
}

func TestRoomIsReclaimedAndRecreatedAfterLastClientLeaves(t *testing.T) {
	store := storage.NewMemory()
	if _, err := store.CreateDocument(context.Background(), "doc", "abc", "owner"); err != nil {
		t.Fatal(err)
	}
	manager := NewManager(store, WithIdleTimeout(10*time.Millisecond))
	first, err := manager.Room("doc")
	if err != nil {
		t.Fatal(err)
	}
	owner := joinTestClient(t, first, "owner")
	first.Leave(owner)
	select {
	case <-first.Done():
	case <-time.After(time.Second):
		t.Fatal("empty room was not reclaimed")
	}
	if count := manager.roomCount(); count != 0 {
		t.Fatalf("room count = %d, want 0", count)
	}

	second, err := manager.Room("doc")
	if err != nil {
		t.Fatal(err)
	}
	if second == first {
		t.Fatal("manager returned stopped room")
	}
}

func TestManagerCreatesSingleRoomConcurrently(t *testing.T) {
	store := storage.NewMemory()
	if _, err := store.CreateDocument(context.Background(), "doc", "abc", "owner"); err != nil {
		t.Fatal(err)
	}
	manager := NewManager(store)
	const callers = 64
	rooms := make(chan *Room, callers)
	errorsFound := make(chan error, callers)
	var wait sync.WaitGroup
	for range callers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			target, err := manager.Room("doc")
			if err != nil {
				errorsFound <- err
				return
			}
			rooms <- target
		}()
	}
	wait.Wait()
	close(rooms)
	close(errorsFound)
	for err := range errorsFound {
		t.Fatal(err)
	}
	var first *Room
	for target := range rooms {
		if first == nil {
			first = target
		}
		if target != first {
			t.Fatal("concurrent callers received different rooms")
		}
	}
	if count := manager.roomCount(); count != 1 {
		t.Fatalf("room count = %d, want 1", count)
	}
}

func TestManagerActivatesHigherWriteAuthority(t *testing.T) {
	store := storage.NewMemory()
	if _, err := store.CreateDocument(context.Background(), "doc", "abc", "owner"); err != nil {
		t.Fatal(err)
	}
	nodeA := storage.WriteAuthority{NodeID: "node-a", FencingToken: 1}
	nodeB := storage.WriteAuthority{NodeID: "node-b", FencingToken: 2}
	if err := store.ActivateOwnership(context.Background(), "doc", nodeA); err != nil {
		t.Fatal(err)
	}
	manager := NewManager(store, WithWriteAuthority(nodeB))
	t.Cleanup(func() { _ = manager.Shutdown(context.Background()) })
	if _, err := manager.Room("doc"); err != nil {
		t.Fatal(err)
	}
	current, err := store.GetWriteAuthority(context.Background(), "doc")
	if err != nil {
		t.Fatal(err)
	}
	if current != nodeB {
		t.Fatalf("authority = %#v, want %#v", current, nodeB)
	}
}

func TestRoomStopExitsAndRejectsOldEvents(t *testing.T) {
	target, owner := newTestRoom(t, "abc")
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := target.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-target.Done():
	default:
		t.Fatal("room goroutine has not exited")
	}
	err := target.SubmitEdit(owner, "after-stop", collab.Operation{Kind: collab.OperationInsert, Text: "x"})
	if !errors.Is(err, ErrRoomClosed) {
		t.Fatalf("SubmitEdit after stop = %v, want ErrRoomClosed", err)
	}
}

func TestConcurrentJoinLeaveAndEdit(t *testing.T) {
	store := storage.NewMemory()
	if _, err := store.CreateDocument(context.Background(), "doc", "", "owner"); err != nil {
		t.Fatal(err)
	}
	manager := NewManager(store)
	target, err := manager.Room("doc")
	if err != nil {
		t.Fatal(err)
	}
	owner := joinTestClient(t, target, "owner")

	const clients = 32
	for index := range clients {
		if err := store.SetPermission(context.Background(), permissionCommit("doc", "owner", fmt.Sprintf("reader-%d", index), permission.RoleRead)); err != nil {
			t.Fatal(err)
		}
		if err := target.ChangePermission("owner", fmt.Sprintf("reader-%d", index), permission.RoleRead); err != nil {
			t.Fatal(err)
		}
	}
	var wait sync.WaitGroup
	for index := range clients {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			participant := &Client{UserID: fmt.Sprintf("reader-%d", index), Send: make(chan protocol.Message, 4)}
			if err := target.Join(participant); err == nil {
				<-participant.Send
				target.Leave(participant)
			}
		}(index)
	}
	for index := range clients {
		if err := target.SubmitEdit(owner, fmt.Sprintf("edit-%d", index), collab.Operation{
			BaseVersion: uint64(index), Kind: collab.OperationInsert, Position: index, Text: "x",
		}); err != nil {
			t.Fatal(err)
		}
		<-owner.Send
	}
	wait.Wait()
	if document := target.Snapshot(); document.Version != clients {
		t.Fatalf("version = %d, want %d", document.Version, clients)
	}
}

func TestEditStorageFailureDoesNotChangeRoomOrBroadcast(t *testing.T) {
	base := storage.NewMemory()
	if _, err := base.CreateDocument(context.Background(), "doc", "abc", "owner"); err != nil {
		t.Fatal(err)
	}
	persistErr := errors.New("database unavailable")
	manager := NewManager(&faultStore{Store: base, commitErr: persistErr})
	target, err := manager.Room("doc")
	if err != nil {
		t.Fatal(err)
	}
	owner := joinTestClient(t, target, "owner")
	err = target.SubmitEdit(owner, "failed-edit", collab.Operation{
		Kind: collab.OperationInsert, Position: 0, Text: "X",
	})
	assertCode(t, err, protocol.CodeInternalError)
	if message := <-owner.Send; message.Type != protocol.TypeError || message.Code != protocol.CodeInternalError {
		t.Fatalf("failure message = %#v", message)
	}
	if document := target.Snapshot(); document.Content != "abc" || document.Version != 0 {
		t.Fatalf("room changed after failed commit: %#v", document)
	}
	document, err := base.GetDocument(context.Background(), "doc")
	if err != nil {
		t.Fatal(err)
	}
	if document.Content != "abc" || document.Version != 0 {
		t.Fatalf("storage changed after failed commit: %#v", document)
	}
}

func TestDuplicateRequestDoesNotApplyTwice(t *testing.T) {
	target, owner := newTestRoom(t, "abc")
	operation := collab.Operation{Kind: collab.OperationInsert, Position: 0, Text: "X"}
	if err := target.SubmitEdit(owner, "same-request", operation); err != nil {
		t.Fatal(err)
	}
	<-owner.Send
	operation.BaseVersion = 1
	if err := target.SubmitEdit(owner, "same-request", operation); err != nil {
		t.Fatalf("duplicate retry error = %v", err)
	}
	if message := <-owner.Send; message.Type != protocol.TypeEditApplied || message.RequestID != "same-request" || message.Version != 1 {
		t.Fatalf("duplicate response = %#v", message)
	}
	if document := target.Snapshot(); document.Content != "Xabc" || document.Version != 1 {
		t.Fatalf("document changed after duplicate: %#v", document)
	}
}

func TestPermissionStorageFailureDoesNotChangeRoomOrNotify(t *testing.T) {
	base := storage.NewMemory()
	if _, err := base.CreateDocument(context.Background(), "doc", "", "owner"); err != nil {
		t.Fatal(err)
	}
	if err := base.ActivateOwnership(context.Background(), "doc", storage.LocalAuthority); err != nil {
		t.Fatal(err)
	}
	if err := base.SetPermission(context.Background(), permissionCommit("doc", "owner", "writer", permission.RoleWrite)); err != nil {
		t.Fatal(err)
	}
	manager := NewManager(&faultStore{Store: base, permissionErr: errors.New("commit failed")})
	target, err := manager.Room("doc")
	if err != nil {
		t.Fatal(err)
	}
	joinTestClient(t, target, "owner")
	writer := joinTestClient(t, target, "writer")
	err = target.ChangePermission("owner", "writer", permission.RoleRead)
	assertCode(t, err, protocol.CodeInternalError)
	select {
	case message := <-writer.Send:
		t.Fatalf("writer was notified after failed transaction: %#v", message)
	default:
	}
	role, err := target.Permission("owner", "writer")
	if err != nil {
		t.Fatal(err)
	}
	if role != permission.RoleWrite {
		t.Fatalf("room role = %q, want write", role)
	}
}

type faultStore struct {
	storage.Store
	commitErr     error
	permissionErr error
}

func (s *faultStore) CommitEdit(context.Context, storage.EditCommit) error {
	return s.commitErr
}

func (s *faultStore) SetPermission(context.Context, storage.PermissionCommit) error {
	return s.permissionErr
}

func permissionCommit(docID, actorID, userID string, role permission.Role) storage.PermissionCommit {
	return storage.PermissionCommit{
		Authority:  storage.LocalAuthority,
		DocumentID: docID,
		ActorID:    actorID,
		UserID:     userID,
		Role:       role,
	}
}
