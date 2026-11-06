package quictransport

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/authentication"
	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/collab"
	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/permission"
	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/protocol"
	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/room"
	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/storage"

	"github.com/redis/go-redis/v9"
	"golang.org/x/crypto/bcrypt"
)

func TestDemoAcceptance(t *testing.T) {
	fixture := newQUICFixture(t, 10)
	owner := fixture.addUser(t, "quic-owner")
	writer := fixture.addUser(t, "quic-writer")
	reader := fixture.addUser(t, "quic-reader")
	if _, err := fixture.store.CreateDocument(context.Background(), "doc", "abcdef", owner.user.ID); err != nil {
		t.Fatal(err)
	}
	if err := fixture.store.ActivateOwnership(context.Background(), "doc", storage.LocalAuthority); err != nil {
		t.Fatal(err)
	}
	if err := fixture.store.SetPermission(context.Background(), storage.PermissionCommit{Authority: storage.LocalAuthority, DocumentID: "doc", ActorID: owner.user.ID, UserID: writer.user.ID, Role: permission.RoleWrite}); err != nil {
		t.Fatal(err)
	}
	if err := fixture.store.SetPermission(context.Background(), storage.PermissionCommit{Authority: storage.LocalAuthority, DocumentID: "doc", ActorID: owner.user.ID, UserID: reader.user.ID, Role: permission.RoleRead}); err != nil {
		t.Fatal(err)
	}
	ownerConnection := fixture.connect(t, owner.token)
	defer ownerConnection.Close()
	if state := ownerConnection.State(); state.TLS.Version != tls.VersionTLS13 || state.TLS.NegotiatedProtocol != ALPN {
		t.Fatalf("TLS state = %#v", state.TLS)
	}
	if _, ok := fixture.server.Addr().(*net.UDPAddr); !ok {
		t.Fatalf("QUIC listener is not UDP: %T", fixture.server.Addr())
	}
	join(t, ownerConnection, "doc", permission.RoleOwner)

	writerConnection := fixture.connect(t, writer.token)
	defer writerConnection.Close()
	join(t, writerConnection, "doc", permission.RoleWrite)
	sendAndAssertBroadcast(t, ownerConnection, writerConnection, "doc", "demo-1", 0, 2, "X", 1, 2)
	sendAndAssertBroadcast(t, ownerConnection, writerConnection, "doc", "demo-2-a", 1, 0, "A", 2, 0)
	sendAndAssertBroadcast(t, writerConnection, ownerConnection, "doc", "demo-2-b", 1, 0, "B", 3, 1)

	readerConnection := fixture.connect(t, reader.token)
	defer readerConnection.Close()
	join(t, readerConnection, "doc", permission.RoleRead)
	if err := readerConnection.Send(protocol.Message{
		Type: protocol.TypeEdit, RequestID: "demo-3", DocID: "doc", BaseVersion: 3,
		Operation: &collab.Operation{Kind: collab.OperationInsert, Position: 0, Text: "blocked"},
	}); err != nil {
		t.Fatal(err)
	}
	if message, err := readerConnection.Receive(); err != nil || message.Code != protocol.CodePermissionDenied {
		t.Fatalf("read-only response = %#v err=%v", message, err)
	}

	targetRoom, err := fixture.manager.Room("doc")
	if err != nil {
		t.Fatal(err)
	}
	if err := targetRoom.ChangePermission(owner.user.ID, writer.user.ID, permission.RoleRead); err != nil {
		t.Fatal(err)
	}
	changed, err := writerConnection.Receive()
	if err != nil || changed.Type != protocol.TypePermissionChanged || changed.Role != permission.RoleRead {
		t.Fatalf("permission_changed = %#v err=%v", changed, err)
	}
	if err := writerConnection.Send(protocol.Message{
		Type: protocol.TypeEdit, RequestID: "demo-4", DocID: "doc", BaseVersion: 3,
		Operation: &collab.Operation{Kind: collab.OperationInsert, Position: 0, Text: "blocked"},
	}); err != nil {
		t.Fatal(err)
	}
	if message, err := writerConnection.Receive(); err != nil || message.Code != protocol.CodePermissionDenied {
		t.Fatalf("downgraded response = %#v err=%v", message, err)
	}
	sendAndAssertBroadcast(t, ownerConnection, writerConnection, "doc", "demo-4-owner", 3, 9, "!", 4, 9)
	document, err := fixture.store.GetDocument(context.Background(), "doc")
	if err != nil {
		t.Fatal(err)
	}
	if document.Content != "ABabXcdef!" || document.Version != 4 {
		t.Fatalf("final document = %#v", document)
	}
}

