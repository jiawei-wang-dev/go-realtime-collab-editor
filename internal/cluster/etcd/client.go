package etcd

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"os"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"
)

type Config struct {
	Endpoints       []string
	Username        string
	Password        string
	CAFile          string
	CertificateFile string
	KeyFile         string
	DialTimeout     time.Duration
	RequestTimeout  time.Duration
}

type Client struct {
	client         *clientv3.Client
	requestTimeout time.Duration
}

func Open(ctx context.Context, config Config) (*Client, error) {
	if len(config.Endpoints) == 0 {
		return nil, errors.New("at least one etcd endpoint is required")
	}
	if config.DialTimeout <= 0 || config.RequestTimeout <= 0 {
		return nil, errors.New("etcd dial and request timeouts must be positive")
	}
	tlsConfig, err := loadTLSConfig(config)
	if err != nil {
		return nil, err
	}
	rawClient, err := clientv3.New(clientv3.Config{
		Endpoints:   append([]string(nil), config.Endpoints...),
		DialTimeout: config.DialTimeout,
		Username:    config.Username,
		Password:    config.Password,
		TLS:         tlsConfig,
	})
	if err != nil {
		return nil, fmt.Errorf("open etcd client: %w", err)
	}
	client := &Client{client: rawClient, requestTimeout: config.RequestTimeout}
	requestCtx, cancel := client.requestContext(ctx)
	defer cancel()
	if _, err := rawClient.Get(requestCtx, nodePrefix, clientv3.WithPrefix(), clientv3.WithLimit(1)); err != nil {
		_ = rawClient.Close()
		return nil, fmt.Errorf("verify etcd availability: %w", err)
	}
	return client, nil
}

func (client *Client) Close() error {
	if client == nil || client.client == nil {
		return nil
	}
	return client.client.Close()
}

func (client *Client) Ping(ctx context.Context) error {
	requestCtx, cancel := client.requestContext(ctx)
	defer cancel()
	_, err := client.client.Get(requestCtx, nodePrefix, clientv3.WithPrefix(), clientv3.WithLimit(1))
	return err
}

func (client *Client) requestContext(parent context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(parent, client.requestTimeout)
}

func loadTLSConfig(config Config) (*tls.Config, error) {
	usesTLS := config.CAFile != "" || config.CertificateFile != "" || config.KeyFile != ""
	if !usesTLS {
		return nil, nil
	}
	if config.CAFile == "" {
		return nil, errors.New("ETCD_CA_FILE is required when etcd TLS is configured")
	}
	if (config.CertificateFile == "") != (config.KeyFile == "") {
		return nil, errors.New("ETCD_CERT_FILE and ETCD_KEY_FILE must be configured together")
	}
	caPEM, err := os.ReadFile(config.CAFile)
	if err != nil {
		return nil, fmt.Errorf("read etcd CA file: %w", err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(caPEM) {
		return nil, errors.New("ETCD_CA_FILE does not contain a valid certificate")
	}
	tlsConfig := &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}
	if config.CertificateFile != "" {
		certificate, err := tls.LoadX509KeyPair(config.CertificateFile, config.KeyFile)
		if err != nil {
			return nil, fmt.Errorf("load etcd client certificate: %w", err)
		}
		tlsConfig.Certificates = []tls.Certificate{certificate}
	}
	return tlsConfig, nil
}
