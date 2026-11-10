package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"log"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/api"
	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/authentication"
	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/cluster"
	clusteretcd "github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/cluster/etcd"
	clusterrpc "github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/cluster/rpc"
	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/config"
	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/execution"
	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/migration"
	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/observability"
	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/storage"
	mysqlstorage "github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/storage/mysql"
	quictransport "github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/transport/quic"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

const shutdownTimeout = 10 * time.Second

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	runtimeConfig, err := config.Load()
	if err != nil {
		return err
	}
	logger := observability.NewLogger(observability.LogConfig{
		Writer: os.Stdout, Level: slog.LevelInfo, Service: "collab-editor",
		NodeID: runtimeConfig.Cluster.Node.NodeID, Incarnation: runtimeConfig.Cluster.Node.Incarnation,
	})
	slog.SetDefault(logger)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	shutdownTracing, err := observability.ConfigureTracing(ctx, observability.TraceConfig{
		ServiceName: "collab-editor", Endpoint: runtimeConfig.Observability.OTLPEndpoint,
		Insecure: runtimeConfig.Observability.OTLPInsecure,
	})
	if err != nil {
		return fmt.Errorf("configure tracing: %w", err)
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		_ = shutdownTracing(shutdownCtx)
	}()
	metrics := observability.NewMetrics()
	health := observability.NewHealth()
	registry, ownership, etcdClient, err := openCluster(ctx, runtimeConfig, metrics)
	if err != nil {
		return err
	}
	if etcdClient != nil {
		defer etcdClient.Close()
	}
	if registry != nil {
		defer func() {
			shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
			defer cancel()
			_ = registry.Close(shutdownCtx)
		}()
	}
	store, err := openStore(ctx, runtimeConfig, metrics)
	if err != nil {
		return err
	}
	defer store.Close()
	if err := migrateStore(ctx, store); err != nil {
		return err
	}
	authenticator, authService, sessions, err := openAuthentication(ctx, runtimeConfig, store, metrics)
	if err != nil {
		return err
	}
	if sessions != nil {
		defer sessions.Close()
	}
	if authService != nil {
		defer authService.Close()
	}
	registerReadinessChecks(health, store, sessions, etcdClient, registry)
	directory, forwarder, rpcCredentials, err := openInternalRPC(runtimeConfig, registry)
	if err != nil {
		return err
	}
	executionRunner, err := openExecutionRunner(runtimeConfig)
	if err != nil {
		return err
	}

	address := ":" + runtimeConfig.Port
	application := newApplication(runtimeConfig, store, authenticator, authService, sessions, ownership, directory, forwarder, executionRunner, metrics)
	listener, quicServer, err := listenServers(address, runtimeConfig.REST, application, func() (*quictransport.Server, error) {
		return listenQUIC(runtimeConfig, application, authenticator, authService, ownership, metrics)
	})
	if err != nil {
		return err
	}
	publicHandler := otelhttp.NewHandler(metrics.InstrumentHTTP("public", application), "http.public")
	httpServer := newHTTPServer(address, publicHandler)
	internalHandler := otelhttp.NewHandler(metrics.InstrumentHTTP("internal", application.InternalHandler()), "http.internal")
	internalListener, internalServer, err := listenInternalRPC(runtimeConfig, internalHandler, rpcCredentials)
	if err != nil {
		return errors.Join(err, quicServer.Close(context.Background()), shutdownUnstarted(listener, application))
	}
	adminListener, adminServer, err := listenAdmin(runtimeConfig.Observability.AdminAddress, health.Handler(metrics.Handler()))
	if err != nil {
		return errors.Join(err, closeListener(internalListener), quicServer.Close(context.Background()), shutdownUnstarted(listener, application))
	}
	if registry != nil {
		if err := registry.MarkReady(ctx); err != nil {
			return errors.Join(err, closeListener(adminListener), closeListener(internalListener), shutdownUnstarted(listener, application), quicServer.Close(context.Background()))
		}
		go observeMembership(ctx, registry.Membership(), metrics)
	}
	health.SetReady(true)
	metrics.NodeReady.Set(1)
	go observeRuntime(ctx, application, store, metrics)

	restScheme := "http"
	if runtimeConfig.REST.TLSEnabled() {
		restScheme = "https"
	}
	slog.Info("service ready", "component", "server", "event", "node_ready", "rest_scheme", restScheme, "rest_address", address, "quic_address", quicServer.Addr().String())
	return serveWithRegistry(ctx, serviceGroup{
		publicListener:   listener,
		publicServer:     httpServer,
		quicServer:       quicServer,
		application:      application,
		internalListener: internalListener,
		internalServer:   internalServer,
		adminListener:    adminListener,
		adminServer:      adminServer,
		health:           health,
		metrics:          metrics,
	}, registry)
}

