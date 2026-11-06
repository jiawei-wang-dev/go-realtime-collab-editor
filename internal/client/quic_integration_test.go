package client

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/authentication"
	clientstorage "github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/client/storage"
	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/collab"
	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/migration"
	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/permission"
	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/protocol"
	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/room"
	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/storage"
	mysqlstorage "github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/storage/mysql"
	quictransport "github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/transport/quic"

	"github.com/redis/go-redis/v9"
	"golang.org/x/crypto/bcrypt"
)

type dropEditAckConnection struct {
	Connection
	dropped bool
}

func (c *dropEditAckConnection) Receive() (protocol.Message, error) {
	message, err := c.Connection.Receive()
	if err == nil && !c.dropped && message.Type == protocol.TypeEditApplied {
		c.dropped = true
		_ = c.Connection.Close()
		return protocol.Message{}, errors.New("simulated process loss after server commit")
	}
	return message, err
}

func TestFullCrashRecoveryWithMySQLRedisQUICAndSQLite(t *testing.T) {
	dsn := os.Getenv("MYSQL_TEST_DSN")
	redisAddress := os.Getenv("REDIS_TEST_ADDR")
	if dsn == "" || redisAddress == "" {
		t.Skip("MYSQL_TEST_DSN and REDIS_TEST_ADDR are required for full crash recovery integration")
	}
	ctx := context.Background()
	mysqlStore, err := mysqlstorage.Open(ctx, mysqlstorage.Config{DSN: dsn, MaxOpenConns: 10, MaxIdleConns: 5, ConnMaxLifetime: time.Minute, PingTimeout: 5 * time.Second, QueryTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer mysqlStore.Close()
	if err := migration.Run(ctx, mysqlStore.DB(), "../../migrations"); err != nil {
		t.Fatal(err)
	}
	sessions, err := authentication.NewRedisSessions(ctx, &redis.Options{Addr: redisAddress, Password: os.Getenv("REDIS_TEST_PASSWORD"), DB: 14})
	if err != nil {
		t.Fatal(err)
	}
	defer sessions.Close()
	jwtTokens, _ := authentication.NewTokenManager(authentication.JWTConfig{Secret: []byte("0123456789abcdef0123456789abcdef"), Issuer: "full-test", Audience: "full-test", AccessTTL: time.Hour})
	service := authentication.NewService(authentication.NewMySQLUsers(mysqlStore.DB()), sessions, jwtTokens, authentication.ServiceConfig{
		RefreshTTL: time.Hour, ReplayEncryptionKey: []byte("abcdef0123456789abcdef0123456789"), BcryptCost: bcrypt.MinCost,
	})
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	user, err := service.Register(ctx, "crash-"+suffix, "password1")
	if err != nil {
		t.Fatal(err)
	}
	pair, err := service.Login(ctx, user.Username, "password1")
	if err != nil {
		t.Fatal(err)
	}
	docID := "crash-" + suffix
	if _, err := mysqlStore.CreateDocument(ctx, docID, "", user.ID); err != nil {
		t.Fatal(err)
	}
	roomManager := room.NewManager(mysqlStore, room.WithIdleTimeout(time.Minute))
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = roomManager.Shutdown(shutdownCtx)
	}()
	serverTLS, clientTLS := reliabilityTLSConfigs(t)
	serverConfig := quictransport.DefaultConfig()
	serverConfig.Address, serverConfig.TLSConfig, serverConfig.ConnectionRegistry = "127.0.0.1:0", serverTLS, service.Registry()
	server, serverDone := startReliabilityServer(t, serverConfig, roomManager, service)
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = server.Close(closeCtx)
		_ = <-serverDone
	}()

	directory := t.TempDir()
	firstState, err := clientstorage.Open(ctx, directory, "user")
	if err != nil {
		t.Fatal(err)
	}
	var firstDial bool
	firstManager, err := NewManager(Config{
		Dial: func(ctx context.Context) (Connection, error) {
			connection, dialErr := quictransport.Dial(ctx, server.Addr().String(), quictransport.DialConfig{TLSConfig: clientTLS, HandshakeTimeout: 2 * time.Second, ReadTimeout: 2 * time.Second, WriteTimeout: 2 * time.Second})
			if dialErr != nil {
				return nil, dialErr
			}
			if !firstDial {
				firstDial = true
				return &dropEditAckConnection{Connection: connection}, nil
			}
			return connection, nil
		},
		Tokens: NewTokenManager(pair.AccessToken, pair.RefreshToken, nil, time.Minute), StateStore: firstState,
		Backoff: NewBackoff(10*time.Second, 10*time.Second, 0),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := firstManager.Open(docID); err != nil {
		t.Fatal(err)
	}
	waitForStatus(t, firstManager, func(status Status) bool { return status.State == StateConnected })
	if err := firstManager.SubmitEdit("durable-request", collab.Operation{Kind: collab.OperationInsert, Position: 0, Text: "X"}); err != nil {
		t.Fatal(err)
	}
	waitForStatus(t, firstManager, func(status Status) bool { return status.State == StateReconnecting && len(status.Pending) == 1 })
	if err := firstManager.Close(); err != nil {
		t.Fatal(err)
	}
	if err := firstState.Close(); err != nil {
		t.Fatal(err)
	}

	secondState, err := clientstorage.Open(ctx, directory, "user")
	if err != nil {
		t.Fatal(err)
	}
	defer secondState.Close()
	secondManager, err := NewManager(Config{
		Dial: func(ctx context.Context) (Connection, error) {
			return quictransport.Dial(ctx, server.Addr().String(), quictransport.DialConfig{TLSConfig: clientTLS, HandshakeTimeout: 2 * time.Second, ReadTimeout: 2 * time.Second, WriteTimeout: 2 * time.Second})
		},
		Tokens: NewTokenManager(pair.AccessToken, pair.RefreshToken, nil, time.Minute), StateStore: secondState,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer secondManager.Close()
	waitForStatus(t, secondManager, func(status Status) bool {
		return status.State == StateConnected && status.ServerVersion == 1 && len(status.Pending) == 0
	})
	document, err := mysqlStore.GetDocument(ctx, docID)
	if err != nil || document.Content != "X" || document.Version != 1 {
		t.Fatalf("server document = %#v, %v", document, err)
	}
	operations, err := mysqlStore.LoadOperations(ctx, docID)
	if err != nil || len(operations) != 1 || operations[0].ID != "durable-request" {
		t.Fatalf("operations = %#v, %v", operations, err)
	}
	recovery, err := secondState.Recovery(ctx)
	if err != nil || recovery.Document == nil || recovery.Document.Content != "X" || len(recovery.Pending) != 0 {
		t.Fatalf("SQLite recovery = %#v, %v", recovery, err)
	}
}

func TestRealQUICManagerReconnectsResumesAndReceivesOfflineEdit(t *testing.T) {
	serverTLS, clientTLS := reliabilityTLSConfigs(t)
	users := authentication.NewMemoryUsers()
	sessions := authentication.NewMemorySessions()
	jwtTokens, err := authentication.NewTokenManager(authentication.JWTConfig{
		Secret: []byte("0123456789abcdef0123456789abcdef"), Issuer: "client-test", Audience: "client-test", AccessTTL: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	service := authentication.NewService(users, sessions, jwtTokens, authentication.ServiceConfig{RefreshTTL: time.Hour, BcryptCost: bcrypt.MinCost})
	user, err := service.Register(context.Background(), "reconnecting-owner", "password1")
	if err != nil {
		t.Fatal(err)
	}
	pair, err := service.Login(context.Background(), "reconnecting-owner", "password1")
	if err != nil {
		t.Fatal(err)
	}
	store := storage.NewMemory()
	if _, err := store.CreateDocument(context.Background(), "doc", "", user.ID); err != nil {
		t.Fatal(err)
	}
	roomManager := room.NewManager(store, room.WithIdleTimeout(time.Minute))
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = roomManager.Shutdown(ctx)
	})

	serverConfig := quictransport.DefaultConfig()
	serverConfig.Address = "127.0.0.1:0"
	serverConfig.TLSConfig = serverTLS
	serverConfig.ReadTimeout = 2 * time.Second
	serverConfig.WriteTimeout = 2 * time.Second
	serverConfig.OperationTimeout = 2 * time.Second
	serverConfig.ConnectionRegistry = service.Registry()
	server, serveDone := startReliabilityServer(t, serverConfig, roomManager, service)
	address := server.Addr().String()

	tokens := NewTokenManager(pair.AccessToken, pair.RefreshToken, nil, time.Minute)
	clientManager, err := NewManager(Config{
		Dial: func(ctx context.Context) (Connection, error) {
			return quictransport.Dial(ctx, address, quictransport.DialConfig{
				TLSConfig: clientTLS, HandshakeTimeout: 2 * time.Second, ReadTimeout: 2 * time.Second, WriteTimeout: 2 * time.Second,
			})
		},
		Tokens: tokens, Backoff: NewBackoff(10*time.Millisecond, 50*time.Millisecond, 0.1), ConnectTimeout: 2 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = clientManager.Close() })
	if err := clientManager.Open("doc"); err != nil {
		t.Fatal(err)
	}
	waitForStatus(t, clientManager, func(status Status) bool { return status.State == StateConnected })
	if err := clientManager.SubmitEdit("before-outage", collab.Operation{Kind: collab.OperationInsert, Position: 0, Text: "A"}); err != nil {
		t.Fatal(err)
	}
	waitForStatus(t, clientManager, func(status Status) bool { return status.ServerVersion == 1 && len(status.Pending) == 0 })

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	if err := server.Close(ctx); err != nil {
		cancel()
		t.Fatal(err)
	}
	cancel()
	if err := <-serveDone; err != nil {
		t.Fatal(err)
	}
	waitForStatus(t, clientManager, func(status Status) bool {
		return status.ReconnectAttempts > 0 && status.State != StateConnected
	})

	targetRoom, err := roomManager.Room("doc")
	if err != nil {
		t.Fatal(err)
	}
	offlineEditor := &room.Client{UserID: user.ID, Send: make(chan protocol.Message, 4)}
	if err := targetRoom.Join(offlineEditor); err != nil {
		t.Fatal(err)
	}
	joined := <-offlineEditor.Send
	if joined.Type != protocol.TypeJoined || joined.Role != permission.RoleOwner {
		t.Fatalf("offline editor JOINED = %#v", joined)
	}
	if err := targetRoom.SubmitEdit(offlineEditor, "during-outage", collab.Operation{Kind: collab.OperationInsert, BaseVersion: 1, Position: 1, Text: "B"}); err != nil {
		t.Fatal(err)
	}
	if applied := <-offlineEditor.Send; applied.Type != protocol.TypeEditApplied || applied.Version != 2 {
		t.Fatalf("offline edit = %#v", applied)
	}
	targetRoom.Leave(offlineEditor)

	serverConfig.Address = address
	restarted, restartedDone := startReliabilityServer(t, serverConfig, roomManager, service)
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = restarted.Close(ctx)
		if err := <-restartedDone; err != nil {
			t.Errorf("restarted server: %v", err)
		}
	}()
	waitForStatus(t, clientManager, func(status Status) bool { return status.State == StateConnected && status.ServerVersion == 2 })
	content, version, ok := clientManager.Document()
	if !ok || content != "AB" || version != 2 {
		t.Fatalf("recovered document = %q,%d,%t", content, version, ok)
	}
}

func startReliabilityServer(t *testing.T, config quictransport.Config, manager *room.Manager, service *authentication.Service) (*quictransport.Server, <-chan error) {
	t.Helper()
	server, err := quictransport.Listen(config, manager, service)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- server.Serve(context.Background()) }()
	return server, done
}

func reliabilityTLSConfigs(t *testing.T) (*tls.Config, *tls.Config) {
	t.Helper()
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	caTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "Reliability Test CA"},
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	caCertificate, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatal(err)
	}
	serverKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	serverTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "localhost"}, DNSNames: []string{"localhost"}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	serverDER, err := x509.CreateCertificate(rand.Reader, serverTemplate, caCertificate, &serverKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	serverKeyDER, err := x509.MarshalPKCS8PrivateKey(serverKey)
	if err != nil {
		t.Fatal(err)
	}
	certificate, err := tls.X509KeyPair(
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: serverDER}),
		pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: serverKeyDER}),
	)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(caCertificate)
	return &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{certificate}, NextProtos: []string{quictransport.ALPN}}, &tls.Config{
		MinVersion: tls.VersionTLS13, RootCAs: roots, ServerName: "localhost", NextProtos: []string{quictransport.ALPN},
	}
}

