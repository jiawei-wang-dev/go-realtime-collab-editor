package api

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/authentication"
	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/cluster"
	clusteretcd "github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/cluster/etcd"
	clusterrpc "github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/cluster/rpc"
	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/migration"
	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/permission"
	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/storage"
	mysqlstorage "github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/storage/mysql"
	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/testutil/etcdlock"
	"github.com/redis/go-redis/v9"
)

func TestClusterPermissionForwardingWithRealDependencies(t *testing.T) {
	dependencies := loadClusterTestDependencies(t)
	acquireClusterTestLock(t, dependencies)
	store := openClusterTestStore(t, dependencies.mysqlDSN)
	tokensA, serviceA, sessionsA := newClusterTestAuthentication(t, store, dependencies.redisOptions)
	_, serviceB, sessionsB := newClusterTestAuthentication(t, store, dependencies.redisOptions)
	t.Cleanup(func() { _ = sessionsA.Close(); _ = sessionsB.Close() })

	credentialsA := loadClusterTestCredentials(t, dependencies.internalCA, dependencies.nodeACert, dependencies.nodeAKey)
	credentialsB := loadClusterTestCredentials(t, dependencies.internalCA, dependencies.nodeBCert, dependencies.nodeBKey)
	internalListener, internalEndpoint := reserveInternalListener(t)

	etcdClientA := openClusterTestEtcd(t, dependencies)
	etcdClientB := openClusterTestEtcd(t, dependencies)
	identityA := credentialsA.Identity()
	identityB := credentialsB.Identity()
	nodeA := clusterTestNode(identityA.NodeID, identityA.Incarnation, "https://127.0.0.1:1")
	nodeB := clusterTestNode(identityB.NodeID, identityB.Incarnation, internalEndpoint)
	registryA := startClusterTestRegistry(t, etcdClientA, nodeA)
	registryB := startClusterTestRegistry(t, etcdClientB, nodeB)
	t.Cleanup(func() {
		closeClusterTestRegistry(registryA)
		closeClusterTestRegistry(registryB)
		_ = etcdClientA.Close()
		_ = etcdClientB.Close()
	})
	waitForClusterTest(t, func() bool {
		_, aSeesB := registryA.Membership().Lookup(nodeB.NodeID, nodeB.Incarnation)
		_, bSeesA := registryB.Membership().Lookup(nodeA.NodeID, nodeA.Incarnation)
		return aSeesB && bSeesA
	})

	ownershipA, _ := clusteretcd.NewOwnership(registryA)
	ownershipB, _ := clusteretcd.NewOwnership(registryB)
	forwarderA, err := clusterrpc.NewClient(nodeA, registryA.Membership(), credentialsA, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	serverA := NewServerWithConfig(Config{
		Store: store, Authenticator: serviceA, AuthService: serviceA, Ownership: ownershipA,
		OwnerResolver: ownershipA, LocalNode: nodeA, NodeDirectory: registryA.Membership(), PermissionForwarder: forwarderA,
	})
	serverB := NewServerWithConfig(Config{
		Store: store, Authenticator: serviceB, AuthService: serviceB, Ownership: ownershipB,
		OwnerResolver: ownershipB, LocalNode: nodeB, NodeDirectory: registryB.Membership(),
	})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = serverA.Shutdown(ctx)
		_ = serverB.Shutdown(ctx)
	})
	internalServer := &http.Server{Handler: serverB.InternalHandler(), ReadHeaderTimeout: time.Second}
	tlsListener := tls.NewListener(internalListener, credentialsB.ServerConfig())
	internalDone := make(chan error, 1)
	internalStopped := false
	go func() { internalDone <- internalServer.Serve(tlsListener) }()
	t.Cleanup(func() {
		if internalStopped {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = internalServer.Shutdown(ctx)
		<-internalDone
	})

	owner := registerClusterTestUser(t, serviceA, uniqueClusterUsername("owner"))
	target := registerClusterTestUser(t, serviceA, uniqueClusterUsername("target"))
	nonOwner := registerClusterTestUser(t, serviceA, uniqueClusterUsername("nonowner"))
	ownerTokens := loginClusterTestUser(t, serviceA, owner.Username)
	nonOwnerTokens := loginClusterTestUser(t, serviceA, nonOwner.Username)
	documentID := documentOwnedBy(t, registryA.Membership(), nodeB)
	if _, err := store.CreateDocument(context.Background(), documentID, "", owner.ID); err != nil {
		t.Fatal(err)
	}

	response := publicPermissionRequest(t, serverA, ownerTokens.AccessToken, documentID, target.ID, permission.RoleWrite)
	if response.Code != http.StatusOK {
		t.Fatalf("forwarded permission status = %d body=%s", response.Code, response.Body.String())
	}
	assertStoredRole(t, store, documentID, target.ID, permission.RoleWrite)
	documentResponse := publicDocumentRequest(t, serverA, ownerTokens.AccessToken, documentID)
	if documentResponse.Code != http.StatusOK {
		t.Fatalf("forwarded document status = %d body=%s", documentResponse.Code, documentResponse.Body.String())
	}
	permissionResponse := publicPermissionQuery(t, serverA, ownerTokens.AccessToken, documentID, target.ID)
	if permissionResponse.Code != http.StatusOK {
		t.Fatalf("forwarded permission query status = %d body=%s", permissionResponse.Code, permissionResponse.Body.String())
	}
	resolved, err := ownershipA.Resolve(context.Background(), documentID)
	if err != nil || resolved.NodeID != nodeB.NodeID || resolved.State != cluster.OwnerActive {
		t.Fatalf("resolved owner = %#v, %v", resolved, err)
	}

	response = publicPermissionRequest(t, serverA, nonOwnerTokens.AccessToken, documentID, target.ID, permission.RoleRead)
	if response.Code != http.StatusForbidden {
		t.Fatalf("non-owner status = %d body=%s", response.Code, response.Body.String())
	}
	assertStoredRole(t, store, documentID, target.ID, permission.RoleWrite)

	response = publicPermissionRequest(t, serverA, "invalid.jwt", documentID, target.ID, permission.RoleRead)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("invalid JWT status = %d", response.Code)
	}

	revokedTokens := loginClusterTestUser(t, serviceA, owner.Username)
	revokedPrincipal, err := tokensA.Parse(revokedTokens.AccessToken)
	if err != nil {
		t.Fatal(err)
	}
	if err := serviceA.Logout(context.Background(), revokedPrincipal); err != nil {
		t.Fatal(err)
	}
	status := directInternalPermissionRequest(t, credentialsA, internalEndpoint, nodeA, revokedTokens.AccessToken, documentID, target.ID, permission.RoleRead)
	if status != http.StatusUnauthorized {
		t.Fatalf("revoked session internal status = %d", status)
	}

	rogueCredentials := loadClusterTestCredentials(t, dependencies.internalCA, dependencies.rogueCert, dependencies.rogueKey)
	rogueIdentity := rogueCredentials.Identity()
	rogueNode := clusterTestNode(rogueIdentity.NodeID, rogueIdentity.Incarnation, "https://127.0.0.1:2")
	status = directInternalPermissionRequest(t, rogueCredentials, internalEndpoint, rogueNode, ownerTokens.AccessToken, documentID, target.ID, permission.RoleRead)
	if status != http.StatusForbidden {
		t.Fatalf("unregistered node status = %d", status)
	}
	assertStoredRole(t, store, documentID, target.ID, permission.RoleWrite)

	untrustedCredentials := loadClusterTestCredentials(t, dependencies.internalCA, dependencies.untrustedCert, dependencies.untrustedKey)
	if _, err := directInternalPermissionRequestResult(untrustedCredentials, internalEndpoint, ownerTokens.AccessToken, documentID, target.ID, permission.RoleRead); err == nil {
		t.Fatal("internal RPC accepted a client certificate signed by an untrusted CA")
	}

	currentAuthority, err := store.GetWriteAuthority(context.Background(), documentID)
	if err != nil {
		t.Fatal(err)
	}
	newAuthority := storage.WriteAuthority{NodeID: "takeover-node", FencingToken: currentAuthority.FencingToken + 1}
	if err := store.ActivateOwnership(context.Background(), documentID, newAuthority); err != nil {
		t.Fatal(err)
	}
	response = publicPermissionRequest(t, serverA, ownerTokens.AccessToken, documentID, target.ID, permission.RoleRead)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("fenced owner status = %d body=%s", response.Code, response.Body.String())
	}
	assertStoredRole(t, store, documentID, target.ID, permission.RoleWrite)
	waitForClusterTest(t, func() bool {
		_, resolveErr := ownershipA.Resolve(context.Background(), documentID)
		return errors.Is(resolveErr, cluster.ErrOwnerNotFound)
	})
	response = publicPermissionRequest(t, serverA, ownerTokens.AccessToken, documentID, target.ID, permission.RoleRead)
	if response.Code != http.StatusOK {
		t.Fatalf("new owner permission status = %d body=%s", response.Code, response.Body.String())
	}
	assertStoredRole(t, store, documentID, target.ID, permission.RoleRead)

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 3*time.Second)
	if err := internalServer.Shutdown(shutdownCtx); err != nil {
		shutdownCancel()
		t.Fatal(err)
	}
	shutdownCancel()
	<-internalDone
	internalStopped = true
	response = publicPermissionRequest(t, serverA, ownerTokens.AccessToken, documentID, target.ID, permission.RoleWrite)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("unavailable owner status = %d body=%s", response.Code, response.Body.String())
	}
	assertStoredRole(t, store, documentID, target.ID, permission.RoleRead)
	documentResponse = publicDocumentRequest(t, serverA, ownerTokens.AccessToken, documentID)
	if documentResponse.Code != http.StatusServiceUnavailable {
		t.Fatalf("unavailable owner document status = %d body=%s", documentResponse.Code, documentResponse.Body.String())
	}
}

