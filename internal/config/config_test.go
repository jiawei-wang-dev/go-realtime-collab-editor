package config

import (
	"strings"
	"testing"
)

func TestProductionRequiresMySQL(t *testing.T) {
	t.Setenv("APP_ENV", "production")
	t.Setenv("STORAGE_DRIVER", "memory")
	if _, err := Load(); err == nil {
		t.Fatal("production memory storage was accepted")
	}
}

func TestProductionRequiresRESTTLS(t *testing.T) {
	setValidRuntimeEnvironment(t)
	t.Setenv("APP_ENV", "production")
	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "REST_TLS_CERT_FILE") {
		t.Fatalf("production without REST TLS error = %v", err)
	}
	t.Setenv("REST_TLS_CERT_FILE", "rest.crt")
	t.Setenv("REST_TLS_KEY_FILE", "rest.key")
	if _, err := Load(); err != nil {
		t.Fatalf("production with REST TLS config: %v", err)
	}
}

func TestProductionSecurityRequirements(t *testing.T) {
	setValidRuntimeEnvironment(t)
	t.Setenv("APP_ENV", "production")
	t.Setenv("REST_TLS_CERT_FILE", "rest.crt")
	t.Setenv("REST_TLS_KEY_FILE", "rest.key")
	t.Setenv("REDIS_PASSWORD", "")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "REDIS_PASSWORD") {
		t.Fatalf("production without Redis password error = %v", err)
	}

	t.Setenv("REDIS_PASSWORD", "redis-test-password")
	t.Setenv("REFRESH_REPLAY_ENCRYPTION_KEY", "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=")
	t.Setenv("JWT_SECRET", "0123456789abcdef0123456789abcdef")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "independent") {
		t.Fatalf("shared JWT and replay key error = %v", err)
	}

	t.Setenv("JWT_SECRET", "abcdef0123456789abcdef0123456789")
	t.Setenv("CORS_ALLOWED_ORIGINS", "http://editor.example.com")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "CORS_ALLOWED_ORIGINS") {
		t.Fatalf("insecure production origin error = %v", err)
	}
}

func TestRESTTLSConfigurationMustBePaired(t *testing.T) {
	t.Setenv("REST_TLS_CERT_FILE", "rest.crt")
	if _, err := Load(); err == nil {
		t.Fatal("REST certificate without key was accepted")
	}
}

func TestWebTransportRequiresTLSAndValidLimits(t *testing.T) {
	t.Setenv("WEBTRANSPORT_ENABLED", "true")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "WebTransport or REST TLS") {
		t.Fatalf("WebTransport without TLS error = %v", err)
	}
	t.Setenv("WEBTRANSPORT_TLS_CERT_FILE", "webtransport.crt")
	t.Setenv("WEBTRANSPORT_TLS_KEY_FILE", "webtransport.key")
	// Loading now proceeds to the existing authentication checks.
	if _, err := Load(); err == nil || strings.Contains(err.Error(), "WEBTRANSPORT") {
		t.Fatalf("valid WebTransport configuration error = %v", err)
	}
	t.Setenv("WEBTRANSPORT_MAX_MESSAGE_SIZE", "0")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "WEBTRANSPORT_MAX_MESSAGE_SIZE") {
		t.Fatalf("invalid WebTransport message size error = %v", err)
	}
}

func TestUnknownEnvironmentIsRejected(t *testing.T) {
	t.Setenv("APP_ENV", "prodution")
	if _, err := Load(); err == nil {
		t.Fatal("unknown environment was accepted")
	}
}

func TestObservabilityAdminAddressIsRestricted(t *testing.T) {
	t.Setenv("ADMIN_ADDR", ":9090")
	if _, err := Load(); err == nil {
		t.Fatal("unspecified management bind address was accepted")
	}
	t.Setenv("ADMIN_ADDR", "0.0.0.0:9090")
	if _, err := Load(); err == nil {
		t.Fatal("public management bind address was accepted")
	}
	t.Setenv("ADMIN_ADDR", "127.0.0.1:9090")
	// Loading continues to the existing authentication validation, proving the
	// management address itself was accepted.
	if _, err := Load(); err == nil || strings.Contains(err.Error(), "ADMIN_ADDR") {
		t.Fatalf("private management address error = %v", err)
	}
}

func TestMySQLRequiresDSN(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	t.Setenv("STORAGE_DRIVER", "mysql")
	t.Setenv("MYSQL_DSN", "")
	if _, err := Load(); err == nil {
		t.Fatal("mysql storage without DSN was accepted")
	}
}

func TestSnapshotConfigurationValidation(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	t.Setenv("AUTH_MODE", "development")
	t.Setenv("STORAGE_DRIVER", "memory")
	t.Setenv("SNAPSHOT_INTERVAL", "100")
	t.Setenv("MAX_HISTORY_OPERATIONS", "99")
	if _, err := Load(); err == nil {
		t.Fatal("history limit below snapshot interval was accepted")
	}
	t.Setenv("MAX_HISTORY_OPERATIONS", "1000")
	t.Setenv("SNAPSHOT_TIMEOUT", "invalid")
	if _, err := Load(); err == nil {
		t.Fatal("invalid snapshot timeout was accepted")
	}
}

