package config

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/authentication"
	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/cluster"
	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/execution"
	mysqlstorage "github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/storage/mysql"
	"github.com/redis/go-redis/v9"
)

const (
	StorageMemory = "memory"
	StorageMySQL  = "mysql"
	ClusterSingle = "single"
	ClusterMulti  = "cluster"
)

type Config struct {
	Port                 string
	Environment          string
	REST                 RESTConfig
	Observability        ObservabilityConfig
	StorageDriver        string
	MySQL                mysqlstorage.Config
	AuthMode             string
	JWT                  authentication.JWTConfig
	RefreshTTL           time.Duration
	RefreshReplayTTL     time.Duration
	RefreshReplayKey     []byte
	Redis                *redis.Options
	SnapshotInterval     int
	MaxHistoryOperations int
	SnapshotTimeout      time.Duration
	QUIC                 QUICConfig
	WebTransport         WebTransportConfig
	Cluster              ClusterConfig
	Security             SecurityConfig
	Execution            ExecutionConfig
}

type ExecutionConfig struct {
	SocketPath  string
	Token       string
	Limits      execution.Limits
	GlobalLimit int
	UserLimit   int
	RateLimit   int
}

func (config ExecutionConfig) Enabled() bool { return config.SocketPath != "" }

type SecurityConfig struct {
	AllowedOrigins       []string
	LoginRateLimit       int
	RefreshRateLimit     int
	InternalRPCRateLimit int
	RateLimitWindow      time.Duration
}

type ObservabilityConfig struct {
	AdminAddress string
	OTLPEndpoint string
	OTLPInsecure bool
}

type RESTConfig struct {
	TLSCertFile string
	TLSKeyFile  string
}

func (config RESTConfig) TLSEnabled() bool {
	return config.TLSCertFile != "" && config.TLSKeyFile != ""
}

type ClusterConfig struct {
	Mode            string
	Node            cluster.Node
	Endpoints       []string
	Username        string
	Password        string
	CAFile          string
	CertificateFile string
	KeyFile         string
	LeaseTTL        time.Duration
	DialTimeout     time.Duration
	RequestTimeout  time.Duration
	InternalRPC     InternalRPCConfig
}

type InternalRPCConfig struct {
	Address         string
	CAFile          string
	CertificateFile string
	KeyFile         string
	Timeout         time.Duration
}

func (config ClusterConfig) Enabled() bool {
	return config.Mode == ClusterMulti
}

type QUICConfig struct {
	Address          string
	TLSCertFile      string
	TLSKeyFile       string
	CAFile           string
	HandshakeTimeout time.Duration
	IdleTimeout      time.Duration
	KeepAlivePeriod  time.Duration
	AuthTimeout      time.Duration
	ReadTimeout      time.Duration
	WriteTimeout     time.Duration
	OperationTimeout time.Duration
	MaxMessageSize   int
	SendQueueSize    int
	MaxConnections   int
}

type WebTransportConfig struct {
	Enabled          bool
	Address          string
	TLSCertFile      string
	TLSKeyFile       string
	AuthTimeout      time.Duration
	OperationTimeout time.Duration
	WriteTimeout     time.Duration
	StreamTimeout    time.Duration
	IdleTimeout      time.Duration
	MaxMessageSize   int
	SendQueueSize    int
}