func TestManagerMultipleWakeupsDoNotCreateConcurrentReconnectLoops(t *testing.T) {
	var active atomicCounter
	var maximum atomicCounter
	var calls atomicCounter
	manager, _ := NewManager(Config{
		Dial: func(ctx context.Context) (Connection, error) {
			calls.add(1)
			current := active.add(1)
			maximum.raise(current)
			defer active.add(-1)
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(5 * time.Millisecond):
				return nil, context.DeadlineExceeded
			}
		},
		Tokens: managerToken(t), Backoff: NewBackoff(time.Millisecond, time.Millisecond, 0), ConnectTimeout: 20 * time.Millisecond,
	})
	defer manager.Close()
	_ = manager.Open("doc")
	var wait sync.WaitGroup
	for range 50 {
		wait.Add(1)
		go func() { defer wait.Done(); _ = manager.Reconnect() }()
	}
	wait.Wait()
	time.Sleep(30 * time.Millisecond)
	manager.Disconnect()
	if maximum.load() != 1 {
		t.Fatalf("maximum concurrent dials = %d", maximum.load())
	}
	if calls.load() == 0 {
		t.Fatal("dial was never attempted")
	}
}

type atomicCounter struct {
	mu    sync.Mutex
	value int
}

func (c *atomicCounter) add(delta int) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.value += delta
	return c.value
}
func (c *atomicCounter) load() int { c.mu.Lock(); defer c.mu.Unlock(); return c.value }
func (c *atomicCounter) raise(value int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if value > c.value {
		c.value = value
	}
}
