package etcd

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/cluster"
	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/collab"
	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/protocol"
	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/room"
	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/storage"
)

type cuttableTCPProxy struct {
	listener net.Listener
	target   string
	mu       sync.Mutex
	clients  map[net.Conn]struct{}
	done     chan struct{}
}

func startCuttableTCPProxy(t *testing.T, target string) *cuttableTCPProxy {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	proxy := &cuttableTCPProxy{listener: listener, target: target, clients: make(map[net.Conn]struct{}), done: make(chan struct{})}
	go proxy.run()
	return proxy
}

func (proxy *cuttableTCPProxy) endpoint() string { return "https://" + proxy.listener.Addr().String() }

func (proxy *cuttableTCPProxy) run() {
	defer close(proxy.done)
	for {
		client, err := proxy.listener.Accept()
		if err != nil {
			return
		}
		upstream, err := net.DialTimeout("tcp", proxy.target, time.Second)
		if err != nil {
			_ = client.Close()
			continue
		}
		proxy.mu.Lock()
		proxy.clients[client] = struct{}{}
		proxy.clients[upstream] = struct{}{}
		proxy.mu.Unlock()
		go proxy.copy(client, upstream)
	}
}

func (proxy *cuttableTCPProxy) copy(client, upstream net.Conn) {
	var wait sync.WaitGroup
	wait.Add(2)
	go func() { defer wait.Done(); _, _ = io.Copy(upstream, client) }()
	go func() { defer wait.Done(); _, _ = io.Copy(client, upstream) }()
	wait.Wait()
	_ = client.Close()
	_ = upstream.Close()
	proxy.mu.Lock()
	delete(proxy.clients, client)
	delete(proxy.clients, upstream)
	proxy.mu.Unlock()
}

func (proxy *cuttableTCPProxy) stop() {
	_ = proxy.listener.Close()
	proxy.mu.Lock()
	connections := make([]net.Conn, 0, len(proxy.clients))
	for connection := range proxy.clients {
		connections = append(connections, connection)
	}
	proxy.mu.Unlock()
	for _, connection := range connections {
		_ = connection.Close()
	}
	<-proxy.done
}

func TestEtcdTransportPartitionSelfFencesAndRestartRevalidatesOwnership(t *testing.T) {
	acquireIntegrationLock(t)
	endpoint := os.Getenv("ETCD_TEST_ENDPOINTS")
	if endpoint == "" {
		t.Skip("ETCD_TEST_ENDPOINTS is not configured")
	}
	target := strings.TrimPrefix(strings.Split(endpoint, ",")[0], "https://")
	store := openOwnershipMySQL(t)
	documentID := "phase85/partition/" + time.Now().Format("150405.000000000")
	if _, err := store.CreateDocument(context.Background(), documentID, "safe", "owner"); err != nil {
		t.Fatal(err)
	}

	proxy := startCuttableTCPProxy(t, target)
	clientA := openProxyIntegrationClient(t, proxy.endpoint())
	nodeA := integrationNode("phase85-partition-node", "before-partition")
	registryA := startIntegrationRegistry(t, clientA, nodeA, 3*time.Second)
	if err := registryA.MarkReady(context.Background()); err != nil {
		t.Fatal(err)
	}
	ownershipA, _ := NewOwnership(registryA)
	managerA := room.NewManager(store, room.WithOwnership(ownershipA), room.WithIdleTimeout(time.Minute))
	targetA, err := managerA.Room(documentID)
	if err != nil {
		t.Fatal(err)
	}
	handleA := waitForOwnershipHandle(t, ownershipA, documentID)

	proxy.stop()
	select {
	case <-handleA.Context.Done():
	case <-time.After(4 * time.Second):
		t.Fatal("owner did not self-fence after etcd transport partition")
	}
	select {
	case <-targetA.Done():
	case <-time.After(time.Second):
		t.Fatal("room remained active after etcd transport partition")
	}
	probe := &room.Client{UserID: "owner", Send: make(chan protocol.Message, 1)}
	if err := targetA.SubmitEdit(probe, "partition-write", collab.Operation{Kind: collab.OperationInsert, Position: 0, Text: "X"}); !errors.Is(err, room.ErrRoomClosed) {
		t.Fatalf("write during uncertain ownership = %v", err)
	}
	if document, err := store.GetDocument(context.Background(), documentID); err != nil || document.Content != "safe" || document.Version != 0 {
		t.Fatalf("MySQL changed during partition: %#v, %v", document, err)
	}

	direct := openIntegrationClient(t)
	defer direct.Close()
	waitFor(t, 6*time.Second, func() bool {
		response, getErr := direct.client.Get(context.Background(), ownerKey(documentID))
		return getErr == nil && len(response.Kvs) == 0
	})
	proxyAfterRecovery := startCuttableTCPProxy(t, target)
	defer proxyAfterRecovery.stop()
	clientAfterRecovery := openProxyIntegrationClient(t, proxyAfterRecovery.endpoint())
	defer clientAfterRecovery.Close()
	nodeAfterRecovery := integrationNode(nodeA.NodeID, "after-partition")
	registryAfterRecovery := startIntegrationRegistry(t, clientAfterRecovery, nodeAfterRecovery, 3*time.Second)
	defer closeRegistry(t, registryAfterRecovery)
	if err := registryAfterRecovery.MarkReady(context.Background()); err != nil {
		t.Fatal(err)
	}
	ownershipAfterRecovery, _ := NewOwnership(registryAfterRecovery)
	managerAfterRecovery := room.NewManager(store, room.WithOwnership(ownershipAfterRecovery), room.WithIdleTimeout(time.Minute))
	defer managerAfterRecovery.Shutdown(context.Background())
	if _, err := managerAfterRecovery.Room(documentID); err != nil {
		t.Fatal(err)
	}
	handleAfterRecovery := waitForOwnershipHandle(t, ownershipAfterRecovery, documentID)
	if handleAfterRecovery.FencingToken <= handleA.FencingToken {
		t.Fatalf("post-partition token %d did not exceed %d", handleAfterRecovery.FencingToken, handleA.FencingToken)
	}
	if err := store.CommitEdit(context.Background(), ownershipEdit(handleA.Authority(), documentID, "stale-partition-write", 0, "stale")); !errors.Is(err, storage.ErrFenced) {
		t.Fatalf("old partitioned owner write = %v", err)
	}
	_ = managerA.Shutdown(context.Background())
	_ = clientA.Close()
}

func openProxyIntegrationClient(t *testing.T, endpoint string) *Client {
	t.Helper()
	client, err := Open(context.Background(), Config{
		Endpoints: []string{endpoint}, CAFile: os.Getenv("ETCD_TEST_CA_FILE"),
		CertificateFile: os.Getenv("ETCD_TEST_CERT_FILE"), KeyFile: os.Getenv("ETCD_TEST_KEY_FILE"),
		DialTimeout: 3 * time.Second, RequestTimeout: 2 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	return client
}

var _ cluster.OwnershipCoordinator = (*Ownership)(nil)
