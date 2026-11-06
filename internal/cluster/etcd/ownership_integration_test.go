package etcd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/cluster"
	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/collab"
	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/migration"
	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/permission"
	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/protocol"
	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/room"
	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/storage"
	mysqlstorage "github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/storage/mysql"
)

type blockingRecoveryStore struct {
	storage.Store
	nodeID  string
	once    sync.Once
	entered chan storage.WriteAuthority
	release chan struct{}
}

type failRecoveryOnceStore struct {
	storage.Store
	nodeID string
	once   sync.Once
}

func (store *failRecoveryOnceStore) LoadRecoveryState(ctx context.Context, documentID string, authority storage.WriteAuthority) (storage.RecoveryState, error) {
	failed := false
	if authority.NodeID == store.nodeID {
		store.once.Do(func() { failed = true })
	}
	if failed {
		return storage.RecoveryState{}, errors.New("injected temporary recovery failure")
	}
	return store.Store.LoadRecoveryState(ctx, documentID, authority)
}

func (store *blockingRecoveryStore) LoadRecoveryState(ctx context.Context, documentID string, authority storage.WriteAuthority) (storage.RecoveryState, error) {
	if authority.NodeID == store.nodeID {
		blocked := false
		store.once.Do(func() {
			blocked = true
			store.entered <- authority
		})
		if blocked {
			select {
			case <-store.release:
			case <-ctx.Done():
				return storage.RecoveryState{}, ctx.Err()
			}
		}
	}
	return store.Store.LoadRecoveryState(ctx, documentID, authority)
}

func TestFailoverDuringRecoveryIsRecoveredByThirdNode(t *testing.T) {
	acquireIntegrationLock(t)
	store := openOwnershipMySQL(t)
	documentID := fmt.Sprintf("phase85/recovery-crash/%d", time.Now().UnixNano())
	if _, err := store.CreateDocument(context.Background(), documentID, "state", "owner"); err != nil {
		t.Fatal(err)
	}

	clientA := openIntegrationClient(t)
	registryA := startIntegrationRegistry(t, clientA, integrationNode(fmt.Sprintf("phase85-recovery-a-%d", time.Now().UnixNano()), "a"), 3*time.Second)
	if err := registryA.MarkReady(context.Background()); err != nil {
		t.Fatal(err)
	}
	ownershipA, _ := NewOwnership(registryA)
	managerA := room.NewManager(store, room.WithOwnership(ownershipA), room.WithIdleTimeout(time.Minute))
	if _, err := managerA.Room(documentID); err != nil {
		t.Fatal(err)
	}

	clientB := openIntegrationClient(t)
	nodeB := integrationNode(fmt.Sprintf("phase85-recovery-b-%d", time.Now().UnixNano()), "b")
	registryB := startIntegrationRegistry(t, clientB, nodeB, 3*time.Second)
	if err := registryB.MarkReady(context.Background()); err != nil {
		t.Fatal(err)
	}
	ownershipB, _ := NewOwnership(registryB)
	blockedStore := &blockingRecoveryStore{Store: store, nodeID: nodeB.NodeID, entered: make(chan storage.WriteAuthority, 1), release: make(chan struct{})}
	managerB := room.NewManager(blockedStore, room.WithOwnership(ownershipB), room.WithIdleTimeout(time.Minute))

	if err := clientA.Close(); err != nil {
		t.Fatal(err)
	}
	var authorityB storage.WriteAuthority
	select {
	case authorityB = <-blockedStore.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("second node did not enter recovery")
	}
	ownerDuringRecovery, err := ownershipB.Resolve(context.Background(), documentID)
	if err != nil || ownerDuringRecovery.State != cluster.OwnerRecovering {
		t.Fatalf("owner during recovery = %#v, %v", ownerDuringRecovery, err)
	}

	clientC := openIntegrationClient(t)
	registryC := startIntegrationRegistry(t, clientC, integrationNode(fmt.Sprintf("phase85-recovery-c-%d", time.Now().UnixNano()), "c"), 3*time.Second)
	if err := registryC.MarkReady(context.Background()); err != nil {
		t.Fatal(err)
	}
	ownershipC, _ := NewOwnership(registryC)
	managerC := room.NewManager(store, room.WithOwnership(ownershipC), room.WithIdleTimeout(time.Minute))
	t.Cleanup(func() {
		close(blockedStore.release)
		_ = managerA.Shutdown(context.Background())
		_ = managerB.Shutdown(context.Background())
		_ = managerC.Shutdown(context.Background())
		_ = registryC.Close(context.Background())
		_ = clientB.Close()
		_ = clientC.Close()
	})

	if err := clientB.Close(); err != nil {
		t.Fatal(err)
	}
	var ownerC cluster.Owner
	waitFor(t, 10*time.Second, func() bool {
		owner, resolveErr := ownershipC.Resolve(context.Background(), documentID)
		if resolveErr == nil && owner.State == cluster.OwnerActive && owner.NodeID == registryC.Node().NodeID {
			ownerC = owner
			return true
		}
		return false
	})
	handleC := waitForOwnershipHandle(t, ownershipC, documentID)
	if handleC.FencingToken <= authorityB.FencingToken {
		t.Fatalf("third-node token %d did not exceed interrupted recovery token %d", handleC.FencingToken, authorityB.FencingToken)
	}
	target, err := managerC.Room(documentID)
	if err != nil {
		t.Fatal(err)
	}
	recovered, err := target.SnapshotContext(context.Background())
	if err != nil || recovered.Content != "state" || recovered.Version != 0 {
		t.Fatalf("third-node recovery = %#v, %v, owner %#v", recovered, err, ownerC)
	}
}