func Load() (Config, error) {
	config := Config{
		Port:          envOrDefault("PORT", "8080"),
		Environment:   envOrDefault("APP_ENV", "development"),
		StorageDriver: envOrDefault("STORAGE_DRIVER", StorageMemory),
		AuthMode:      envOrDefault("AUTH_MODE", "jwt"),
	}
	if err := loadRESTConfig(&config); err != nil {
		return Config{}, err
	}
	if err := loadWebTransportConfig(&config); err != nil {
		return Config{}, err
	}
	if err := loadObservabilityConfig(&config); err != nil {
		return Config{}, err
	}
	if err := loadSecurityConfig(&config); err != nil {
		return Config{}, err
	}
	if err := loadExecutionConfig(&config); err != nil {
		return Config{}, err
	}
	if err := loadClusterConfig(&config); err != nil {
		return Config{}, err
	}
	if config.StorageDriver != StorageMemory && config.StorageDriver != StorageMySQL {
		return Config{}, fmt.Errorf("unsupported STORAGE_DRIVER %q", config.StorageDriver)
	}
	snapshotInterval, err := positiveInt("SNAPSHOT_INTERVAL", 100)
	if err != nil {
		return Config{}, err
	}
	maxHistory, err := positiveInt("MAX_HISTORY_OPERATIONS", 1000)
	if err != nil {
		return Config{}, err
	}
	if maxHistory < snapshotInterval {
		return Config{}, fmt.Errorf("MAX_HISTORY_OPERATIONS must be greater than or equal to SNAPSHOT_INTERVAL")
	}
	snapshotTimeout, err := duration("SNAPSHOT_TIMEOUT", 5*time.Second)
	if err != nil {
		return Config{}, err
	}
	config.SnapshotInterval = snapshotInterval
	config.MaxHistoryOperations = maxHistory
	config.SnapshotTimeout = snapshotTimeout
	if config.Environment == "production" && (config.StorageDriver != StorageMySQL || config.AuthMode != "jwt") {
		return Config{}, fmt.Errorf("production requires STORAGE_DRIVER=mysql and AUTH_MODE=jwt")
	}
	if config.AuthMode != "development" && config.AuthMode != "jwt" {
		return Config{}, fmt.Errorf("unsupported AUTH_MODE %q", config.AuthMode)
	}
	if config.AuthMode != "jwt" {
		return Config{}, fmt.Errorf("QUIC realtime transport requires AUTH_MODE=jwt")
	}
	if config.AuthMode == "jwt" && config.StorageDriver != StorageMySQL {
		return Config{}, fmt.Errorf("AUTH_MODE=jwt requires STORAGE_DRIVER=mysql")
	}
	if err := loadAuthenticationConfig(&config); err != nil {
		return Config{}, err
	}
	if config.StorageDriver == StorageMemory {
		return config, nil
	}
	if err := loadMySQLConfig(&config); err != nil {
		return Config{}, err
	}
	quicConfig, err := loadQUICConfig()
	if err != nil {
		return Config{}, err
	}
	config.QUIC = quicConfig
	return config, nil
}

func loadWebTransportConfig(config *Config) error {
	enabled, err := strconv.ParseBool(envOrDefault("WEBTRANSPORT_ENABLED", "false"))
	if err != nil {
		return fmt.Errorf("WEBTRANSPORT_ENABLED must be a boolean")
	}
	wt := WebTransportConfig{
		Enabled: enabled, Address: envOrDefault("WEBTRANSPORT_ADDR", ":8080"),
		TLSCertFile: os.Getenv("WEBTRANSPORT_TLS_CERT_FILE"), TLSKeyFile: os.Getenv("WEBTRANSPORT_TLS_KEY_FILE"),
	}
	if !enabled {
		config.WebTransport = wt
		return nil
	}
	if wt.TLSCertFile == "" {
		wt.TLSCertFile = config.REST.TLSCertFile
	}
	if wt.TLSKeyFile == "" {
		wt.TLSKeyFile = config.REST.TLSKeyFile
	}
	if wt.TLSCertFile == "" || wt.TLSKeyFile == "" {
		return fmt.Errorf("WEBTRANSPORT_ENABLED requires WebTransport or REST TLS certificate and key files")
	}
	if wt.AuthTimeout, err = duration("WEBTRANSPORT_AUTH_TIMEOUT", 10*time.Second); err != nil {
		return err
	}
	if wt.OperationTimeout, err = duration("WEBTRANSPORT_OPERATION_TIMEOUT", 10*time.Second); err != nil {
		return err
	}
	if wt.WriteTimeout, err = duration("WEBTRANSPORT_WRITE_TIMEOUT", 10*time.Second); err != nil {
		return err
	}
	if wt.StreamTimeout, err = duration("WEBTRANSPORT_STREAM_TIMEOUT", 10*time.Second); err != nil {
		return err
	}
	if wt.IdleTimeout, err = duration("WEBTRANSPORT_IDLE_TIMEOUT", 60*time.Second); err != nil {
		return err
	}
	if wt.MaxMessageSize, err = positiveInt("WEBTRANSPORT_MAX_MESSAGE_SIZE", 1<<20); err != nil {
		return err
	}
	if wt.SendQueueSize, err = positiveInt("WEBTRANSPORT_SEND_QUEUE_SIZE", 32); err != nil {
		return err
	}
	config.WebTransport = wt
	return nil
}

