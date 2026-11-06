package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/api"
	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/config"
	mysqlstorage "github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/storage/mysql"
	quictransport "github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/transport/quic"
)

func TestHTTPServerTimeouts(t *testing.T) {
	server := newHTTPServer(":0", http.NotFoundHandler())
	if server.ReadHeaderTimeout <= 0 || server.ReadTimeout <= 0 || server.WriteTimeout <= 0 || server.IdleTimeout <= 0 {
		t.Fatalf("HTTP timeouts are not fully configured: %#v", server)
	}
}

func TestRESTTLSConfigAndHandshake(t *testing.T) {
	certificateFile, keyFile, roots := writeRESTCertificate(t, time.Now().Add(-time.Minute), time.Now().Add(time.Hour))
	tlsConfig, err := loadRESTTLSConfig(config.RESTConfig{TLSCertFile: certificateFile, TLSKeyFile: keyFile})
	if err != nil {
		t.Fatal(err)
	}
	if tlsConfig.MinVersion != tls.VersionTLS12 {
		t.Fatalf("minimum TLS version = %x", tlsConfig.MinVersion)
	}

	listener, err := listenREST("127.0.0.1:0", config.RESTConfig{TLSCertFile: certificateFile, TLSKeyFile: keyFile})
	if err != nil {
		t.Fatal(err)
	}
	server := newHTTPServer(listener.Addr().String(), http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusNoContent)
	}))
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	t.Cleanup(func() {
		_ = server.Close()
		<-done
	})

	trustedClient := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, ServerName: "localhost", MinVersion: tls.VersionTLS12}}}
	response, err := trustedClient.Get("https://" + listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("HTTPS status = %d", response.StatusCode)
	}
	if _, err := http.Get("https://" + listener.Addr().String()); err == nil {
		t.Fatal("untrusted REST certificate was accepted")
	}
}

func TestRESTTLSRejectsExpiredCertificateAndWrongKey(t *testing.T) {
	expiredCertificate, expiredKey, _ := writeRESTCertificate(t, time.Now().Add(-2*time.Hour), time.Now().Add(-time.Hour))
	if _, err := loadRESTTLSConfig(config.RESTConfig{TLSCertFile: expiredCertificate, TLSKeyFile: expiredKey}); err == nil {
		t.Fatal("expired REST certificate was accepted")
	}
	validCertificate, _, _ := writeRESTCertificate(t, time.Now().Add(-time.Minute), time.Now().Add(time.Hour))
	_, unrelatedKey, _ := writeRESTCertificate(t, time.Now().Add(-time.Minute), time.Now().Add(time.Hour))
	if _, err := loadRESTTLSConfig(config.RESTConfig{TLSCertFile: validCertificate, TLSKeyFile: unrelatedKey}); err == nil {
		t.Fatal("REST certificate with unrelated private key was accepted")
	}
}

func writeRESTCertificate(t *testing.T, notBefore, notAfter time.Time) (string, string, *x509.CertPool) {
	t.Helper()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: "localhost"},
		DNSNames: []string{"localhost"}, NotBefore: notBefore, NotAfter: notAfter,
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	certificateDER, err := x509.CreateCertificate(rand.Reader, template, template, publicKey, privateKey)
	if err != nil {
		t.Fatal(err)
	}
	privateKeyDER, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	certificateFile := directory + "/rest.crt"
	keyFile := directory + "/rest.key"
	certificatePEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificateDER})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privateKeyDER})
	if err := os.WriteFile(certificateFile, certificatePEM, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyFile, keyPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	certificate, err := x509.ParseCertificate(certificateDER)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(certificate)
	return certificateFile, keyFile, roots
}

func TestServeGracefulShutdown(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	application := api.NewServer()
	server := newHTTPServer(listener.Addr().String(), application)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	transport := &fakeQUICLifecycle{serveDone: make(chan struct{})}
	go func() {
		done <- serve(ctx, listener, server, transport, application)
	}()

	request, err := http.NewRequest(http.MethodPost, "http://"+listener.Addr().String()+"/api/documents", strings.NewReader(`{"id":"shutdown","content":"","owner_id":"owner"}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-User-ID", "owner")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("create status = %d", response.StatusCode)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("server did not shut down gracefully")
	}
	if !transport.closed {
		t.Fatal("QUIC transport was not shut down")
	}
}

func TestServeGracefulShutdownWithRESTTLS(t *testing.T) {
	certificateFile, keyFile, roots := writeRESTCertificate(t, time.Now().Add(-time.Minute), time.Now().Add(time.Hour))
	listener, err := listenREST("127.0.0.1:0", config.RESTConfig{TLSCertFile: certificateFile, TLSKeyFile: keyFile})
	if err != nil {
		t.Fatal(err)
	}
	application := api.NewServer()
	server := newHTTPServer(listener.Addr().String(), application)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	transport := &fakeQUICLifecycle{serveDone: make(chan struct{})}
	go func() { done <- serve(ctx, listener, server, transport, application) }()
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, ServerName: "localhost", MinVersion: tls.VersionTLS12}}}
	response, err := client.Get("https://" + listener.Addr().String() + "/api/documents/missing")
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("TLS REST server did not shut down gracefully")
	}
	if !transport.closed {
		t.Fatal("QUIC transport was not shut down")
	}
}

func TestQUICStartupFailureReleasesHTTPListener(t *testing.T) {
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := probe.Addr().String()
	if err := probe.Close(); err != nil {
		t.Fatal(err)
	}

	application := api.NewServer()
	listener, quicServer, err := listenServers(address, config.RESTConfig{}, application, func() (*quictransport.Server, error) {
		return nil, errors.New("injected QUIC bind failure")
	})
	if err == nil {
		t.Fatal("QUIC startup failure was ignored")
	}
	if listener != nil || quicServer != nil {
		t.Fatal("failed startup returned live server resources")
	}

	rebound, err := net.Listen("tcp", address)
	if err != nil {
		t.Fatalf("HTTP listener was not released: %v", err)
	}
	_ = rebound.Close()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := application.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("repeated application shutdown: %v", err)
	}
}

func TestHTTPStartupFailureShutsDownApplication(t *testing.T) {
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	application := api.NewServer()
	quicStarted := false
	listener, quicServer, err := listenServers(occupied.Addr().String(), config.RESTConfig{}, application, func() (*quictransport.Server, error) {
		quicStarted = true
		return nil, errors.New("must not be called")
	})
	if err == nil {
		t.Fatal("HTTP bind failure was ignored")
	}
	if listener != nil || quicServer != nil || quicStarted {
		t.Fatal("HTTP bind failure started or returned server resources")
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := application.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("repeated application shutdown: %v", err)
	}
}

type fakeQUICLifecycle struct {
	serveDone chan struct{}
	closed    bool
}

func (f *fakeQUICLifecycle) Serve(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return nil
	case <-f.serveDone:
		return errors.New("unexpected transport stop")
	}
}

func (f *fakeQUICLifecycle) Close(context.Context) error {
	f.closed = true
	return nil
}

func TestMySQLUnavailableDoesNotFallBackToMemory(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	store, err := openStore(ctx, config.Config{
		StorageDriver: config.StorageMySQL,
		MySQL: mysqlstorage.Config{
			DSN:          "invalid:invalid@tcp(127.0.0.1:1)/missing",
			PingTimeout:  100 * time.Millisecond,
			QueryTimeout: time.Second,
		},
	})
	if err == nil {
		_ = store.Close()
		t.Fatal("unavailable MySQL silently returned a store")
	}
	if store != nil {
		t.Fatal("unavailable MySQL fell back to another store")
	}
}