func TestAutomaticFailoverRetriesAfterTemporaryRecoveryFailure(t *testing.T) {
	acquireIntegrationLock(t)
	store := openOwnershipMySQL(t)
	documentID := fmt.Sprintf("phase85/recovery-retry/%d", time.Now().UnixNano())
	if _, err := store.CreateDocument(context.Background(), documentID, "retry", "owner"); err != nil {
		t.Fatal(err)
	}
	clientA := openIntegrationClient(t)
	registryA := startIntegrationRegistry(t, clientA, integrationNode(fmt.Sprintf("phase85-retry-a-%d", time.Now().UnixNano()), "a"), 3*time.Second)
	if err := registryA.MarkReady(context.Background()); err != nil {
		t.Fatal(err)
	}
	ownershipA, _ := NewOwnership(registryA)
	managerA := room.NewManager(store, room.WithOwnership(ownershipA), room.WithIdleTimeout(time.Minute))
	if _, err := managerA.Room(documentID); err != nil {
		t.Fatal(err)
	}
	handleA := waitForOwnershipHandle(t, ownershipA, documentID)

	clientB := openIntegrationClient(t)
	nodeB := integrationNode(fmt.Sprintf("phase85-retry-b-%d", time.Now().UnixNano()), "b")
	registryB := startIntegrationRegistry(t, clientB, nodeB, 3*time.Second)
	if err := registryB.MarkReady(context.Background()); err != nil {
		t.Fatal(err)
	}
	ownershipB, _ := NewOwnership(registryB)
	faultStore := &failRecoveryOnceStore{Store: store, nodeID: nodeB.NodeID}
	managerB := room.NewManager(faultStore, room.WithOwnership(ownershipB), room.WithIdleTimeout(time.Minute))
	t.Cleanup(func() {
		_ = managerA.Shutdown(context.Background())
		_ = managerB.Shutdown(context.Background())
		_ = registryB.Close(context.Background())
		_ = clientB.Close()
	})

	if err := clientA.Close(); err != nil {
		t.Fatal(err)
	}
	var active cluster.Owner
	waitFor(t, 12*time.Second, func() bool {
		owner, resolveErr := ownershipB.Resolve(context.Background(), documentID)
		if resolveErr == nil && owner.State == cluster.OwnerActive && owner.NodeID == nodeB.NodeID {
			active = owner
			return true
		}
		return false
	})
	handleB := waitForOwnershipHandle(t, ownershipB, documentID)
	if handleB.FencingToken <= handleA.FencingToken+1 {
		t.Fatalf("retry token %d does not show a failed acquisition after old token %d", handleB.FencingToken, handleA.FencingToken)
	}
	target, err := managerB.Room(documentID)
	if err != nil {
		t.Fatal(err)
	}
	recovered, err := target.SnapshotContext(context.Background())
	if err != nil || recovered.Content != "retry" || recovered.Version != 0 || active.NodeID != nodeB.NodeID {
		t.Fatalf("retried recovery = %#v, owner %#v, %v", recovered, active, err)
	}
}

