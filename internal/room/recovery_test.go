package room

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/collab"
	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/permission"
	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/protocol"
	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/storage"
)

func TestSnapshotPrunesHistoryAndResumeChoosesSyncOrResync(t *testing.T) {
	store := storage.NewMemory()
	if _, err := store.CreateDocument(context.Background(), "doc", "a", "owner"); err != nil {
		t.Fatal(err)
	}
	manager := NewManager(store, WithSnapshotPolicy(2, 10, time.Second))
	t.Cleanup(func() { _ = manager.Shutdown(context.Background()) })
	target, err := manager.Room("doc")
	if err != nil {
		t.Fatal(err)
	}
	owner := joinTestClient(t, target, "owner")
	applyInsert(t, target, owner, "one", 0, 1, "X")
	applyInsert(t, target, owner, "two", 1, 2, "Y")
	waitSnapshot(t, store, "doc", 2)
	time.Sleep(20 * time.Millisecond)

	current := &Client{UserID: "owner", Send: make(chan protocol.Message, 2)}
	if err := target.ResumeContext(context.Background(), current, 2); err != nil {
		t.Fatal(err)
	}
	message := <-current.Send
	if message.Type != protocol.TypeSync || message.FromVersion != 2 || message.ToVersion != 2 || len(message.Operations) != 0 {
		t.Fatalf("current resume = %#v", message)
	}
	target.Leave(current)

	old := &Client{UserID: "owner", Send: make(chan protocol.Message, 2)}
	if err := target.ResumeContext(context.Background(), old, 0); err != nil {
		t.Fatal(err)
	}
	message = <-old.Send
	if message.Type != protocol.TypeResyncRequired || message.Snapshot == nil || message.Snapshot.Version != 2 || message.Snapshot.Content != "aXY" || message.CurrentVersion != 2 {
		t.Fatalf("old resume = %#v", message)
	}
	target.Leave(old)
	future := &Client{UserID: "owner", Send: make(chan protocol.Message, 2)}
	if err := target.ResumeContext(context.Background(), future, 99); err != nil {
		t.Fatal(err)
	}
	if message := <-future.Send; message.Type != protocol.TypeResyncRequired || message.CurrentVersion != 2 {
		t.Fatalf("future resume = %#v", message)
	}
	target.Leave(future)

	err = target.SubmitEdit(owner, "too-old", collab.Operation{BaseVersion: 0, Kind: collab.OperationInsert, Position: 0, Text: "Z"})
	assertCode(t, err, protocol.CodeResyncRequired)
	if rejected := <-owner.Send; rejected.Code != protocol.CodeResyncRequired {
		t.Fatalf("old edit response = %#v", rejected)
	}
}

func TestConcurrentResumeJoinLeaveAndEdit(t *testing.T) {
	store := storage.NewMemory()
	_, _ = store.CreateDocument(context.Background(), "doc", "", "owner")
	_ = store.ActivateOwnership(context.Background(), "doc", storage.LocalAuthority)
	_ = store.SetPermission(context.Background(), permissionCommit("doc", "owner", "reader", permission.RoleRead))
	manager := NewManager(store, WithSnapshotPolicy(100, 200, time.Second))
	t.Cleanup(func() { _ = manager.Shutdown(context.Background()) })
	target, _ := manager.Room("doc")
	owner := &Client{UserID: "owner", Send: make(chan protocol.Message, 128)}
	if err := target.Join(owner); err != nil {
		t.Fatal(err)
	}
	<-owner.Send

	var wait sync.WaitGroup
	errorsSeen := make(chan error, 20)
	for range 20 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			client := &Client{UserID: "reader", Send: make(chan protocol.Message, 64)}
			if err := target.ResumeContext(context.Background(), client, 0); err != nil {
				errorsSeen <- err
				return
			}
			if err := target.LeaveContext(context.Background(), client); err != nil {
				errorsSeen <- err
			}
		}()
	}
	for version := range 20 {
		if err := target.SubmitEdit(owner, fmt.Sprintf("concurrent-%d", version), collab.Operation{
			BaseVersion: uint64(version), Kind: collab.OperationInsert, Position: version, Text: "x",
		}); err != nil {
			t.Fatal(err)
		}
		<-owner.Send
	}
	wait.Wait()
	close(errorsSeen)
	for err := range errorsSeen {
		t.Fatalf("concurrent room operation: %v", err)
	}
	if document := target.Snapshot(); document.Version != 20 {
		t.Fatalf("version = %d, want 20", document.Version)
	}
}

