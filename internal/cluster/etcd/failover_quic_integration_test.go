package etcd

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/authentication"
	collabclient "github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/client"
	clientstorage "github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/client/storage"
	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/cluster"
	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/collab"
	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/protocol"
	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/room"
	quictransport "github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/transport/quic"

	"github.com/redis/go-redis/v9"
	"golang.org/x/crypto/bcrypt"
)

type failoverDropAckConnection struct {
	collabclient.Connection
	dropped chan struct{}
	once    sync.Once
}

func (connection *failoverDropAckConnection) Receive() (protocol.Message, error) {
	message, err := connection.Connection.Receive()
	if err == nil && message.Type == protocol.TypeEditApplied {
		connection.once.Do(func() { close(connection.dropped) })
		_ = connection.Connection.Close()
		return protocol.Message{}, errors.New("simulated owner crash after commit before acknowledgement")
	}
	return message, err
}

func TestQUICClientAutomaticallyResumesAfterOwnershipFailover(t *testing.T) {
	acquireIntegrationLock(t)
	if os.Getenv("REDIS_TEST_ADDR") == "" {
		t.Skip("REDIS_TEST_ADDR is not configured")
	}
	store := openOwnershipMySQL(t)
	sessions, err := authentication.NewRedisSessions(context.Background(), &redis.Options{
		Addr: os.Getenv("REDIS_TEST_ADDR"), Password: os.Getenv("REDIS_TEST_PASSWORD"), DB: 13,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sessions.Close() })
	tokens, err := authentication.NewTokenManager(authentication.JWTConfig{
		Secret: []byte("0123456789abcdef0123456789abcdef"), Issuer: "phase85", Audience: "phase85", AccessTTL: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	auth := authentication.NewService(authentication.NewMySQLUsers(store.DB()), sessions, tokens, authentication.ServiceConfig{
		RefreshTTL: time.Hour, ReplayEncryptionKey: []byte("abcdef0123456789abcdef0123456789"), BcryptCost: bcrypt.MinCost,
	})
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	user, err := auth.Register(context.Background(), "phase85-"+suffix, "password1")
	if err != nil {
		t.Fatal(err)
	}
	pair, err := auth.Login(context.Background(), user.Username, "password1")
	if err != nil {
		t.Fatal(err)
	}
	documentID := "phase85/quic/" + suffix
	if _, err := store.CreateDocument(context.Background(), documentID, "", user.ID); err != nil {
		t.Fatal(err)
	}

	serverTLS, clientTLS := failoverTLSConfigs(t)
	endpointA := reserveUDPAddress(t)
	clientA := openIntegrationClient(t)
	nodeA := integrationNode("phase85-quic-a-"+suffix, "process-a")
	nodeA.QUICEndpoint = endpointA
	registryA := startIntegrationRegistry(t, clientA, nodeA, 3*time.Second)
	if err := registryA.MarkReady(context.Background()); err != nil {
		t.Fatal(err)
	}
	ownershipA, _ := NewOwnership(registryA)
	managerA := room.NewManager(store, room.WithOwnership(ownershipA), room.WithIdleTimeout(time.Minute))
	if _, err := managerA.Room(documentID); err != nil {
		t.Fatal(err)
	}
	serverA, doneA := startFailoverQUICServer(t, endpointA, serverTLS, managerA, auth, ownershipA, registryA.Node())

	endpointB := reserveUDPAddress(t)
	clientB := openIntegrationClient(t)
	nodeB := integrationNode("phase85-quic-b-"+suffix, "process-b")
	nodeB.QUICEndpoint = endpointB
	registryB := startIntegrationRegistry(t, clientB, nodeB, 3*time.Second)
	if err := registryB.MarkReady(context.Background()); err != nil {
		t.Fatal(err)
	}
	ownershipB, _ := NewOwnership(registryB)
	managerB := room.NewManager(store, room.WithOwnership(ownershipB), room.WithIdleTimeout(time.Minute))
	serverB, doneB := startFailoverQUICServer(t, endpointB, serverTLS, managerB, auth, ownershipB, registryB.Node())
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = serverB.Close(ctx)
		<-doneB
		_ = managerB.Shutdown(ctx)
		_ = registryB.Close(ctx)
		_ = clientB.Close()
	})

	stateStore, err := clientstorage.Open(context.Background(), t.TempDir(), "phase85-user")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stateStore.Close() })
	dropped := make(chan struct{})
	var wrapA sync.Once
	clientManager, err := collabclient.NewManager(collabclient.Config{
		SeedEndpoint: endpointB,
		DialEndpoint: func(ctx context.Context, endpoint string) (collabclient.Connection, error) {
			connection, dialErr := quictransport.Dial(ctx, endpoint, quictransport.DialConfig{
				TLSConfig: clientTLS, HandshakeTimeout: 2 * time.Second, ReadTimeout: 2 * time.Second, WriteTimeout: 2 * time.Second,
			})
			if dialErr != nil {
				return nil, dialErr
			}
			if endpoint == endpointA {
				wrapped := collabclient.Connection(connection)
				wrapA.Do(func() { wrapped = &failoverDropAckConnection{Connection: connection, dropped: dropped} })
				return wrapped, nil
			}
			return connection, nil
		},
		Tokens:  collabclient.NewTokenManager(pair.AccessToken, pair.RefreshToken, nil, time.Minute),
		Backoff: collabclient.NewBackoff(100*time.Millisecond, 300*time.Millisecond, 0), ConnectTimeout: 2 * time.Second,
		StateStore: stateStore,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = clientManager.Close() })
	if err := clientManager.Open(documentID); err != nil {
		t.Fatal(err)
	}
	waitForClientStatus(t, clientManager, 10*time.Second, func(status collabclient.Status) bool {
		return status.State == collabclient.StateConnected && status.OwnerEndpoint == endpointA
	})
	if err := clientManager.SubmitEdit("survives-owner-crash", collab.Operation{Kind: collab.OperationInsert, Position: 0, Text: "X"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-dropped:
	case <-time.After(5 * time.Second):
		t.Fatal("committed acknowledgement was not intercepted")
	}

	crashCtx, crashCancel := context.WithTimeout(context.Background(), 3*time.Second)
	if err := serverA.Close(crashCtx); err != nil {
		crashCancel()
		t.Fatal(err)
	}
	crashCancel()
	<-doneA
	if err := clientA.Close(); err != nil {
		t.Fatal(err)
	}

	status := waitForClientStatus(t, clientManager, 15*time.Second, func(status collabclient.Status) bool {
		return status.State == collabclient.StateConnected && status.OwnerEndpoint == endpointB && status.ServerVersion == 1 && len(status.Pending) == 0
	})
	if status.ReconnectAttempts != 0 {
		// Manager resets the count after connecting; only the final stable state is
		// relevant here.
		t.Logf("reconnect attempts after recovery = %d", status.ReconnectAttempts)
	}
	content, version, available := clientManager.Document()
	if !available || content != "X" || version != 1 {
		t.Fatalf("client recovery = %q version %d available %t", content, version, available)
	}
	if err := clientManager.SubmitEdit("after-owner-failover", collab.Operation{Kind: collab.OperationInsert, Position: 1, Text: "Y"}); err != nil {
		t.Fatal(err)
	}
	waitForClientStatus(t, clientManager, 5*time.Second, func(status collabclient.Status) bool {
		return status.ServerVersion == 2 && len(status.Pending) == 0
	})
	stored, err := store.GetDocument(context.Background(), documentID)
	if err != nil || stored.Content != "XY" || stored.Version != 2 {
		t.Fatalf("stored document = %#v, %v", stored, err)
	}
	operations, err := store.LoadOperations(context.Background(), documentID)
	if err != nil || len(operations) != 2 || operations[0].ID != "survives-owner-crash" || operations[1].ID != "after-owner-failover" {
		t.Fatalf("operation log = %#v, %v", operations, err)
	}
	recovery, err := stateStore.Recovery(context.Background())
	if err != nil || recovery.Document == nil || recovery.Document.Content != "XY" || len(recovery.Pending) != 0 {
		t.Fatalf("SQLite recovery = %#v, %v", recovery, err)
	}
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	_ = managerA.Shutdown(shutdownCtx)
}