func loadObservabilityConfig(config *Config) error {
	config.Observability.AdminAddress = envOrDefault("ADMIN_ADDR", "127.0.0.1:9090")
	host, port, err := net.SplitHostPort(config.Observability.AdminAddress)
	if err != nil || host == "" || port == "" {
		return fmt.Errorf("ADMIN_ADDR must contain an explicit management host and port")
	}
	if host != "localhost" {
		address := net.ParseIP(host)
		if address == nil || (!address.IsLoopback() && !address.IsPrivate()) {
			return fmt.Errorf("ADMIN_ADDR must bind a loopback or private management address")
		}
	}
	config.Observability.OTLPEndpoint = os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")
	if value := os.Getenv("OTEL_EXPORTER_OTLP_INSECURE"); value != "" {
		config.Observability.OTLPInsecure, err = strconv.ParseBool(value)
		if err != nil {
			return fmt.Errorf("OTEL_EXPORTER_OTLP_INSECURE must be a boolean")
		}
	}
	if config.Environment == "production" && config.Observability.OTLPInsecure {
		return fmt.Errorf("production does not allow an insecure OTLP exporter")
	}
	return nil
}

func loadRESTConfig(config *Config) error {
	if config.Environment != "development" && config.Environment != "test" && config.Environment != "production" {
		return fmt.Errorf("unsupported APP_ENV %q", config.Environment)
	}
	config.REST = RESTConfig{
		TLSCertFile: os.Getenv("REST_TLS_CERT_FILE"),
		TLSKeyFile:  os.Getenv("REST_TLS_KEY_FILE"),
	}
	if (config.REST.TLSCertFile == "") != (config.REST.TLSKeyFile == "") {
		return fmt.Errorf("REST_TLS_CERT_FILE and REST_TLS_KEY_FILE must be configured together")
	}
	if config.Environment == "production" && !config.REST.TLSEnabled() {
		return fmt.Errorf("production requires REST_TLS_CERT_FILE and REST_TLS_KEY_FILE")
	}
	return nil
}