func openInternalRPC(runtimeConfig config.Config, registry *clusteretcd.Registry) (cluster.NodeDirectory, cluster.PermissionForwarder, *clusterrpc.Credentials, error) {
	if !runtimeConfig.Cluster.Enabled() {
		return nil, nil, nil, nil
	}
	if registry == nil {
		return nil, nil, nil, errors.New("cluster registry is required for internal RPC")
	}
	credentials, err := clusterrpc.LoadCredentials(clusterrpc.TLSConfig{
		CAFile:          runtimeConfig.Cluster.InternalRPC.CAFile,
		CertificateFile: runtimeConfig.Cluster.InternalRPC.CertificateFile,
		KeyFile:         runtimeConfig.Cluster.InternalRPC.KeyFile,
	})
	if err != nil {
		return nil, nil, nil, err
	}
	if err := credentials.ValidateIdentity(runtimeConfig.Cluster.Node); err != nil {
		return nil, nil, nil, err
	}
	directory := registry.Membership()
	forwarder, err := clusterrpc.NewClient(runtimeConfig.Cluster.Node, directory, credentials, runtimeConfig.Cluster.InternalRPC.Timeout)
	if err != nil {
		return nil, nil, nil, err
	}
	return directory, forwarder, credentials, nil
}

func openCluster(ctx context.Context, runtimeConfig config.Config, observers ...clusteretcd.Observer) (*clusteretcd.Registry, cluster.OwnershipService, *clusteretcd.Client, error) {
	if !runtimeConfig.Cluster.Enabled() {
		return nil, nil, nil, nil
	}
	client, err := clusteretcd.Open(ctx, clusteretcd.Config{
		Endpoints:       runtimeConfig.Cluster.Endpoints,
		Username:        runtimeConfig.Cluster.Username,
		Password:        runtimeConfig.Cluster.Password,
		CAFile:          runtimeConfig.Cluster.CAFile,
		CertificateFile: runtimeConfig.Cluster.CertificateFile,
		KeyFile:         runtimeConfig.Cluster.KeyFile,
		DialTimeout:     runtimeConfig.Cluster.DialTimeout,
		RequestTimeout:  runtimeConfig.Cluster.RequestTimeout,
	})
	if err != nil {
		return nil, nil, nil, err
	}
	registry, err := clusteretcd.NewRegistry(client, runtimeConfig.Cluster.Node, runtimeConfig.Cluster.LeaseTTL)
	if err != nil {
		_ = client.Close()
		return nil, nil, nil, err
	}
	if len(observers) > 0 {
		registry.SetObserver(observers[0])
	}
	if err := registry.Start(ctx); err != nil {
		_ = client.Close()
		return nil, nil, nil, err
	}
	ownership, err := clusteretcd.NewOwnership(registry)
	if err != nil {
		_ = registry.Close(context.Background())
		_ = client.Close()
		return nil, nil, nil, err
	}
	return registry, ownership, client, nil
}

func listenServers(address string, restConfig config.RESTConfig, application *api.Server, startQUIC func() (*quictransport.Server, error)) (net.Listener, *quictransport.Server, error) {
	listener, err := listenREST(address, restConfig)
	if err != nil {
		return nil, nil, errors.Join(err, shutdownUnstarted(nil, application))
	}
	quicServer, err := startQUIC()
	if err != nil {
		return nil, nil, errors.Join(err, shutdownUnstarted(listener, application))
	}
	return listener, quicServer, nil
}