func failoverTLSConfigs(t *testing.T) (*tls.Config, *tls.Config) {
	t.Helper()
	certificate := os.Getenv("ETCD_TEST_CERT_FILE")
	key := os.Getenv("ETCD_TEST_KEY_FILE")
	ca := os.Getenv("ETCD_TEST_CA_FILE")
	if certificate == "" || key == "" || ca == "" {
		t.Skip("etcd TLS files are required for QUIC failover integration")
	}
	serverTLS, err := quictransport.LoadServerTLSConfig(certificate, key, ca)
	if err != nil {
		t.Fatal(err)
	}
	clientTLS, err := quictransport.LoadClientTLSConfig(ca, "localhost")
	if err != nil {
		t.Fatal(err)
	}
	return serverTLS, clientTLS
}

func reserveUDPAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.LocalAddr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return address
}

func startFailoverQUICServer(t *testing.T, endpoint string, tlsConfig *tls.Config, manager *room.Manager, auth authentication.Authenticator, resolver cluster.OwnerResolver, node cluster.Node) (*quictransport.Server, <-chan error) {
	t.Helper()
	config := quictransport.DefaultConfig()
	config.Address, config.TLSConfig, config.OwnerResolver, config.LocalNode = endpoint, tlsConfig, resolver, node
	config.ReadTimeout, config.WriteTimeout, config.OperationTimeout = 2*time.Second, 2*time.Second, 2*time.Second
	server, err := quictransport.Listen(config, manager, auth)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- server.Serve(context.Background()) }()
	return server, done
}

func waitForClientStatus(t *testing.T, manager *collabclient.Manager, timeout time.Duration, predicate func(collabclient.Status) bool) collabclient.Status {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		status := manager.Status()
		if predicate(status) {
			return status
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("client status timeout: %#v", manager.Status())
	return collabclient.Status{}
}