func loadClusterConfig(config *Config) error {
	mode := strings.ToLower(envOrDefault("CLUSTER_MODE", ClusterSingle))
	switch mode {
	case "false", "disabled", ClusterSingle:
		mode = ClusterSingle
	case "true", "enabled", ClusterMulti:
		mode = ClusterMulti
	default:
		return fmt.Errorf("unsupported CLUSTER_MODE %q", mode)
	}
	config.Cluster.Mode = mode
	if mode == ClusterSingle {
		return nil
	}

	incarnation := os.Getenv("NODE_INCARNATION")
	if incarnation == "" {
		if config.Environment == "production" {
			return fmt.Errorf("NODE_INCARNATION is required in production cluster mode")
		}
		var err error
		incarnation, err = cluster.NewIncarnation()
		if err != nil {
			return err
		}
	}
	config.Cluster.Node = cluster.Node{
		NodeID:          os.Getenv("NODE_ID"),
		Incarnation:     incarnation,
		QUICEndpoint:    os.Getenv("NODE_QUIC_ENDPOINT"),
		HTTPEndpoint:    os.Getenv("NODE_HTTP_ENDPOINT"),
		BrowserEndpoint: os.Getenv("NODE_BROWSER_ENDPOINT"),
		Status:          cluster.NodeStarting,
		StartedAt:       time.Now().UTC(),
	}
	if err := config.Cluster.Node.Validate(); err != nil {
		return fmt.Errorf("invalid cluster node configuration: %w", err)
	}
	internalEndpoint, err := url.Parse(config.Cluster.Node.HTTPEndpoint)
	if err != nil || internalEndpoint.Scheme != "https" || internalEndpoint.Host == "" {
		return fmt.Errorf("NODE_HTTP_ENDPOINT must be an absolute HTTPS URL in cluster mode")
	}
	if config.Cluster.Node.BrowserEndpoint != "" {
		browserEndpoint, parseErr := url.Parse(config.Cluster.Node.BrowserEndpoint)
		if parseErr != nil || browserEndpoint.Scheme != "https" || browserEndpoint.Host == "" {
			return fmt.Errorf("NODE_BROWSER_ENDPOINT must be an absolute HTTPS URL in cluster mode")
		}
	}
	for _, endpoint := range strings.Split(os.Getenv("ETCD_ENDPOINTS"), ",") {
		if endpoint = strings.TrimSpace(endpoint); endpoint != "" {
			config.Cluster.Endpoints = append(config.Cluster.Endpoints, endpoint)
		}
	}
	if len(config.Cluster.Endpoints) == 0 {
		return fmt.Errorf("ETCD_ENDPOINTS is required in cluster mode")
	}
	config.Cluster.Username = os.Getenv("ETCD_USERNAME")
	config.Cluster.Password = os.Getenv("ETCD_PASSWORD")
	config.Cluster.CAFile = os.Getenv("ETCD_CA_FILE")
	config.Cluster.CertificateFile = os.Getenv("ETCD_CERT_FILE")
	config.Cluster.KeyFile = os.Getenv("ETCD_KEY_FILE")
	if config.Cluster.CAFile == "" || config.Cluster.CertificateFile == "" || config.Cluster.KeyFile == "" {
		return fmt.Errorf("ETCD_CA_FILE, ETCD_CERT_FILE and ETCD_KEY_FILE are required in cluster mode")
	}
	if config.Cluster.LeaseTTL, err = duration("ETCD_LEASE_TTL", 10*time.Second); err != nil {
		return err
	}
	if config.Cluster.LeaseTTL < 2*time.Second {
		return fmt.Errorf("ETCD_LEASE_TTL must be at least 2s")
	}
	if config.Cluster.DialTimeout, err = duration("ETCD_DIAL_TIMEOUT", 5*time.Second); err != nil {
		return err
	}
	if config.Cluster.RequestTimeout, err = duration("ETCD_REQUEST_TIMEOUT", 5*time.Second); err != nil {
		return err
	}
	config.Cluster.InternalRPC = InternalRPCConfig{
		Address:         envOrDefault("INTERNAL_RPC_ADDR", ":9443"),
		CAFile:          os.Getenv("INTERNAL_RPC_CA_FILE"),
		CertificateFile: os.Getenv("INTERNAL_RPC_CERT_FILE"),
		KeyFile:         os.Getenv("INTERNAL_RPC_KEY_FILE"),
	}
	if config.Cluster.InternalRPC.CAFile == "" || config.Cluster.InternalRPC.CertificateFile == "" || config.Cluster.InternalRPC.KeyFile == "" {
		return fmt.Errorf("INTERNAL_RPC_CA_FILE, INTERNAL_RPC_CERT_FILE and INTERNAL_RPC_KEY_FILE are required in cluster mode")
	}
	if config.Cluster.InternalRPC.Timeout, err = duration("INTERNAL_RPC_TIMEOUT", 5*time.Second); err != nil {
		return err
	}
	return nil
}

