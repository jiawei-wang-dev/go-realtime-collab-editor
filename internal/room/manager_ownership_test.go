package room

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/cluster"
	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/collab"
	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/storage"
)

func TestClusterManagerOpensOneRoomAndStopsOnOwnershipCancellation(t *testing.T) {
	store := storage.NewMemory()
	if _, err := store.CreateDocument(context.Background(), "doc", "abc", "owner"); err != nil {
		t.Fatal(err)
	}
	coordinator := &fakeOwnershipCoordinator{}
	manager := NewManager(store, WithOwnership(coordinator))

	const callers = 32
	rooms := make(chan *Room, callers)
	errorsFound := make(chan error, callers)
	var callersDone sync.WaitGroup
	for index := 0; index < callers; index++ {
		callersDone.Add(1)
		go func() {
			defer callersDone.Done()
			target, err := manager.Room("doc")
			if err != nil {
				errorsFound <- err
				return
			}
			rooms <- target
		}()
	}
	callersDone.Wait()
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
			t.Fatal("concurrent cluster callers received different rooms")
		}
	}
	if calls := coordinator.acquireCalls.Load(); calls != 1 {
		t.Fatalf("Acquire calls = %d, want 1", calls)
	}

	coordinator.cancelOwnership(cluster.ErrOwnershipLost)
	select {
	case <-first.Done():
	case <-time.After(time.Second):
		t.Fatal("room did not stop after ownership cancellation")
	}
	deadline := time.Now().Add(time.Second)
	for manager.roomCount() != 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if count := manager.roomCount(); count != 0 {
		t.Fatalf("room count after ownership loss = %d", count)
	}
}

func TestManagerAutomaticallyRecoversFailoverCandidate(t *testing.T) {
	store := storage.NewMemory()
	if _, err := store.CreateDocument(context.Background(), "doc", "abc", "owner"); err != nil {
		t.Fatal(err)
	}
	coordinator := &fakeOwnershipCoordinator{candidates: make(chan string, 1)}
	manager := NewManager(store, WithOwnership(coordinator))
	t.Cleanup(func() { _ = manager.Shutdown(context.Background()) })
	coordinator.candidates <- "doc"

	deadline := time.Now().Add(time.Second)
	for (coordinator.acquireCalls.Load() != 1 || manager.roomCount() != 1) && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if coordinator.acquireCalls.Load() != 1 || manager.roomCount() != 1 {
		t.Fatalf("automatic recovery acquire calls = %d, rooms = %d", coordinator.acquireCalls.Load(), manager.roomCount())
	}
}

func TestOwnershipCancellationClosesClientsAndRejectsConcurrentEdit(t *testing.T) {
	store := storage.NewMemory()
	if _, err := store.CreateDocument(context.Background(), "doc", "", "owner"); err != nil {
		t.Fatal(err)
	}
	coordinator := &fakeOwnershipCoordinator{}
	manager := NewManager(store, WithOwnership(coordinator))
	t.Cleanup(func() { _ = manager.Shutdown(context.Background()) })
	target, err := manager.Room("doc")
	if err != nil {
		t.Fatal(err)
	}
	client := joinTestClient(t, target, "owner")
	coordinator.cancelOwnership(cluster.ErrOwnershipLost)
	select {
	case <-target.Done():
	case <-time.After(time.Second):
		t.Fatal("room did not self-fence after ownership cancellation")
	}
	if err := target.SubmitEditContext(context.Background(), client, "after-fence", collab.Operation{Kind: collab.OperationInsert, Position: 0, Text: "X"}); !errors.Is(err, ErrRoomClosed) {
		t.Fatalf("edit after self-fence error = %v", err)
	}
	if !client.closed || !errors.Is(client.closeErr, ErrRoomClosed) {
		t.Fatalf("client close state = %t, error = %v", client.closed, client.closeErr)
	}
}

type fakeOwnershipCoordinator struct {
	acquireCalls atomic.Int32
	mu           sync.Mutex
	handle       *cluster.OwnershipHandle
	candidates   chan string
}

func (coordinator *fakeOwnershipCoordinator) FailoverCandidates() <-chan string {
	return coordinator.candidates
}

func (coordinator *fakeOwnershipCoordinator) Acquire(_ context.Context, documentID string) (*cluster.OwnershipHandle, error) {
	coordinator.acquireCalls.Add(1)
	time.Sleep(20 * time.Millisecond)
	handle := cluster.NewOwnershipHandle(context.Background(), documentID, cluster.Node{NodeID: "node-a", Incarnation: "process-a"}, 10)
	coordinator.mu.Lock()
	coordinator.handle = handle
	coordinator.mu.Unlock()
	return handle, nil
}

func (*fakeOwnershipCoordinator) BeginRecovery(context.Context, *cluster.OwnershipHandle) error {
	return nil
}

func (*fakeOwnershipCoordinator) Activate(context.Context, *cluster.OwnershipHandle) error {
	return nil
}

func (*fakeOwnershipCoordinator) BeginDraining(context.Context, *cluster.OwnershipHandle) error {
	return nil
}

func (*fakeOwnershipCoordinator) Release(_ context.Context, handle *cluster.OwnershipHandle) error {
	handle.Cancel(nil)
	return nil
}

func (coordinator *fakeOwnershipCoordinator) cancelOwnership(cause error) {
	coordinator.mu.Lock()
	handle := coordinator.handle
	coordinator.mu.Unlock()
	if handle != nil {
		handle.Cancel(cause)
	}
}