func sendAndAssertBroadcast(t *testing.T, sender, receiver *Connection, docID, requestID string, base uint64, position int, text string, version uint64, expectedPosition int) {
	t.Helper()
	if err := sender.Send(protocol.Message{
		Type: protocol.TypeEdit, RequestID: requestID, DocID: docID, BaseVersion: base,
		Operation: &collab.Operation{Kind: collab.OperationInsert, Position: position, Text: text},
	}); err != nil {
		t.Fatal(err)
	}
	for name, connection := range map[string]*Connection{"sender": sender, "receiver": receiver} {
		message, err := connection.Receive()
		if err != nil {
			t.Fatalf("%s receive: %v", name, err)
		}
		if message.Type != protocol.TypeEditApplied || message.Version != version || message.Operation == nil || message.Operation.Position != expectedPosition {
			t.Fatalf("%s edit_applied = %#v", name, message)
		}
	}
}

func TestRealQUICReadPermissionAndSessionRevocation(t *testing.T) {
	fixture := newQUICFixture(t, 10)
	owner := fixture.addUser(t, "permission-owner")
	reader := fixture.addUser(t, "permission-reader")
	_, _ = fixture.store.CreateDocument(context.Background(), "doc", "abc", owner.user.ID)
	_ = fixture.store.ActivateOwnership(context.Background(), "doc", storage.LocalAuthority)
	_ = fixture.store.SetPermission(context.Background(), storage.PermissionCommit{Authority: storage.LocalAuthority, DocumentID: "doc", ActorID: owner.user.ID, UserID: reader.user.ID, Role: permission.RoleRead})
	connection := fixture.connect(t, reader.token)
	defer connection.Close()
	join(t, connection, "doc", permission.RoleRead)
	if err := connection.Send(protocol.Message{
		Type: protocol.TypeEdit, RequestID: "blocked", DocID: "doc",
		Operation: &collab.Operation{Kind: collab.OperationInsert, Position: 0, Text: "X"},
	}); err != nil {
		t.Fatal(err)
	}
	message, err := connection.Receive()
	if err != nil {
		t.Fatal(err)
	}
	if message.Type != protocol.TypeError || message.Code != protocol.CodePermissionDenied {
		t.Fatalf("read edit response = %#v", message)
	}
	if err := fixture.sessions.Delete(context.Background(), reader.principal.SessionID); err != nil {
		t.Fatal(err)
	}
	if err := connection.Send(protocol.Message{Type: protocol.TypeJoin, DocID: "doc"}); err != nil {
		return
	}
	if _, err := connection.Receive(); err == nil {
		t.Fatal("revoked QUIC session remained usable")
	}
}

func TestRealQUICLogoutClosesActiveConnection(t *testing.T) {
	fixture := newQUICFixture(t, 10)
	owner := fixture.addUser(t, "logout-owner")
	_, _ = fixture.store.CreateDocument(context.Background(), "doc", "", owner.user.ID)
	connection := fixture.connect(t, owner.token)
	defer connection.Close()
	join(t, connection, "doc", permission.RoleOwner)
	if err := fixture.service.Logout(context.Background(), owner.principal); err != nil {
		t.Fatal(err)
	}
	if _, err := connection.Receive(); err == nil {
		t.Fatal("logout did not close active QUIC connection")
	}
}