func listenREST(address string, restConfig config.RESTConfig) (net.Listener, error) {
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return nil, err
	}
	if !restConfig.TLSEnabled() {
		return listener, nil
	}
	tlsConfig, err := loadRESTTLSConfig(restConfig)
	if err != nil {
		_ = listener.Close()
		return nil, err
	}
	return tls.NewListener(listener, tlsConfig), nil
}

func loadRESTTLSConfig(restConfig config.RESTConfig) (*tls.Config, error) {
	if !restConfig.TLSEnabled() {
		return nil, errors.New("REST TLS certificate and key are required")
	}
	certificate, err := tls.LoadX509KeyPair(restConfig.TLSCertFile, restConfig.TLSKeyFile)
	if err != nil {
		return nil, fmt.Errorf("load REST TLS certificate: %w", err)
	}
	if len(certificate.Certificate) == 0 {
		return nil, errors.New("REST TLS certificate chain is empty")
	}
	leaf, err := x509.ParseCertificate(certificate.Certificate[0])
	if err != nil {
		return nil, fmt.Errorf("parse REST TLS certificate: %w", err)
	}
	now := time.Now()
	if now.Before(leaf.NotBefore) || now.After(leaf.NotAfter) {
		return nil, errors.New("REST TLS certificate is not currently valid")
	}
	if !supportsExtKeyUsage(leaf, x509.ExtKeyUsageServerAuth) {
		return nil, errors.New("REST TLS certificate is not valid for server authentication")
	}
	certificate.Leaf = leaf
	return &tls.Config{
		MinVersion:   tls.VersionTLS12,
		Certificates: []tls.Certificate{certificate},
	}, nil
}

func supportsExtKeyUsage(certificate *x509.Certificate, required x509.ExtKeyUsage) bool {
	for _, usage := range certificate.ExtKeyUsage {
		if usage == required || usage == x509.ExtKeyUsageAny {
			return true
		}
	}
	return false
}

func shutdownUnstarted(listener net.Listener, application *api.Server) error {
	var listenerErr error
	if listener != nil {
		listenerErr = listener.Close()
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	return errors.Join(listenerErr, application.Shutdown(shutdownCtx))
}

func newApplication(runtimeConfig config.Config, store storage.Store, authenticator authentication.Authenticator, authService *authentication.Service, sessions authentication.SessionStore, ownership cluster.OwnershipService, directory cluster.NodeDirectory, forwarder cluster.PermissionForwarder, executionRunner execution.Runner, metrics *observability.Metrics) *api.Server {
	return api.NewServerWithConfig(api.Config{
		Store:                store,
		Authenticator:        authenticator,
		AuthService:          authService,
		SnapshotInterval:     runtimeConfig.SnapshotInterval,
		MaxHistoryOperations: runtimeConfig.MaxHistoryOperations,
		SnapshotTimeout:      runtimeConfig.SnapshotTimeout,
		Ownership:            ownership,
		OwnerResolver:        ownership,
		LocalNode:            runtimeConfig.Cluster.Node,
		NodeDirectory:        directory,
		PermissionForwarder:  forwarder,
		RoomObserver:         metrics,
		RateLimiter:          sessionsRateLimiter(sessions),
		AllowedOrigins:       runtimeConfig.Security.AllowedOrigins,
		LoginRateLimit:       runtimeConfig.Security.LoginRateLimit,
		RefreshRateLimit:     runtimeConfig.Security.RefreshRateLimit,
		InternalRPCRateLimit: runtimeConfig.Security.InternalRPCRateLimit,
		RateLimitWindow:      runtimeConfig.Security.RateLimitWindow,
		BrowserCookieSecure:  runtimeConfig.Environment == "production",
		BrowserRefreshTTL:    runtimeConfig.RefreshTTL,
		ExecutionRunner:      executionRunner,
		ExecutionLimits:      runtimeConfig.Execution.Limits,
		ExecutionGlobalLimit: runtimeConfig.Execution.GlobalLimit,
		ExecutionUserLimit:   runtimeConfig.Execution.UserLimit,
		ExecutionRateLimit:   runtimeConfig.Execution.RateLimit,
	})
}

func openExecutionRunner(runtimeConfig config.Config) (execution.Runner, error) {
	if !runtimeConfig.Execution.Enabled() {
		return nil, nil
	}
	return execution.NewUnixClient(
		runtimeConfig.Execution.SocketPath,
		runtimeConfig.Execution.Token,
		runtimeConfig.Execution.Limits.Timeout+2*time.Second,
	)
}

func listenInternalRPC(runtimeConfig config.Config, handler http.Handler, credentials *clusterrpc.Credentials) (net.Listener, *http.Server, error) {
	if !runtimeConfig.Cluster.Enabled() {
		return nil, nil, nil
	}
	if credentials == nil {
		return nil, nil, errors.New("internal RPC TLS credentials are required")
	}
	listener, err := tls.Listen("tcp", runtimeConfig.Cluster.InternalRPC.Address, credentials.ServerConfig())
	if err != nil {
		return nil, nil, fmt.Errorf("listen internal RPC: %w", err)
	}
	server := newHTTPServer(runtimeConfig.Cluster.InternalRPC.Address, handler)
	return listener, server, nil
}

func listenAdmin(address string, handler http.Handler) (net.Listener, *http.Server, error) {
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return nil, nil, fmt.Errorf("listen management endpoint: %w", err)
	}
	return listener, newHTTPServer(address, handler), nil
}