func TestResumeFailsAfterRoomStops(t *testing.T) {
	store := storage.NewMemory()
	_, _ = store.CreateDocument(context.Background(), "doc", "", "owner")
	manager := NewManager(store)
	target, _ := manager.Room("doc")
	if err := manager.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	client := &Client{UserID: "owner", Send: make(chan protocol.Message, 1)}
	if err := target.ResumeContext(context.Background(), client, 0); !errors.Is(err, ErrRoomClosed) {
		t.Fatalf("resume after stop error = %v", err)
	}
}

func TestResumeIncrementalOperationsAndSubsequentBroadcast(t *testing.T) {
	store := storage.NewMemory()
	if _, err := store.CreateDocument(context.Background(), "doc", "", "owner"); err != nil {
		t.Fatal(err)
	}
	manager := NewManager(store, WithSnapshotPolicy(10, 20, time.Second))
	t.Cleanup(func() { _ = manager.Shutdown(context.Background()) })
	target, _ := manager.Room("doc")
	owner := joinTestClient(t, target, "owner")
	applyInsert(t, target, owner, "one", 0, 0, "A")
	applyInsert(t, target, owner, "two", 1, 1, "B")

	resumed := &Client{UserID: "owner", Send: make(chan protocol.Message, 4)}
	if err := target.ResumeContext(context.Background(), resumed, 1); err != nil {
		t.Fatal(err)
	}
	syncMessage := <-resumed.Send
	if syncMessage.Type != protocol.TypeSync || len(syncMessage.Operations) != 1 || syncMessage.Operations[0].Version != 2 {
		t.Fatalf("incremental sync = %#v", syncMessage)
	}
	applyInsert(t, target, owner, "three", 2, 2, "C")
	if live := <-resumed.Send; live.Type != protocol.TypeEditApplied || live.Version != 3 {
		t.Fatalf("live message after resume = %#v", live)
	}
}

func TestResumeChecksReadPermission(t *testing.T) {
	store := storage.NewMemory()
	_, _ = store.CreateDocument(context.Background(), "doc", "abc", "owner")
	_ = store.ActivateOwnership(context.Background(), "doc", storage.LocalAuthority)
	_ = store.SetPermission(context.Background(), permissionCommit("doc", "owner", "reader", permission.RoleRead))
	manager := NewManager(store)
	t.Cleanup(func() { _ = manager.Shutdown(context.Background()) })
	target, _ := manager.Room("doc")
	reader := &Client{UserID: "reader", Send: make(chan protocol.Message, 1)}
	if err := target.ResumeContext(context.Background(), reader, 0); err != nil {
		t.Fatalf("reader resume: %v", err)
	}
	if message := <-reader.Send; message.Type != protocol.TypeSync {
		t.Fatalf("reader sync = %#v", message)
	}
	unauthorized := &Client{UserID: "stranger", Send: make(chan protocol.Message, 1)}
	err := target.ResumeContext(context.Background(), unauthorized, 0)
	assertCode(t, err, protocol.CodePermissionDenied)
}

func TestSnapshotFailureRetainsHistoryAndRetries(t *testing.T) {
	base := storage.NewMemory()
	_, _ = base.CreateDocument(context.Background(), "doc", "", "owner")
	store := &snapshotFaultStore{Store: base, remainingFailures: 1}
	manager := NewManager(store, WithSnapshotPolicy(1, 10, 100*time.Millisecond))
	t.Cleanup(func() { _ = manager.Shutdown(context.Background()) })
	target, _ := manager.Room("doc")
	owner := joinTestClient(t, target, "owner")
	applyInsert(t, target, owner, "one", 0, 0, "A")

	// The first save fails; history remains available for incremental resume.
	time.Sleep(150 * time.Millisecond)
	resumed := &Client{UserID: "owner", Send: make(chan protocol.Message, 2)}
	if err := target.ResumeContext(context.Background(), resumed, 0); err != nil {
		t.Fatal(err)
	}
	if message := <-resumed.Send; message.Type != protocol.TypeSync || len(message.Operations) != 1 {
		t.Fatalf("resume before retry = %#v", message)
	}
	target.Leave(resumed)

	waitSnapshot(t, base, "doc", 1)
	time.Sleep(20 * time.Millisecond)
	resynced := &Client{UserID: "owner", Send: make(chan protocol.Message, 2)}
	if err := target.ResumeContext(context.Background(), resynced, 0); err != nil {
		t.Fatal(err)
	}
	if message := <-resynced.Send; message.Type != protocol.TypeResyncRequired || message.Snapshot == nil || message.Snapshot.Version != 1 {
		t.Fatalf("resume after retry = %#v", message)
	}
}