func TestRealQUICCrossNodeRedisLogoutClosesRemoteConnection(t *testing.T) {
	redisAddress := os.Getenv("REDIS_TEST_ADDR")
	if redisAddress == "" {
		t.Skip("REDIS_TEST_ADDR is required for cross-node logout integration")
	}
	options := func() *redis.Options {
		return &redis.Options{Addr: redisAddress, Password: os.Getenv("REDIS_TEST_PASSWORD"), DB: 12}
	}
	storeA, err := authentication.NewRedisSessions(context.Background(), options())
	if err != nil {
		t.Fatal(err)
	}
	storeB, err := authentication.NewRedisSessions(context.Background(), options())
	if err != nil {
		_ = storeA.Close()
		t.Fatal(err)
	}
	users := authentication.NewMemoryUsers()
	tokens, err := authentication.NewTokenManager(authentication.JWTConfig{
		Secret: []byte("0123456789abcdef0123456789abcdef"), Issuer: "phase86-quic", Audience: "phase86-quic", AccessTTL: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	serviceConfig := func(nodeID string) authentication.ServiceConfig {
		return authentication.ServiceConfig{
			RefreshTTL: time.Hour, BcryptCost: bcrypt.MinCost, NodeID: nodeID,
			OperationLeaseTTL: 5 * time.Second, LogoutWaitTimeout: 3 * time.Second,
			PresenceTTL: time.Second, ReconcileInterval: 100 * time.Millisecond,
		}
	}
	serviceA := authentication.NewService(users, storeA, tokens, serviceConfig("node-a"))
	serviceB := authentication.NewService(users, storeB, tokens, serviceConfig("node-b"))
	t.Cleanup(func() {
		_ = serviceA.Close()
		_ = serviceB.Close()
		_ = storeA.Close()
		_ = storeB.Close()
	})
	user, err := serviceA.Register(context.Background(), fmt.Sprintf("phase86-quic-%d", time.Now().UnixNano()), "password1")
	if err != nil {
		t.Fatal(err)
	}
	pair, err := serviceA.Login(context.Background(), user.Username, "password1")
	if err != nil {
		t.Fatal(err)
	}
	principal, err := serviceA.AuthenticateToken(context.Background(), pair.AccessToken)
	if err != nil {
		t.Fatal(err)
	}

	serverTLS, clientTLS := testTLSConfigs(t)
	documentStore := storage.NewMemory()
	if _, err := documentStore.CreateDocument(context.Background(), "phase86-doc", "", user.ID); err != nil {
		t.Fatal(err)
	}
	manager := room.NewManager(documentStore, room.WithIdleTimeout(time.Minute))
	config := DefaultConfig()
	config.Address, config.TLSConfig, config.ConnectionRegistry = "127.0.0.1:0", serverTLS, serviceB.Registry()
	config.ReadTimeout, config.WriteTimeout, config.OperationTimeout = 2*time.Second, 2*time.Second, 2*time.Second
	server, err := Listen(config, manager, serviceB)
	if err != nil {
		t.Fatal(err)
	}
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.Serve(context.Background()) }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = server.Close(ctx)
		_ = manager.Shutdown(ctx)
		<-serveDone
	})
	connection, err := Dial(context.Background(), server.Addr().String(), DialConfig{
		TLSConfig: clientTLS, HandshakeTimeout: 3 * time.Second, ReadTimeout: 3 * time.Second, WriteTimeout: 2 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	if err := connection.Authenticate(pair.AccessToken); err != nil {
		t.Fatal(err)
	}
	join(t, connection, "phase86-doc", permission.RoleOwner)
	if err := serviceA.Logout(context.Background(), principal); err != nil {
		t.Fatal(err)
	}
	if _, err := connection.Receive(); err == nil {
		t.Fatal("cross-node Redis logout did not close remote QUIC connection")
	}
}

func TestRealQUICServerCloseClosesActiveConnection(t *testing.T) {
	fixture := newQUICFixture(t, 10)
	owner := fixture.addUser(t, "shutdown-owner")
	_, _ = fixture.store.CreateDocument(context.Background(), "doc", "", owner.user.ID)
	connection := fixture.connect(t, owner.token)
	defer connection.Close()
	join(t, connection, "doc", permission.RoleOwner)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := fixture.server.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := connection.Receive(); err == nil {
		t.Fatal("active QUIC connection survived server shutdown")
	}
}

func TestRealQUICResumeSyncAndResync(t *testing.T) {
	t.Run("sync", func(t *testing.T) {
		fixture := newQUICFixture(t, 10)
		owner := fixture.addUser(t, "sync-owner")
		_, _ = fixture.store.CreateDocument(context.Background(), "doc", "", owner.user.ID)
		connection := fixture.connect(t, owner.token)
		join(t, connection, "doc", permission.RoleOwner)
		applyQUICEdit(t, connection, "doc", "one", 0, 0, "A", 1)
		applyQUICEdit(t, connection, "doc", "two", 1, 1, "B", 2)
		_ = connection.Close()

		resumed := fixture.connect(t, owner.token)
		defer resumed.Close()
		if err := resumed.Send(protocol.Message{Type: protocol.TypeResume, DocID: "doc", LastVersion: 1}); err != nil {
			t.Fatal(err)
		}
		message, err := resumed.Receive()
		if err != nil {
			t.Fatal(err)
		}
		if message.Type != protocol.TypeSync || message.FromVersion != 1 || message.ToVersion != 2 || len(message.Operations) != 1 {
			t.Fatalf("sync = %#v", message)
		}
	})

	t.Run("resync", func(t *testing.T) {
		fixture := newQUICFixture(t, 2)
		owner := fixture.addUser(t, "resync-owner")
		_, _ = fixture.store.CreateDocument(context.Background(), "doc", "", owner.user.ID)
		connection := fixture.connect(t, owner.token)
		join(t, connection, "doc", permission.RoleOwner)
		applyQUICEdit(t, connection, "doc", "one", 0, 0, "A", 1)
		applyQUICEdit(t, connection, "doc", "two", 1, 1, "B", 2)
		waitForSnapshot(t, fixture.store, "doc", 2)
		applyQUICEdit(t, connection, "doc", "three", 2, 2, "C", 3)
		_ = connection.Close()
		time.Sleep(20 * time.Millisecond)

		resumed := fixture.connect(t, owner.token)
		defer resumed.Close()
		if err := resumed.Send(protocol.Message{Type: protocol.TypeResume, DocID: "doc", LastVersion: 0}); err != nil {
			t.Fatal(err)
		}
		message, err := resumed.Receive()
		if err != nil {
			t.Fatal(err)
		}
		if message.Type != protocol.TypeResyncRequired || message.Snapshot == nil || message.Snapshot.Version != 2 || message.Snapshot.Content != "AB" || len(message.Operations) != 1 || message.CurrentVersion != 3 {
			t.Fatalf("resync = %#v", message)
		}
	})
}

func TestRealQUICConcurrentSendsDoNotInterleaveFrames(t *testing.T) {
	fixture := newQUICFixture(t, 10)
	owner := fixture.addUser(t, "concurrent-owner")
	_, _ = fixture.store.CreateDocument(context.Background(), "doc", "", owner.user.ID)
	connection := fixture.connect(t, owner.token)
	defer connection.Close()
	join(t, connection, "doc", permission.RoleOwner)
	const count = 20
	var wait sync.WaitGroup
	for index := range count {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			if err := connection.Send(protocol.Message{Type: fmt.Sprintf("unknown-%d", index)}); err != nil {
				t.Errorf("send: %v", err)
			}
		}(index)
	}
	wait.Wait()
	for range count {
		message, err := connection.Receive()
		if err != nil {
			t.Fatal(err)
		}
		if message.Type != protocol.TypeError || message.Code != protocol.CodeBadMessage {
			t.Fatalf("response = %#v", message)
		}
	}
}