func closeListener(listener net.Listener) error {
	if listener == nil {
		return nil
	}
	return listener.Close()
}

func registerReadinessChecks(health *observability.Health, store storage.Store, sessions authentication.SessionStore, etcdClient *clusteretcd.Client, registry *clusteretcd.Registry) {
	if mysqlStore, ok := store.(*mysqlstorage.Store); ok {
		health.Register("mysql", mysqlStore.DB().PingContext)
	}
	if redisStore, ok := sessions.(interface{ Ping(context.Context) error }); ok {
		health.Register("redis", redisStore.Ping)
	}
	if etcdClient != nil {
		health.Register("etcd", etcdClient.Ping)
	}
	if registry != nil {
		health.Register("membership", func(context.Context) error {
			if !registry.Ready() {
				return errors.New("local membership is not ready")
			}
			return nil
		})
	}
}

func observeMembership(ctx context.Context, membership *cluster.Membership, metrics *observability.Metrics) {
	updates, cancel := membership.Subscribe(16)
	defer cancel()
	update := func() {
		nodes, _ := membership.Snapshot()
		metrics.MembershipNodes.Set(float64(len(nodes)))
	}
	update()
	for {
		select {
		case <-ctx.Done():
			return
		case <-updates:
			update()
		}
	}
}

func listenQUIC(runtimeConfig config.Config, application *api.Server, authenticator authentication.Authenticator, authService *authentication.Service, ownership cluster.OwnerResolver, metrics *observability.Metrics) (*quictransport.Server, error) {
	quicTLS, err := quictransport.LoadServerTLSConfig(
		runtimeConfig.QUIC.TLSCertFile,
		runtimeConfig.QUIC.TLSKeyFile,
		runtimeConfig.QUIC.CAFile,
	)
	if err != nil {
		return nil, err
	}
	quicConfig := newQUICConfig(runtimeConfig, quicTLS)
	quicConfig.OwnerResolver = ownership
	quicConfig.LocalNode = runtimeConfig.Cluster.Node
	quicConfig.Observer = metrics
	if authService != nil {
		quicConfig.ConnectionRegistry = authService.Registry()
	}
	return quictransport.Listen(quicConfig, application.RoomManager(), authenticator)
}