func TestHistoryHardLimitRejectsNewEdits(t *testing.T) {
	base := storage.NewMemory()
	_, _ = base.CreateDocument(context.Background(), "doc", "", "owner")
	store := &snapshotFaultStore{Store: base, alwaysFail: true}
	manager := NewManager(store, WithSnapshotPolicy(1, 2, 50*time.Millisecond))
	t.Cleanup(func() { _ = manager.Shutdown(context.Background()) })
	target, _ := manager.Room("doc")
	owner := joinTestClient(t, target, "owner")
	applyInsert(t, target, owner, "one", 0, 0, "A")
	applyInsert(t, target, owner, "two", 1, 1, "B")
	err := target.SubmitEdit(owner, "three", collab.Operation{BaseVersion: 2, Kind: collab.OperationInsert, Position: 2, Text: "C"})
	assertCode(t, err, protocol.CodeServiceUnavailable)
	if message := <-owner.Send; message.Code != protocol.CodeServiceUnavailable {
		t.Fatalf("hard limit response = %#v", message)
	}
}

func TestDuplicateRetryReturnsOriginalOnlyToOriginalUser(t *testing.T) {
	store := storage.NewMemory()
	_, _ = store.CreateDocument(context.Background(), "doc", "", "owner")
	_ = store.ActivateOwnership(context.Background(), "doc", storage.LocalAuthority)
	_ = store.SetPermission(context.Background(), permissionCommit("doc", "owner", "writer", permission.RoleWrite))
	manager := NewManager(store)
	t.Cleanup(func() { _ = manager.Shutdown(context.Background()) })
	target, _ := manager.Room("doc")
	owner := joinTestClient(t, target, "owner")
	writer := joinTestClient(t, target, "writer")
	applyInsert(t, target, owner, "request", 0, 0, "A")
	<-writer.Send
	if err := target.SubmitEdit(owner, "request", collab.Operation{BaseVersion: 1, Kind: collab.OperationInsert, Position: 1, Text: "ignored"}); err != nil {
		t.Fatal(err)
	}
	if message := <-owner.Send; message.Type != protocol.TypeEditApplied || message.Version != 1 || message.Operation == nil || message.Operation.Text != "A" {
		t.Fatalf("retry response = %#v", message)
	}
	select {
	case message := <-writer.Send:
		t.Fatalf("retry was rebroadcast: %#v", message)
	default:
	}
	err := target.SubmitEdit(writer, "request", collab.Operation{BaseVersion: 1, Kind: collab.OperationInsert, Position: 1, Text: "B"})
	assertCode(t, err, protocol.CodeDuplicateRequest)
	if message := <-writer.Send; message.Code != protocol.CodeDuplicateRequest || message.Operation != nil {
		t.Fatalf("cross-user duplicate response = %#v", message)
	}
}

type snapshotFaultStore struct {
	storage.Store
	mu                sync.Mutex
	remainingFailures int
	alwaysFail        bool
}

func (s *snapshotFaultStore) SaveSnapshot(ctx context.Context, snapshot storage.SnapshotCommit) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.alwaysFail || s.remainingFailures > 0 {
		if s.remainingFailures > 0 {
			s.remainingFailures--
		}
		return errors.New("snapshot unavailable")
	}
	return s.Store.SaveSnapshot(ctx, snapshot)
}

func applyInsert(t *testing.T, target *Room, client *Client, requestID string, base uint64, position int, text string) {
	t.Helper()
	if err := target.SubmitEdit(client, requestID, collab.Operation{BaseVersion: base, Kind: collab.OperationInsert, Position: position, Text: text}); err != nil {
		t.Fatal(err)
	}
	if message := <-client.Send; message.Type != protocol.TypeEditApplied {
		t.Fatalf("edit response = %#v", message)
	}
}

func waitSnapshot(t *testing.T, store storage.Store, docID string, version uint64) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		snapshot, err := store.GetLatestSnapshot(context.Background(), docID)
		if err == nil && snapshot.Version >= version {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("snapshot version %d was not saved", version)
}