func loadAuthenticationConfig(config *Config) error {
	secret := os.Getenv("JWT_SECRET")
	if len([]byte(secret)) < 32 {
		return fmt.Errorf("JWT_SECRET must be at least 32 bytes")
	}
	jwtTTL, err := duration("JWT_TTL", 15*time.Minute)
	if err != nil {
		return err
	}
	refreshTTL, err := duration("REFRESH_TOKEN_TTL", 7*24*time.Hour)
	if err != nil {
		return err
	}
	replayTTL, err := duration("REFRESH_REPLAY_TTL", 10*time.Minute)
	if err != nil {
		return err
	}
	replayKey, err := decodeReplayKey(os.Getenv("REFRESH_REPLAY_ENCRYPTION_KEY"))
	if err != nil {
		return err
	}
	redisDB, err := nonNegativeInt("REDIS_DB", 0)
	if err != nil {
		return err
	}
	config.JWT = authentication.JWTConfig{
		Secret: []byte(secret), Issuer: envOrDefault("JWT_ISSUER", "collab-editor"),
		Audience: envOrDefault("JWT_AUDIENCE", "collab-editor-api"), AccessTTL: jwtTTL,
	}
	config.RefreshTTL = refreshTTL
	config.RefreshReplayTTL = replayTTL
	config.RefreshReplayKey = replayKey
	config.Redis = &redis.Options{Addr: envOrDefault("REDIS_ADDR", "127.0.0.1:6379"), Password: os.Getenv("REDIS_PASSWORD"), DB: redisDB}
	if caFile := os.Getenv("REDIS_TLS_CA_FILE"); caFile != "" {
		caPEM, err := os.ReadFile(caFile)
		if err != nil {
			return fmt.Errorf("read REDIS_TLS_CA_FILE: %w", err)
		}
		roots := x509.NewCertPool()
		if !roots.AppendCertsFromPEM(caPEM) {
			return fmt.Errorf("REDIS_TLS_CA_FILE contains no valid certificates")
		}
		config.Redis.TLSConfig = &tls.Config{
			MinVersion: tls.VersionTLS12,
			RootCAs:    roots,
			ServerName: os.Getenv("REDIS_TLS_SERVER_NAME"),
		}
	}
	if config.Environment == "production" {
		if config.Redis.Password == "" {
			return fmt.Errorf("production requires REDIS_PASSWORD")
		}
		if bytes.Equal([]byte(secret), replayKey) {
			return fmt.Errorf("JWT_SECRET and REFRESH_REPLAY_ENCRYPTION_KEY must be independent")
		}
	}
	return nil
}

func loadSecurityConfig(config *Config) error {
	for _, origin := range strings.Split(os.Getenv("CORS_ALLOWED_ORIGINS"), ",") {
		origin = strings.TrimSpace(origin)
		if origin == "" {
			continue
		}
		parsed, err := url.Parse(origin)
		if err != nil || (parsed.Scheme != "https" && !(config.Environment != "production" && parsed.Scheme == "http")) || parsed.Host == "" || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
			return fmt.Errorf("CORS_ALLOWED_ORIGINS must contain absolute origins without paths")
		}
		config.Security.AllowedOrigins = append(config.Security.AllowedOrigins, origin)
	}
	var err error
	if config.Security.LoginRateLimit, err = positiveInt("LOGIN_RATE_LIMIT", 20); err != nil {
		return err
	}
	if config.Security.RefreshRateLimit, err = positiveInt("REFRESH_RATE_LIMIT", 60); err != nil {
		return err
	}
	if config.Security.InternalRPCRateLimit, err = positiveInt("INTERNAL_RPC_RATE_LIMIT", 300); err != nil {
		return err
	}
	if config.Security.RateLimitWindow, err = duration("RATE_LIMIT_WINDOW", time.Minute); err != nil {
		return err
	}
	return nil
}