func TestRealQUICMalformedJSONReturnsErrorAndConnectionContinues(t *testing.T) {
	fixture := newQUICFixture(t, 10)
	owner := fixture.addUser(t, "malformed-owner")
	_, _ = fixture.store.CreateDocument(context.Background(), "doc", "", owner.user.ID)
	connection := fixture.connect(t, owner.token)
	defer connection.Close()
	join(t, connection, "doc", permission.RoleOwner)
	connection.writeMu.Lock()
	err := writeFrame(connection.stream, []byte(`{"type":`), connection.config.MaxMessageSize)
	connection.writeMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	message, err := connection.Receive()
	if err != nil {
		t.Fatal(err)
	}
	if message.Type != protocol.TypeError || message.Code != protocol.CodeBadMessage {
		t.Fatalf("malformed response = %#v", message)
	}
	if err := connection.Send(protocol.Message{Type: "unknown"}); err != nil {
		t.Fatal(err)
	}
	message, err = connection.Receive()
	if err != nil || message.Code != protocol.CodeBadMessage {
		t.Fatalf("connection did not remain usable: %#v err=%v", message, err)
	}
}

func TestRealQUICTLSVerificationAndMessageLimit(t *testing.T) {
	fixture := newQUICFixture(t, 10)
	owner := fixture.addUser(t, "tls-owner")
	_, _ = fixture.store.CreateDocument(context.Background(), "doc", "", owner.user.ID)
	wrongTLS, _ := testTLSConfigs(t)
	wrongTLS.RootCAs = x509.NewCertPool()
	if connection, err := Dial(context.Background(), fixture.server.Addr().String(), DialConfig{TLSConfig: wrongTLS}); err == nil {
		connection.Close()
		t.Fatal("QUIC client accepted an untrusted server certificate")
	}
	connection, err := Dial(context.Background(), fixture.server.Addr().String(), DialConfig{
		TLSConfig: fixture.clientTLS, HandshakeTimeout: 15 * time.Second, MaxMessageSize: 2 * 1024 * 1024,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	if err := connection.Authenticate(owner.token); err != nil {
		t.Fatal(err)
	}
	if err := connection.Send(protocol.Message{Type: protocol.TypeJoin, DocID: "doc", Message: strings.Repeat("x", 1024*1024+1)}); err == nil {
		if _, err := connection.Receive(); err == nil {
			t.Fatal("server accepted an oversized framed message")
		}
	}
}

func TestRealQUICRejectsInvalidAuthAndInsecureTLSConfig(t *testing.T) {
	fixture := newQUICFixture(t, 10)
	insecure := fixture.clientTLS.Clone()
	insecure.InsecureSkipVerify = true
	if connection, err := Dial(context.Background(), fixture.server.Addr().String(), DialConfig{TLSConfig: insecure}); err == nil {
		connection.Close()
		t.Fatal("InsecureSkipVerify client config was accepted")
	}
	connection, err := Dial(context.Background(), fixture.server.Addr().String(), DialConfig{TLSConfig: fixture.clientTLS, HandshakeTimeout: 15 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	if err := connection.Authenticate("not-a-jwt"); err == nil {
		t.Fatal("invalid AUTH token was accepted")
	}
}

type quicFixture struct {
	server    *Server
	manager   *room.Manager
	store     *storage.Memory
	users     *authentication.MemoryUsers
	sessions  *authentication.MemorySessions
	service   *authentication.Service
	clientTLS *tls.Config
}

type testUser struct {
	user      authentication.User
	token     string
	principal authentication.Principal
}

func newQUICFixture(t *testing.T, snapshotInterval int) *quicFixture {
	t.Helper()
	serverTLS, clientTLS := testTLSConfigs(t)
	users := authentication.NewMemoryUsers()
	sessions := authentication.NewMemorySessions()
	tokens, err := authentication.NewTokenManager(authentication.JWTConfig{
		Secret: []byte("0123456789abcdef0123456789abcdef"), Issuer: "test", Audience: "test", AccessTTL: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	service := authentication.NewService(users, sessions, tokens, authentication.ServiceConfig{RefreshTTL: time.Hour, BcryptCost: bcrypt.MinCost})
	store := storage.NewMemory()
	manager := room.NewManager(store, room.WithSnapshotPolicy(snapshotInterval, 100, time.Second))
	config := DefaultConfig()
	config.Address = "127.0.0.1:0"
	config.TLSConfig = serverTLS
	config.HandshakeTimeout = 15 * time.Second
	config.ConnectionRegistry = service.Registry()
	config.ReadTimeout = 2 * time.Second
	config.WriteTimeout = 2 * time.Second
	config.OperationTimeout = 2 * time.Second
	server, err := Listen(config, manager, service)
	if err != nil {
		t.Fatal(err)
	}
	serveCtx, cancel := context.WithCancel(context.Background())
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.Serve(serveCtx) }()
	t.Cleanup(func() {
		cancel()
		ctx, closeCancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer closeCancel()
		_ = server.Close(ctx)
		_ = manager.Shutdown(ctx)
		if err := <-serveDone; err != nil {
			t.Errorf("serve QUIC: %v", err)
		}
	})
	return &quicFixture{server: server, manager: manager, store: store, users: users, sessions: sessions, service: service, clientTLS: clientTLS}
}

func (f *quicFixture) addUser(t *testing.T, username string) testUser {
	t.Helper()
	user, err := f.service.Register(context.Background(), username, "password1")
	if err != nil {
		t.Fatal(err)
	}
	pair, err := f.service.Login(context.Background(), username, "password1")
	if err != nil {
		t.Fatal(err)
	}
	principal, err := f.service.AuthenticateToken(context.Background(), pair.AccessToken)
	if err != nil {
		t.Fatal(err)
	}
	return testUser{user: user, token: pair.AccessToken, principal: principal}
}

func (f *quicFixture) connect(t *testing.T, token string) *Connection {
	t.Helper()
	connection, err := Dial(context.Background(), f.server.Addr().String(), DialConfig{
		TLSConfig: f.clientTLS, HandshakeTimeout: 15 * time.Second, ReadTimeout: 2 * time.Second, WriteTimeout: 2 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := connection.Authenticate(token); err != nil {
		connection.Close()
		t.Fatal(err)
	}
	return connection
}

func join(t *testing.T, connection *Connection, docID string, role permission.Role) {
	t.Helper()
	if err := connection.Send(protocol.Message{Type: protocol.TypeJoin, DocID: docID}); err != nil {
		t.Fatal(err)
	}
	message, err := connection.Receive()
	if err != nil {
		t.Fatal(err)
	}
	if message.Type != protocol.TypeJoined || message.DocID != docID || message.Role != role {
		t.Fatalf("joined = %#v", message)
	}
}

func applyQUICEdit(t *testing.T, connection *Connection, docID, requestID string, base uint64, position int, text string, version uint64) {
	t.Helper()
	if err := connection.Send(protocol.Message{
		Type: protocol.TypeEdit, RequestID: requestID, DocID: docID, BaseVersion: base,
		Operation: &collab.Operation{Kind: collab.OperationInsert, Position: position, Text: text},
	}); err != nil {
		t.Fatal(err)
	}
	message, err := connection.Receive()
	if err != nil {
		t.Fatal(err)
	}
	if message.Type != protocol.TypeEditApplied || message.Version != version {
		t.Fatalf("edit response = %#v", message)
	}
}

func waitForSnapshot(t *testing.T, store storage.Store, docID string, version uint64) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		snapshot, err := store.GetLatestSnapshot(context.Background(), docID)
		if err == nil && snapshot.Version >= version {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("snapshot %d was not saved", version)
}

func testTLSConfigs(t *testing.T) (*tls.Config, *tls.Config) {
	t.Helper()
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	caTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "QUIC Test CA"},
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
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
		SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "localhost"},
		DNSNames: []string{"localhost"}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
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
	rootPool := x509.NewCertPool()
	rootPool.AddCert(caCertificate)
	return &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{certificate}, NextProtos: []string{ALPN}}, &tls.Config{
		MinVersion: tls.VersionTLS13, RootCAs: rootPool, ServerName: "localhost", NextProtos: []string{ALPN},
	}
}