type clusterTestDependencies struct {
	etcdEndpoints, etcdCA, etcdCert, etcdKey string
	mysqlDSN                                 string
	redisOptions                             *redis.Options
	internalCA, nodeACert, nodeAKey          string
	nodeBCert, nodeBKey                      string
	rogueCert, rogueKey                      string
	untrustedCert, untrustedKey              string
}

func loadClusterTestDependencies(t *testing.T) clusterTestDependencies {
	t.Helper()
	dependencies := clusterTestDependencies{
		etcdEndpoints: os.Getenv("ETCD_TEST_ENDPOINTS"),
		etcdCA:        os.Getenv("ETCD_TEST_CA_FILE"),
		etcdCert:      os.Getenv("ETCD_TEST_CERT_FILE"),
		etcdKey:       os.Getenv("ETCD_TEST_KEY_FILE"),
		mysqlDSN:      os.Getenv("MYSQL_TEST_DSN"),
		internalCA:    os.Getenv("CLUSTER_TEST_INTERNAL_CA_FILE"),
		nodeACert:     os.Getenv("CLUSTER_TEST_NODE_A_CERT_FILE"),
		nodeAKey:      os.Getenv("CLUSTER_TEST_NODE_A_KEY_FILE"),
		nodeBCert:     os.Getenv("CLUSTER_TEST_NODE_B_CERT_FILE"),
		nodeBKey:      os.Getenv("CLUSTER_TEST_NODE_B_KEY_FILE"),
		rogueCert:     os.Getenv("CLUSTER_TEST_ROGUE_CERT_FILE"),
		rogueKey:      os.Getenv("CLUSTER_TEST_ROGUE_KEY_FILE"),
		untrustedCert: os.Getenv("CLUSTER_TEST_UNTRUSTED_CERT_FILE"),
		untrustedKey:  os.Getenv("CLUSTER_TEST_UNTRUSTED_KEY_FILE"),
	}
	redisAddress := os.Getenv("REDIS_TEST_ADDR")
	if dependencies.etcdEndpoints == "" || dependencies.mysqlDSN == "" || redisAddress == "" || dependencies.internalCA == "" || dependencies.nodeACert == "" || dependencies.nodeBCert == "" || dependencies.rogueCert == "" || dependencies.untrustedCert == "" {
		t.Skip("real etcd, MySQL, Redis and internal mTLS test configuration is required")
	}
	dependencies.redisOptions = &redis.Options{Addr: redisAddress, Password: os.Getenv("REDIS_TEST_PASSWORD")}
	return dependencies
}

