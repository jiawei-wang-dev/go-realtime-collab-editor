package etcdlock

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"os"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"
	"go.etcd.io/etcd/client/v3/concurrency"
)

const SharedMembership = "/collab-test-locks/shared-membership-v1"

type Config struct {
	Endpoints       []string
	CAFile          string
	CertificateFile string
	KeyFile         string
	DialTimeout     time.Duration
}

func Open(config Config) (*clientv3.Client, error) {
	if len(config.Endpoints) == 0 {
		return nil, errors.New("etcd test lock requires at least one endpoint")
	}
	if config.DialTimeout <= 0 {
		return nil, errors.New("etcd test lock dial timeout must be positive")
	}
	tlsConfig, err := loadTLS(config)
	if err != nil {
		return nil, err
	}
	client, err := clientv3.New(clientv3.Config{Endpoints: config.Endpoints, DialTimeout: config.DialTimeout, TLS: tlsConfig})
	if err != nil {
		return nil, fmt.Errorf("open etcd test lock client: %w", err)
	}
	return client, nil
}

// Acquire holds a cross-process test mutex with an independent lease. If the
// process exits, session keepalive stops and etcd removes the key after its TTL.
func Acquire(ctx context.Context, client *clientv3.Client, namespace string, ttlSeconds int) (func() error, error) {
	if client == nil || namespace == "" {
		return nil, errors.New("etcd test lock client and namespace are required")
	}
	if ttlSeconds < 2 {
		return nil, errors.New("etcd test lock TTL must be at least two seconds")
	}
	session, err := concurrency.NewSession(client, concurrency.WithTTL(ttlSeconds), concurrency.WithContext(ctx))
	if err != nil {
		return nil, fmt.Errorf("create etcd test lock session: %w", err)
	}
	mutex := concurrency.NewMutex(session, namespace)
	if err := mutex.Lock(ctx); err != nil {
		_ = session.Close()
		return nil, fmt.Errorf("acquire etcd test lock: %w", err)
	}
	return func() error {
		releaseCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		unlockErr := mutex.Unlock(releaseCtx)
		closeErr := session.Close()
		return errors.Join(unlockErr, closeErr)
	}, nil
}

func loadTLS(config Config) (*tls.Config, error) {
	if config.CAFile == "" && config.CertificateFile == "" && config.KeyFile == "" {
		return nil, nil
	}
	if config.CAFile == "" || config.CertificateFile == "" || config.KeyFile == "" {
		return nil, errors.New("etcd test lock TLS requires CA, certificate and key files")
	}
	caPEM, err := os.ReadFile(config.CAFile)
	if err != nil {
		return nil, fmt.Errorf("read etcd test lock CA: %w", err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(caPEM) {
		return nil, errors.New("etcd test lock CA contains no valid certificate")
	}
	certificate, err := tls.LoadX509KeyPair(config.CertificateFile, config.KeyFile)
	if err != nil {
		return nil, fmt.Errorf("load etcd test lock certificate: %w", err)
	}
	return &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots, Certificates: []tls.Certificate{certificate}}, nil
}