func TestGracefulShutdownReleasesOwnershipForNextNode(t *testing.T) {
	acquireIntegrationLock(t)
	store := openOwnershipMySQL(t)
	documentID := fmt.Sprintf("phase85/graceful/%d", time.Now().UnixNano())
	if _, err := store.CreateDocument(context.Background(), documentID, "safe", "owner"); err != nil {
		t.Fatal(err)
	}
	clientA := openIntegrationClient(t)
	registryA := startIntegrationRegistry(t, clientA, integrationNode(fmt.Sprintf("phase85-graceful-a-%d", time.Now().UnixNano()), "a"), 3*time.Second)
	if err := registryA.MarkReady(context.Background()); err != nil {
		t.Fatal(err)
	}
	ownershipA, _ := NewOwnership(registryA)
	managerA := room.NewManager(store, room.WithOwnership(ownershipA), room.WithIdleTimeout(time.Minute))
	if _, err := managerA.Room(documentID); err != nil {
		t.Fatal(err)
	}
	handleA := waitForOwnershipHandle(t, ownershipA, documentID)

	if err := managerA.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := registryA.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := clientA.Close(); err != nil {
		t.Fatal(err)
	}
	clientB := openIntegrationClient(t)
	registryB := startIntegrationRegistry(t, clientB, integrationNode(fmt.Sprintf("phase85-graceful-b-%d", time.Now().UnixNano()), "b"), 3*time.Second)
	defer clientB.Close()
	defer closeRegistry(t, registryB)
	if err := registryB.MarkReady(context.Background()); err != nil {
		t.Fatal(err)
	}
	ownershipB, _ := NewOwnership(registryB)
	managerB := room.NewManager(store, room.WithOwnership(ownershipB), room.WithIdleTimeout(time.Minute))
	defer managerB.Shutdown(context.Background())
	if _, err := managerB.Room(documentID); err != nil {
		t.Fatal(err)
	}
	handleB := waitForOwnershipHandle(t, ownershipB, documentID)
	if handleB.FencingToken <= handleA.FencingToken {
		t.Fatalf("graceful takeover token %d did not exceed %d", handleB.FencingToken, handleA.FencingToken)
	}
	owner, err := ownershipB.Resolve(context.Background(), documentID)
	if err != nil || owner.State != cluster.OwnerActive {
		t.Fatalf("owner after graceful takeover = %#v, %v", owner, err)
	}
}