func observeRuntime(ctx context.Context, application *api.Server, store storage.Store, metrics *observability.Metrics) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	update := func() {
		stats := application.RoomManager().Stats()
		metrics.ActiveRooms.Set(float64(stats.ActiveRooms))
		metrics.ActiveClients.Set(float64(stats.ActiveClients))
		metrics.RoomQueueDepth.Set(float64(stats.EventQueueDepth))
		if mysqlStore, ok := store.(*mysqlstorage.Store); ok {
			metrics.ObserveDBStats(mysqlStore.DB().Stats())
		}
	}
	update()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			update()
		}
	}
}

func migrateStore(ctx context.Context, store storage.Store) error {
	mysqlStore, ok := store.(*mysqlstorage.Store)
	if !ok {
		return nil
	}
	return migration.Run(ctx, mysqlStore.DB(), "migrations")
}

func openAuthentication(ctx context.Context, runtimeConfig config.Config, store storage.Store, observers ...authentication.Observer) (authentication.Authenticator, *authentication.Service, authentication.SessionStore, error) {
	if runtimeConfig.AuthMode != "jwt" {
		return authentication.DevelopmentAuthenticator{}, nil, nil, nil
	}
	mysqlStore, ok := store.(*mysqlstorage.Store)
	if !ok {
		return nil, nil, nil, errors.New("JWT authentication requires MySQL storage")
	}
	tokens, err := authentication.NewTokenManager(runtimeConfig.JWT)
	if err != nil {
		return nil, nil, nil, err
	}
	sessions, err := authentication.NewRedisSessions(ctx, runtimeConfig.Redis, observers...)
	if err != nil {
		return nil, nil, nil, err
	}
	serviceConfig := authentication.ServiceConfig{
		RefreshTTL:          runtimeConfig.RefreshTTL,
		RefreshReplayTTL:    runtimeConfig.RefreshReplayTTL,
		ReplayEncryptionKey: runtimeConfig.RefreshReplayKey,
		NodeID:              authenticationNodeID(runtimeConfig),
		OperationLeaseTTL:   2 * runtimeConfig.QUIC.OperationTimeout,
		LogoutWaitTimeout:   runtimeConfig.QUIC.OperationTimeout + 5*time.Second,
		PresenceTTL:         30 * time.Second,
		ReconcileInterval:   5 * time.Second,
	}
	if len(observers) > 0 {
		serviceConfig.Observer = observers[0]
	}
	service := authentication.NewService(authentication.NewMySQLUsers(mysqlStore.DB()), sessions, tokens, serviceConfig)
	return service, service, sessions, nil
}

func authenticationNodeID(runtimeConfig config.Config) string {
	if runtimeConfig.Cluster.Enabled() {
		return runtimeConfig.Cluster.Node.NodeID
	}
	return storage.LocalAuthority.NodeID
}

func newQUICConfig(runtimeConfig config.Config, tlsConfig *tls.Config) quictransport.Config {
	return quictransport.Config{
		Address:          runtimeConfig.QUIC.Address,
		TLSConfig:        tlsConfig,
		HandshakeTimeout: runtimeConfig.QUIC.HandshakeTimeout,
		IdleTimeout:      runtimeConfig.QUIC.IdleTimeout,
		KeepAlivePeriod:  runtimeConfig.QUIC.KeepAlivePeriod,
		AuthTimeout:      runtimeConfig.QUIC.AuthTimeout,
		ReadTimeout:      runtimeConfig.QUIC.ReadTimeout,
		WriteTimeout:     runtimeConfig.QUIC.WriteTimeout,
		OperationTimeout: runtimeConfig.QUIC.OperationTimeout,
		MaxMessageSize:   uint32(runtimeConfig.QUIC.MaxMessageSize),
		SendQueueSize:    runtimeConfig.QUIC.SendQueueSize,
		MaxConnections:   runtimeConfig.QUIC.MaxConnections,
	}
}

func sessionsRateLimiter(sessions authentication.SessionStore) api.RateLimiter {
	limiter, _ := sessions.(api.RateLimiter)
	return limiter
}