func loadExecutionConfig(config *Config) error {
	limits := execution.DefaultLimits()
	limits.Image = envOrDefault("RUNNER_IMAGE", limits.Image)
	var err error
	if limits.Timeout, err = duration("EXECUTION_TIMEOUT", limits.Timeout); err != nil {
		return err
	}
	if limits.MaxSourceBytes, err = positiveInt64("EXECUTION_MAX_SOURCE_BYTES", limits.MaxSourceBytes); err != nil {
		return err
	}
	if limits.MaxOutputBytes, err = positiveInt64("EXECUTION_MAX_OUTPUT_BYTES", limits.MaxOutputBytes); err != nil {
		return err
	}
	if limits.MemoryBytes, err = positiveInt64("EXECUTION_MEMORY_BYTES", limits.MemoryBytes); err != nil {
		return err
	}
	if limits.PIDs, err = positiveInt64("EXECUTION_PIDS", limits.PIDs); err != nil {
		return err
	}
	if limits.WorkspaceBytes, err = positiveInt64("EXECUTION_WORKSPACE_BYTES", limits.WorkspaceBytes); err != nil {
		return err
	}
	if limits.TemporaryBytes, err = positiveInt64("EXECUTION_TEMPORARY_BYTES", limits.TemporaryBytes); err != nil {
		return err
	}
	if value := os.Getenv("EXECUTION_CPUS"); value != "" {
		limits.CPUs, err = strconv.ParseFloat(value, 64)
		if err != nil || limits.CPUs <= 0 {
			return fmt.Errorf("EXECUTION_CPUS must be positive")
		}
	}
	socketPath := os.Getenv("RUNNER_SOCKET")
	token := os.Getenv("RUNNER_TOKEN")
	if (socketPath == "") != (token == "") {
		return fmt.Errorf("RUNNER_SOCKET and RUNNER_TOKEN must be configured together")
	}
	if token != "" && len(token) < 32 {
		return fmt.Errorf("RUNNER_TOKEN must be at least 32 bytes")
	}
	globalLimit, err := positiveInt("EXECUTION_GLOBAL_CONCURRENCY", 4)
	if err != nil {
		return err
	}
	userLimit, err := positiveInt("EXECUTION_USER_CONCURRENCY", 1)
	if err != nil {
		return err
	}
	rateLimit, err := positiveInt("EXECUTION_RATE_LIMIT", 10)
	if err != nil {
		return err
	}
	config.Execution = ExecutionConfig{
		SocketPath: socketPath, Token: token, Limits: limits,
		GlobalLimit: globalLimit, UserLimit: userLimit, RateLimit: rateLimit,
	}
	return nil
}

func decodeReplayKey(encoded string) ([]byte, error) {
	key, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(key) != 32 {
		return nil, fmt.Errorf("REFRESH_REPLAY_ENCRYPTION_KEY must be base64 encoding of exactly 32 bytes")
	}
	return key, nil
}

func loadMySQLConfig(config *Config) error {
	maxOpen, err := positiveInt("MYSQL_MAX_OPEN_CONNS", 20)
	if err != nil {
		return err
	}
	maxIdle, err := positiveInt("MYSQL_MAX_IDLE_CONNS", 10)
	if err != nil {
		return err
	}
	connLifetime, err := duration("MYSQL_CONN_MAX_LIFETIME", 5*time.Minute)
	if err != nil {
		return err
	}
	pingTimeout, err := duration("MYSQL_PING_TIMEOUT", 5*time.Second)
	if err != nil {
		return err
	}
	queryTimeout, err := duration("MYSQL_QUERY_TIMEOUT", 5*time.Second)
	if err != nil {
		return err
	}
	config.MySQL = mysqlstorage.Config{
		DSN:             os.Getenv("MYSQL_DSN"),
		MaxOpenConns:    maxOpen,
		MaxIdleConns:    maxIdle,
		ConnMaxLifetime: connLifetime,
		PingTimeout:     pingTimeout,
		QueryTimeout:    queryTimeout,
	}
	if config.MySQL.DSN == "" {
		return fmt.Errorf("MYSQL_DSN is required when STORAGE_DRIVER=mysql")
	}
	return nil
}