func TestAutomaticFailoverRecoversRoomAndFencesOldOwner(t *testing.T) {
	acquireIntegrationLock(t)
	store := openOwnershipMySQL(t)
	documentID := fmt.Sprintf("phase85/failover/%d", time.Now().UnixNano())
	if _, err := store.CreateDocument(context.Background(), documentID, "", "owner"); err != nil {
		t.Fatal(err)
	}

	clientA := openIntegrationClient(t)
	registryA := startIntegrationRegistry(t, clientA, integrationNode(fmt.Sprintf("phase85-a-%d", time.Now().UnixNano()), "process-a"), 3*time.Second)
	if err := registryA.MarkReady(context.Background()); err != nil {
		t.Fatal(err)
	}
	ownershipA, err := NewOwnership(registryA)
	if err != nil {
		t.Fatal(err)
	}
	managerA := room.NewManager(store, room.WithOwnership(ownershipA), room.WithSnapshotPolicy(1, 20, time.Second))
	t.Cleanup(func() { _ = managerA.Shutdown(context.Background()) })
	roomA, err := managerA.Room(documentID)
	if err != nil {
		t.Fatal(err)
	}
	handleA := waitForOwnershipHandle(t, ownershipA, documentID)
	writerA := &room.Client{UserID: "owner", Send: make(chan protocol.Message, 8)}
	if err := roomA.Join(writerA); err != nil {
		t.Fatal(err)
	}
	<-writerA.Send
	applyFailoverEdit(t, roomA, writerA, "before-a", 0, 0, "A")
	applyFailoverEdit(t, roomA, writerA, "before-b", 1, 1, "B")
	waitFor(t, 5*time.Second, func() bool {
		snapshot, snapshotErr := store.GetLatestSnapshot(context.Background(), documentID)
		return snapshotErr == nil && snapshot.Version == 2 && snapshot.Content == "AB"
	})

	type candidate struct {
		registry  *Registry
		ownership *Ownership
		manager   *room.Manager
		client    *Client
	}
	candidates := make([]candidate, 0, 2)
	for _, nodeID := range []string{"phase85-b", "phase85-c"} {
		client := openIntegrationClient(t)
		registry := startIntegrationRegistry(t, client, integrationNode(fmt.Sprintf("%s-%d", nodeID, time.Now().UnixNano()), "process"), 3*time.Second)
		if err := registry.MarkReady(context.Background()); err != nil {
			t.Fatal(err)
		}
		ownership, err := NewOwnership(registry)
		if err != nil {
			t.Fatal(err)
		}
		manager := room.NewManager(store, room.WithOwnership(ownership), room.WithIdleTimeout(20*time.Second))
		candidates = append(candidates, candidate{registry: registry, ownership: ownership, manager: manager, client: client})
	}
	t.Cleanup(func() {
		for _, candidate := range candidates {
			_ = candidate.manager.Shutdown(context.Background())
			_ = candidate.registry.Close(context.Background())
			_ = candidate.client.Close()
		}
	})

	// Close Node A's etcd connection without releasing ownership to simulate a
	// process crash.
	if err := clientA.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-handleA.Context.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("old owner did not self-fence after lease confirmation was lost")
	}

	var active cluster.Owner
	waitFor(t, 10*time.Second, func() bool {
		for _, candidate := range candidates {
			owner, resolveErr := candidate.ownership.Resolve(context.Background(), documentID)
			if resolveErr == nil && owner.State == cluster.OwnerActive && owner.NodeID != registryA.Node().NodeID {
				active = owner
				return true
			}
		}
		return false
	})

	var winner candidate
	for _, candidate := range candidates {
		if candidate.registry.Node().NodeID == active.NodeID && candidate.registry.Node().Incarnation == active.Incarnation {
			winner = candidate
			break
		}
	}
	if winner.manager == nil {
		t.Fatalf("active owner %#v does not match a candidate", active)
	}
	roomAfterFailover, err := winner.manager.Room(documentID)
	if err != nil {
		t.Fatal(err)
	}
	recovered, err := roomAfterFailover.SnapshotContext(context.Background())
	if err != nil || recovered.Content != "AB" || recovered.Version != 2 {
		t.Fatalf("recovered document = %#v, %v", recovered, err)
	}
	handleAfterFailover := waitForOwnershipHandle(t, winner.ownership, documentID)
	if handleAfterFailover.FencingToken <= handleA.FencingToken {
		t.Fatalf("failover token %d did not exceed %d", handleAfterFailover.FencingToken, handleA.FencingToken)
	}
	if err := store.CommitEdit(context.Background(), ownershipEdit(handleA.Authority(), documentID, "old-after-failover", 2, "stale")); !errors.Is(err, storage.ErrFenced) {
		t.Fatalf("old owner write after failover = %v", err)
	}

	writerAfterFailover := &room.Client{UserID: "owner", Send: make(chan protocol.Message, 4)}
	if err := roomAfterFailover.Join(writerAfterFailover); err != nil {
		t.Fatal(err)
	}
	<-writerAfterFailover.Send
	applyFailoverEdit(t, roomAfterFailover, writerAfterFailover, "after-failover", 2, 2, "C")
	stored, err := store.GetDocument(context.Background(), documentID)
	if err != nil || stored.Content != "ABC" || stored.Version != 3 {
		t.Fatalf("stored document after failover = %#v, %v", stored, err)
	}
	operations, err := store.LoadOperations(context.Background(), documentID)
	if err != nil || len(operations) != 3 || operations[2].ID != "after-failover" {
		t.Fatalf("operation log after failover = %#v, %v", operations, err)
	}
}

func waitForOwnershipHandle(t *testing.T, ownership *Ownership, documentID string) *cluster.OwnershipHandle {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		ownership.mu.Lock()
		for handle := range ownership.owners {
			if handle.DocumentID == documentID {
				ownership.mu.Unlock()
				return handle
			}
		}
		ownership.mu.Unlock()
		time.Sleep(time.Millisecond)
	}
	t.Fatal("ownership handle was not created")
	return nil
}