func openStore(ctx context.Context, runtimeConfig config.Config, observers ...mysqlstorage.Observer) (storage.Store, error) {
	switch runtimeConfig.StorageDriver {
	case config.StorageMemory:
		return storage.NewMemory(), nil
	case config.StorageMySQL:
		mysqlConfig := runtimeConfig.MySQL
		if len(observers) > 0 {
			mysqlConfig.Observer = observers[0]
		}
		store, err := mysqlstorage.Open(ctx, mysqlConfig)
		if err != nil {
			return nil, err
		}
		return store, nil
	default:
		return nil, fmt.Errorf("unsupported storage driver %q", runtimeConfig.StorageDriver)
	}
}

func newHTTPServer(address string, handler http.Handler) *http.Server {
	return &http.Server{
		Addr:              address,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}
}

type quicLifecycle interface {
	Serve(context.Context) error
	Close(context.Context) error
}

// serviceGroup owns every listener and server started by one process. Keeping
// them together makes the shutdown order explicit without a long positional
// argument list.
type serviceGroup struct {
	publicListener   net.Listener
	publicServer     *http.Server
	quicServer       quicLifecycle
	application      *api.Server
	internalListener net.Listener
	internalServer   *http.Server
	adminListener    net.Listener
	adminServer      *http.Server
	health           *observability.Health
	metrics          *observability.Metrics
}

func serve(ctx context.Context, listener net.Listener, server *http.Server, quicServer quicLifecycle, application *api.Server) error {
	return serveServices(ctx, serviceGroup{
		publicListener: listener,
		publicServer:   server,
		quicServer:     quicServer,
		application:    application,
	})
}

func serveServices(ctx context.Context, services serviceGroup) error {
	serveErrors := make(chan error, 4)
	go func() {
		serveErrors <- services.publicServer.Serve(services.publicListener)
	}()
	go func() {
		serveErrors <- services.quicServer.Serve(ctx)
	}()
	if services.internalServer != nil && services.internalListener != nil {
		go func() {
			serveErrors <- services.internalServer.Serve(services.internalListener)
		}()
	}
	if services.adminServer != nil && services.adminListener != nil {
		go func() {
			serveErrors <- services.adminServer.Serve(services.adminListener)
		}()
	}

	var serveErr error
	select {
	case serveErr = <-serveErrors:
		if errors.Is(serveErr, http.ErrServerClosed) {
			serveErr = nil
		}
	case <-ctx.Done():
	}

	if services.health != nil {
		services.health.Stop()
	}
	if services.metrics != nil {
		services.metrics.NodeReady.Set(0)
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	quicErr := services.quicServer.Close(shutdownCtx)
	var internalErr error
	if services.internalServer != nil {
		internalErr = services.internalServer.Shutdown(shutdownCtx)
	}
	var adminErr error
	if services.adminServer != nil {
		adminErr = services.adminServer.Shutdown(shutdownCtx)
	}
	httpErr := services.publicServer.Shutdown(shutdownCtx)
	roomErr := services.application.Shutdown(shutdownCtx)
	if errors.Is(httpErr, http.ErrServerClosed) {
		httpErr = nil
	}
	if errors.Is(internalErr, http.ErrServerClosed) {
		internalErr = nil
	}
	if errors.Is(adminErr, http.ErrServerClosed) {
		adminErr = nil
	}
	return errors.Join(serveErr, quicErr, internalErr, adminErr, roomErr, httpErr)
}

func serveWithRegistry(ctx context.Context, services serviceGroup, registry *clusteretcd.Registry) error {
	if registry == nil {
		return serveServices(ctx, services)
	}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	leaseErrors := make(chan error, 1)
	go func() {
		select {
		case err := <-registry.LeaseLost():
			leaseErrors <- err
			cancel()
		case <-runCtx.Done():
		}
	}()
	serveErr := serveServices(runCtx, services)
	select {
	case leaseErr := <-leaseErrors:
		return errors.Join(leaseErr, serveErr)
	default:
		return serveErr
	}
}