func loadQUICConfig() (QUICConfig, error) {
	config := QUICConfig{
		Address:     envOrDefault("QUIC_ADDR", ":8443"),
		TLSCertFile: os.Getenv("QUIC_TLS_CERT_FILE"),
		TLSKeyFile:  os.Getenv("QUIC_TLS_KEY_FILE"),
		CAFile:      os.Getenv("QUIC_CA_FILE"),
	}
	if config.TLSCertFile == "" || config.TLSKeyFile == "" || config.CAFile == "" {
		return QUICConfig{}, fmt.Errorf("QUIC_TLS_CERT_FILE, QUIC_TLS_KEY_FILE and QUIC_CA_FILE are required")
	}
	var err error
	if config.HandshakeTimeout, err = duration("QUIC_HANDSHAKE_TIMEOUT", 5*time.Second); err != nil {
		return QUICConfig{}, err
	}
	if config.IdleTimeout, err = duration("QUIC_IDLE_TIMEOUT", 60*time.Second); err != nil {
		return QUICConfig{}, err
	}
	if config.KeepAlivePeriod, err = duration("QUIC_KEEPALIVE_PERIOD", 20*time.Second); err != nil {
		return QUICConfig{}, err
	}
	if config.AuthTimeout, err = duration("QUIC_AUTH_TIMEOUT", 10*time.Second); err != nil {
		return QUICConfig{}, err
	}
	if config.ReadTimeout, err = duration("QUIC_READ_TIMEOUT", 2*time.Minute); err != nil {
		return QUICConfig{}, err
	}
	if config.WriteTimeout, err = duration("QUIC_WRITE_TIMEOUT", 10*time.Second); err != nil {
		return QUICConfig{}, err
	}
	if config.OperationTimeout, err = duration("QUIC_OPERATION_TIMEOUT", 10*time.Second); err != nil {
		return QUICConfig{}, err
	}
	if config.MaxMessageSize, err = positiveInt("QUIC_MAX_MESSAGE_SIZE", 1024*1024); err != nil {
		return QUICConfig{}, err
	}
	if uint64(config.MaxMessageSize) > uint64(^uint32(0)) {
		return QUICConfig{}, fmt.Errorf("QUIC_MAX_MESSAGE_SIZE must fit in 32 bits")
	}
	if config.SendQueueSize, err = positiveInt("QUIC_SEND_QUEUE_SIZE", 32); err != nil {
		return QUICConfig{}, err
	}
	if config.MaxConnections, err = positiveInt("QUIC_MAX_CONNECTIONS", 1000); err != nil {
		return QUICConfig{}, err
	}
	return config, nil
}

func nonNegativeInt(name string, fallback int) (int, error) {
	value := os.Getenv(name)
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < 0 {
		return 0, fmt.Errorf("%s must be a non-negative integer", name)
	}
	return parsed, nil
}

func envOrDefault(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func positiveInt(name string, fallback int) (int, error) {
	value := os.Getenv(name)
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed <= 0 {
		return 0, fmt.Errorf("%s must be a positive integer", name)
	}
	return parsed, nil
}

func positiveInt64(name string, fallback int64) (int64, error) {
	value := os.Getenv(name)
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil || parsed <= 0 {
		return 0, fmt.Errorf("%s must be a positive integer", name)
	}
	return parsed, nil
}

func duration(name string, fallback time.Duration) (time.Duration, error) {
	value := os.Getenv(name)
	if value == "" {
		return fallback, nil
	}
	parsed, err := time.ParseDuration(value)
	if err != nil || parsed <= 0 {
		return 0, fmt.Errorf("%s must be a positive duration", name)
	}
	return parsed, nil
}