func applyFailoverEdit(t *testing.T, target *room.Room, client *room.Client, requestID string, baseVersion uint64, position int, text string) {
	t.Helper()
	if err := target.SubmitEdit(client, requestID, collab.Operation{BaseVersion: baseVersion, Kind: collab.OperationInsert, Position: position, Text: text}); err != nil {
		t.Fatal(err)
	}
	if message := <-client.Send; message.Type != protocol.TypeEditApplied || message.RequestID != requestID {
		t.Fatalf("edit response = %#v", message)
	}
}

func TestOwnershipTakeoverFencesOldNodeInMySQL(t *testing.T) {
	acquireIntegrationLock(t)
	etcdClient := openIntegrationClient(t)
	defer etcdClient.Close()
	store := openOwnershipMySQL(t)
	documentID := fmt.Sprintf("phase83/takeover/%d", time.Now().UnixNano())
	if _, err := store.CreateDocument(context.Background(), documentID, "", "owner"); err != nil {
		t.Fatal(err)
	}

	registryA := startIntegrationRegistry(t, etcdClient, integrationNode(fmt.Sprintf("phase83-a-%d", time.Now().UnixNano()), "process-a"), 3*time.Second)
	if err := registryA.MarkReady(context.Background()); err != nil {
		t.Fatal(err)
	}
	ownershipA, _ := NewOwnership(registryA)
	handleA, err := ownershipA.Acquire(context.Background(), documentID)
	if err != nil {
		t.Fatal(err)
	}
	activateOwnership(t, store, ownershipA, handleA)

	registryB := startIntegrationRegistry(t, etcdClient, integrationNode(fmt.Sprintf("phase83-b-%d", time.Now().UnixNano()), "process-b"), 3*time.Second)
	defer closeRegistry(t, registryB)
	if err := registryB.MarkReady(context.Background()); err != nil {
		t.Fatal(err)
	}
	closeRegistry(t, registryA)
	select {
	case <-handleA.Context.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("old ownership context was not canceled")
	}
	waitFor(t, 5*time.Second, func() bool {
		nodes, _ := registryB.Membership().Snapshot()
		return len(cluster.RankNodes(documentID, nodes)) == 1
	})

	ownershipB, _ := NewOwnership(registryB)
	handleB, err := ownershipB.Acquire(context.Background(), documentID)
	if err != nil {
		t.Fatal(err)
	}
	defer ownershipB.Release(context.Background(), handleB)
	if handleB.FencingToken <= handleA.FencingToken {
		t.Fatalf("takeover token %d did not exceed old token %d", handleB.FencingToken, handleA.FencingToken)
	}
	activateOwnership(t, store, ownershipB, handleB)

	oldEdit := ownershipEdit(handleA.Authority(), documentID, "old-edit", 0, "old")
	if err := store.CommitEdit(context.Background(), oldEdit); !errors.Is(err, storage.ErrFenced) {
		t.Fatalf("old edit error = %v", err)
	}
	oldPermission := storage.PermissionCommit{Authority: handleA.Authority(), DocumentID: documentID, ActorID: "owner", UserID: "reader", Role: permission.RoleRead}
	if err := store.SetPermission(context.Background(), oldPermission); !errors.Is(err, storage.ErrFenced) {
		t.Fatalf("old permission error = %v", err)
	}
	oldSnapshot := storage.SnapshotCommit{Authority: handleA.Authority(), Snapshot: storage.Snapshot{DocumentID: documentID, Version: 0, Content: ""}}
	if err := store.SaveSnapshot(context.Background(), oldSnapshot); !errors.Is(err, storage.ErrFenced) {
		t.Fatalf("old snapshot error = %v", err)
	}
	if err := store.CommitEdit(context.Background(), ownershipEdit(handleB.Authority(), documentID, "new-edit", 0, "new")); err != nil {
		t.Fatalf("new owner edit: %v", err)
	}
}