func openClusterTestStore(t *testing.T, dsn string) *mysqlstorage.Store {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	store, err := mysqlstorage.Open(ctx, mysqlstorage.Config{DSN: dsn, MaxOpenConns: 10, MaxIdleConns: 5, ConnMaxLifetime: time.Minute, PingTimeout: 5 * time.Second, QueryTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := migration.Run(ctx, store.DB(), "../../migrations"); err != nil {
		t.Fatal(err)
	}
	return store
}

func newClusterTestAuthentication(t *testing.T, store *mysqlstorage.Store, redisOptions *redis.Options) (*authentication.TokenManager, *authentication.Service, *authentication.RedisSessions) {
	t.Helper()
	jwtConfig := authentication.JWTConfig{Secret: []byte("phase84-integration-secret-32-bytes-minimum"), Issuer: "phase84", Audience: "phase84", AccessTTL: time.Hour}
	tokens, err := authentication.NewTokenManager(jwtConfig)
	if err != nil {
		t.Fatal(err)
	}
	sessions, err := authentication.NewRedisSessions(context.Background(), redisOptions)
	if err != nil {
		t.Fatal(err)
	}
	service := authentication.NewService(authentication.NewMySQLUsers(store.DB()), sessions, tokens, authentication.ServiceConfig{RefreshTTL: time.Hour, ReplayEncryptionKey: []byte("0123456789abcdef0123456789abcdef")})
	return tokens, service, sessions
}

func openClusterTestEtcd(t *testing.T, dependencies clusterTestDependencies) *clusteretcd.Client {
	t.Helper()
	client, err := clusteretcd.Open(context.Background(), clusteretcd.Config{
		Endpoints: strings.Split(dependencies.etcdEndpoints, ","), CAFile: dependencies.etcdCA,
		CertificateFile: dependencies.etcdCert, KeyFile: dependencies.etcdKey,
		DialTimeout: 3 * time.Second, RequestTimeout: 3 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func acquireClusterTestLock(t *testing.T, dependencies clusterTestDependencies) {
	t.Helper()
	client, err := etcdlock.Open(etcdlock.Config{
		Endpoints: strings.Split(dependencies.etcdEndpoints, ","), CAFile: dependencies.etcdCA,
		CertificateFile: dependencies.etcdCert, KeyFile: dependencies.etcdKey, DialTimeout: 3 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	release, err := etcdlock.Acquire(ctx, client, etcdlock.SharedMembership, 5)
	if err != nil {
		cancel()
		_ = client.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := release(); err != nil {
			t.Errorf("release shared etcd test lock: %v", err)
		}
		cancel()
		_ = client.Close()
	})
}

func clusterTestNode(nodeID, incarnation, internalEndpoint string) cluster.Node {
	return cluster.Node{NodeID: nodeID, Incarnation: incarnation, QUICEndpoint: "localhost:8443", HTTPEndpoint: internalEndpoint, Status: cluster.NodeStarting, StartedAt: time.Now().UTC()}
}

func startClusterTestRegistry(t *testing.T, client *clusteretcd.Client, node cluster.Node) *clusteretcd.Registry {
	t.Helper()
	registry, err := clusteretcd.NewRegistry(client, node, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := registry.MarkReady(context.Background()); err != nil {
		t.Fatal(err)
	}
	return registry
}

func closeClusterTestRegistry(registry *clusteretcd.Registry) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = registry.Close(ctx)
}

func reserveInternalListener(t *testing.T) (net.Listener, string) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	return listener, fmt.Sprintf("https://localhost:%d", port)
}

func loadClusterTestCredentials(t *testing.T, ca, cert, key string) *clusterrpc.Credentials {
	t.Helper()
	credentials, err := clusterrpc.LoadCredentials(clusterrpc.TLSConfig{CAFile: ca, CertificateFile: cert, KeyFile: key})
	if err != nil {
		t.Fatal(err)
	}
	return credentials
}

func registerClusterTestUser(t *testing.T, service *authentication.Service, username string) authentication.User {
	t.Helper()
	user, err := service.Register(context.Background(), username, "StrongPassword123!")
	if err != nil {
		t.Fatal(err)
	}
	return user
}

func loginClusterTestUser(t *testing.T, service *authentication.Service, username string) authentication.TokenPair {
	t.Helper()
	pair, err := service.Login(context.Background(), username, "StrongPassword123!")
	if err != nil {
		t.Fatal(err)
	}
	return pair
}

func uniqueClusterUsername(prefix string) string {
	return fmt.Sprintf("%s_%d", prefix, time.Now().UnixNano())
}

func documentOwnedBy(t *testing.T, membership *cluster.Membership, wanted cluster.Node) string {
	t.Helper()
	nodes, _ := membership.Snapshot()
	for index := 0; index < 10000; index++ {
		documentID := fmt.Sprintf("phase84-doc-%d-%d", time.Now().UnixNano(), index)
		ranked := cluster.RankNodes(documentID, nodes)
		if len(ranked) > 0 && ranked[0].Identity() == wanted.Identity() {
			return documentID
		}
	}
	t.Fatal("could not find a document assigned to node B")
	return ""
}

func publicPermissionRequest(t *testing.T, server *Server, token, documentID, userID string, role permission.Role) *httptest.ResponseRecorder {
	t.Helper()
	body, _ := json.Marshal(updatePermissionRequest{Role: role})
	request := httptest.NewRequest(http.MethodPut, "/api/documents/"+documentID+"/permissions/"+userID, bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+token)
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	return response
}

func publicDocumentRequest(t *testing.T, server *Server, token, documentID string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, "/api/documents/"+documentID, nil)
	request.Header.Set("Authorization", "Bearer "+token)
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	return response
}

func publicPermissionQuery(t *testing.T, server *Server, token, documentID, userID string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, "/api/documents/"+documentID+"/permissions/"+userID, nil)
	request.Header.Set("Authorization", "Bearer "+token)
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	return response
}

func directInternalPermissionRequest(t *testing.T, credentials *clusterrpc.Credentials, endpoint string, node cluster.Node, token, documentID, userID string, role permission.Role) int {
	t.Helper()
	payload, _ := json.Marshal(clusterrpc.PermissionRequest{DocumentID: documentID, UserID: userID, Role: role})
	request, err := http.NewRequest(http.MethodPut, endpoint+"/internal/v1/permissions", bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set(clusterrpc.NodeIDHeader, node.NodeID)
	request.Header.Set(clusterrpc.IncarnationHeader, node.Incarnation)
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: credentials.ClientConfig("localhost")}, Timeout: 3 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	return response.StatusCode
}

func directInternalPermissionRequestResult(credentials *clusterrpc.Credentials, endpoint, token, documentID, userID string, role permission.Role) (int, error) {
	payload, _ := json.Marshal(clusterrpc.PermissionRequest{DocumentID: documentID, UserID: userID, Role: role})
	request, err := http.NewRequest(http.MethodPut, endpoint+"/internal/v1/permissions", bytes.NewReader(payload))
	if err != nil {
		return 0, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+token)
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: credentials.ClientConfig("localhost")}, Timeout: 3 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		return 0, err
	}
	defer response.Body.Close()
	return response.StatusCode, nil
}

func assertStoredRole(t *testing.T, store storage.Store, documentID, userID string, want permission.Role) {
	t.Helper()
	got, err := store.GetPermission(context.Background(), documentID, userID)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("stored role = %q, want %q", got, want)
	}
}

func waitForClusterTest(t *testing.T, condition func() bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if condition() {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal("cluster condition did not become true")
		case <-ticker.C:
		}
	}
}