func TestQUICConfigurationIsRequiredAndValidated(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	t.Setenv("AUTH_MODE", "jwt")
	t.Setenv("STORAGE_DRIVER", "mysql")
	t.Setenv("MYSQL_DSN", "user:password@tcp(localhost:3306)/database")
	t.Setenv("JWT_SECRET", "abcdef0123456789abcdef0123456789")
	t.Setenv("REFRESH_REPLAY_ENCRYPTION_KEY", "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=")
	if _, err := Load(); err == nil {
		t.Fatal("missing QUIC TLS files were accepted")
	}
	t.Setenv("QUIC_TLS_CERT_FILE", "server.crt")
	t.Setenv("QUIC_TLS_KEY_FILE", "server.key")
	t.Setenv("QUIC_CA_FILE", "ca.crt")
	t.Setenv("QUIC_MAX_MESSAGE_SIZE", "1048576")
	config, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if config.QUIC.Address != ":8443" || config.QUIC.MaxMessageSize != 1024*1024 {
		t.Fatalf("QUIC config = %#v", config.QUIC)
	}
	if config.QUIC.MaxConnections != 1000 {
		t.Fatalf("QUIC max connections = %d", config.QUIC.MaxConnections)
	}
	t.Setenv("QUIC_MAX_CONNECTIONS", "0")
	if _, err := Load(); err == nil {
		t.Fatal("invalid QUIC connection limit was accepted")
	}
	t.Setenv("QUIC_MAX_CONNECTIONS", "1000")
	t.Setenv("QUIC_MAX_MESSAGE_SIZE", "0")
	if _, err := Load(); err == nil {
		t.Fatal("invalid QUIC message size was accepted")
	}
}

func TestDevelopmentAuthenticationCannotBackQUIC(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	t.Setenv("AUTH_MODE", "development")
	t.Setenv("STORAGE_DRIVER", "memory")
	if _, err := Load(); err == nil {
		t.Fatal("untrusted development identity was accepted for QUIC")
	}
}

func TestClusterConfigurationRequiresNodeAndTLS(t *testing.T) {
	t.Setenv("CLUSTER_MODE", "cluster")
	if _, err := Load(); err == nil {
		t.Fatal("cluster mode without node configuration was accepted")
	}
}

func TestExecutionConfigurationRequiresPairedSocketAndStrongToken(t *testing.T) {
	setValidRuntimeEnvironment(t)
	t.Setenv("RUNNER_SOCKET", "/tmp/collab-runner.sock")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "configured together") {
		t.Fatalf("unpaired runner configuration error = %v", err)
	}
	t.Setenv("RUNNER_TOKEN", "short")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "at least 32") {
		t.Fatalf("short runner token error = %v", err)
	}
	t.Setenv("RUNNER_TOKEN", "01234567890123456789012345678901")
	loaded, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if !loaded.Execution.Enabled() || loaded.Execution.Limits.MaxSourceBytes != 128*1024 || loaded.Execution.UserLimit != 1 {
		t.Fatalf("execution config = %#v", loaded.Execution)
	}
}

func TestLoadClusterConfiguration(t *testing.T) {
	setValidRuntimeEnvironment(t)
	t.Setenv("CLUSTER_MODE", "cluster")
	t.Setenv("NODE_ID", "node-a")
	t.Setenv("NODE_QUIC_ENDPOINT", "node-a:8443")
	t.Setenv("NODE_HTTP_ENDPOINT", "https://node-a:9443")
	t.Setenv("NODE_BROWSER_ENDPOINT", "https://node-a.example:8080")
	t.Setenv("ETCD_ENDPOINTS", "https://etcd-a:2379, https://etcd-b:2379")
	t.Setenv("ETCD_CA_FILE", "ca.crt")
	t.Setenv("ETCD_CERT_FILE", "client.crt")
	t.Setenv("ETCD_KEY_FILE", "client.key")
	t.Setenv("INTERNAL_RPC_CA_FILE", "internal-ca.crt")
	t.Setenv("INTERNAL_RPC_CERT_FILE", "node-a.crt")
	t.Setenv("INTERNAL_RPC_KEY_FILE", "node-a.key")
	loaded, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if !loaded.Cluster.Enabled() || loaded.Cluster.Node.NodeID != "node-a" || len(loaded.Cluster.Endpoints) != 2 {
		t.Fatalf("unexpected cluster config: %#v", loaded.Cluster)
	}
	if loaded.Cluster.Node.Incarnation == "" || loaded.Cluster.Node.StartedAt.IsZero() {
		t.Fatal("process identity was not generated")
	}
	if loaded.Cluster.Node.BrowserEndpoint != "https://node-a.example:8080" {
		t.Fatalf("browser endpoint = %q", loaded.Cluster.Node.BrowserEndpoint)
	}
}

func setValidRuntimeEnvironment(t *testing.T) {
	t.Helper()
	t.Setenv("APP_ENV", "development")
	t.Setenv("AUTH_MODE", "jwt")
	t.Setenv("STORAGE_DRIVER", "mysql")
	t.Setenv("MYSQL_DSN", "user:password@tcp(localhost:3306)/database")
	t.Setenv("JWT_SECRET", "abcdef0123456789abcdef0123456789")
	t.Setenv("REFRESH_REPLAY_ENCRYPTION_KEY", "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=")
	t.Setenv("QUIC_TLS_CERT_FILE", "server.crt")
	t.Setenv("QUIC_TLS_KEY_FILE", "server.key")
	t.Setenv("QUIC_CA_FILE", "ca.crt")
	t.Setenv("REDIS_PASSWORD", "redis-test-password")
}