func TestConcurrentOwnershipAcquireHasSingleWinner(t *testing.T) {
	acquireIntegrationLock(t)
	client := openIntegrationClient(t)
	defer client.Close()
	registry := startIntegrationRegistry(t, client, integrationNode(fmt.Sprintf("phase83-race-%d", time.Now().UnixNano()), "process"), 3*time.Second)
	defer closeRegistry(t, registry)
	if err := registry.MarkReady(context.Background()); err != nil {
		t.Fatal(err)
	}
	ownership, _ := NewOwnership(registry)
	documentID := fmt.Sprintf("race/doc/%d", time.Now().UnixNano())
	results := make(chan error, 2)
	handles := make(chan *cluster.OwnershipHandle, 2)
	for index := 0; index < 2; index++ {
		go func() {
			handle, err := ownership.Acquire(context.Background(), documentID)
			if handle != nil {
				handles <- handle
			}
			results <- err
		}()
	}
	successes := 0
	for index := 0; index < 2; index++ {
		err := <-results
		if err == nil {
			successes++
		} else if !errors.Is(err, cluster.ErrOwnerExists) {
			t.Fatalf("unexpected acquire error: %v", err)
		}
	}
	if successes != 1 {
		t.Fatalf("successful acquisitions = %d, want 1", successes)
	}
	handle := <-handles
	if err := ownership.Release(context.Background(), handle); err != nil {
		t.Fatal(err)
	}
}

func TestFailedRecoveryNeverBecomesActive(t *testing.T) {
	acquireIntegrationLock(t)
	client := openIntegrationClient(t)
	defer client.Close()
	registry := startIntegrationRegistry(t, client, integrationNode(fmt.Sprintf("phase83-recovery-%d", time.Now().UnixNano()), "process"), 3*time.Second)
	defer closeRegistry(t, registry)
	if err := registry.MarkReady(context.Background()); err != nil {
		t.Fatal(err)
	}
	ownership, _ := NewOwnership(registry)
	handle, err := ownership.Acquire(context.Background(), fmt.Sprintf("missing/%d", time.Now().UnixNano()))
	if err != nil {
		t.Fatal(err)
	}
	if err := ownership.BeginRecovery(context.Background(), handle); err != nil {
		t.Fatal(err)
	}
	if err := ownership.Release(context.Background(), handle); err != nil {
		t.Fatal(err)
	}
	response, err := client.client.Get(context.Background(), ownerKey(handle.DocumentID))
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Kvs) != 0 {
		t.Fatal("failed recovery left an ownership key")
	}
}

func openOwnershipMySQL(t *testing.T) *mysqlstorage.Store {
	t.Helper()
	dsn := os.Getenv("MYSQL_TEST_DSN")
	if dsn == "" {
		t.Skip("MYSQL_TEST_DSN is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	store, err := mysqlstorage.Open(ctx, mysqlstorage.Config{
		DSN: dsn, MaxOpenConns: 8, MaxIdleConns: 4,
		ConnMaxLifetime: time.Minute, PingTimeout: 5 * time.Second, QueryTimeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := migration.Run(ctx, store.DB(), "../../../migrations"); err != nil {
		t.Fatal(err)
	}
	return store
}

func activateOwnership(t *testing.T, store *mysqlstorage.Store, ownership *Ownership, handle *cluster.OwnershipHandle) {
	t.Helper()
	if err := store.ActivateOwnership(context.Background(), handle.DocumentID, handle.Authority()); err != nil {
		t.Fatal(err)
	}
	if err := ownership.BeginRecovery(context.Background(), handle); err != nil {
		t.Fatal(err)
	}
	if _, err := store.LoadRecoveryState(context.Background(), handle.DocumentID, handle.Authority()); err != nil {
		t.Fatal(err)
	}
	if err := ownership.Activate(context.Background(), handle); err != nil {
		t.Fatal(err)
	}
}

func ownershipEdit(authority storage.WriteAuthority, documentID, requestID string, version uint64, content string) storage.EditCommit {
	operation := collab.Operation{ID: requestID, UserID: "owner", BaseVersion: version, Kind: collab.OperationInsert, Position: 0, Text: content}
	return storage.EditCommit{
		Authority: authority, ExpectedVersion: version,
		Document:  collab.Document{ID: documentID, Content: content, Version: version + 1},
		Operation: collab.AppliedOperation{Operation: operation, Version: version + 1},
	}
}
