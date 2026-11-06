package rpc

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/cluster"
)

func TestIdentityFromCertificateRequiresCanonicalNodeAndIncarnation(t *testing.T) {
	identityURI, err := IdentityURI("node-a", "process-1")
	if err != nil {
		t.Fatal(err)
	}
	identity, err := IdentityFromCertificate(&x509.Certificate{URIs: []*url.URL{identityURI}})
	if err != nil {
		t.Fatal(err)
	}
	if identity.NodeID != "node-a" || identity.Incarnation != "process-1" {
		t.Fatalf("identity = %#v", identity)
	}

	invalid := []struct {
		name string
		uris []*url.URL
	}{
		{name: "missing"},
		{name: "wrong trust domain", uris: []*url.URL{{Scheme: "spiffe", Host: "other", Path: "/node/node-a/incarnation/process-1"}}},
		{name: "encoded ambiguity", uris: []*url.URL{{Scheme: "spiffe", Host: nodeIdentityTrustDomain, Path: "/node/node-a/incarnation/process-1", RawPath: "/node/node-a/incarnation/process%2D1"}}},
		{name: "multiple", uris: []*url.URL{identityURI, identityURI}},
	}
	for _, test := range invalid {
		t.Run(test.name, func(t *testing.T) {
			if _, err := IdentityFromCertificate(&x509.Certificate{URIs: test.uris}); err == nil {
				t.Fatal("invalid certificate identity was accepted")
			}
		})
	}
}

func TestLoadCredentialsBindsConfiguredProcessIdentity(t *testing.T) {
	caFile, certificateFile, keyFile := writeRPCTestCertificate(t, "node-a", "process-1")
	credentials, err := LoadCredentials(TLSConfig{CAFile: caFile, CertificateFile: certificateFile, KeyFile: keyFile})
	if err != nil {
		t.Fatal(err)
	}
	matching := cluster.Node{NodeID: "node-a", Incarnation: "process-1"}
	if err := credentials.ValidateIdentity(matching); err != nil {
		t.Fatal(err)
	}
	if err := credentials.ValidateIdentity(cluster.Node{NodeID: "node-a", Incarnation: "process-2"}); err == nil {
		t.Fatal("old certificate was accepted for a new incarnation")
	}
}

func TestLoadCredentialsRejectsExpiredCertificate(t *testing.T) {
	caFile, certificateFile, keyFile := writeRPCTestCertificateWithValidity(t, "node-a", "expired-process", time.Now().Add(-2*time.Hour), time.Now().Add(-time.Hour))
	if _, err := LoadCredentials(TLSConfig{CAFile: caFile, CertificateFile: certificateFile, KeyFile: keyFile}); err == nil {
		t.Fatal("expired internal RPC certificate was accepted")
	}
}

func writeRPCTestCertificate(t *testing.T, nodeID, incarnation string) (string, string, string) {
	t.Helper()
	now := time.Now()
	return writeRPCTestCertificateWithValidity(t, nodeID, incarnation, now.Add(-time.Hour), now.Add(time.Hour))
}

func writeRPCTestCertificateWithValidity(t *testing.T, nodeID, incarnation string, notBefore, notAfter time.Time) (string, string, string) {
	t.Helper()
	now := time.Now()
	caPublic, caPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	caTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test-ca"},
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour), IsCA: true,
		BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, caPublic, caPrivate)
	if err != nil {
		t.Fatal(err)
	}
	caCertificate, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatal(err)
	}

	identityURI, err := IdentityURI(nodeID, incarnation)
	if err != nil {
		t.Fatal(err)
	}
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	leafTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: nodeID}, URIs: []*url.URL{identityURI},
		DNSNames: []string{"localhost"}, NotBefore: notBefore, NotAfter: notAfter,
		KeyUsage:    x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTemplate, caCertificate, publicKey, caPrivate)
	if err != nil {
		t.Fatal(err)
	}
	privateDER, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}

	directory := t.TempDir()
	caFile := directory + "/ca.crt"
	certificateFile := directory + "/node.crt"
	keyFile := directory + "/node.key"
	writePEMFile(t, caFile, "CERTIFICATE", caDER)
	writePEMFile(t, certificateFile, "CERTIFICATE", leafDER)
	writePEMFile(t, keyFile, "PRIVATE KEY", privateDER)
	return caFile, certificateFile, keyFile
}

func writePEMFile(t *testing.T, path, blockType string, bytes []byte) {
	t.Helper()
	encoded := pem.EncodeToMemory(&pem.Block{Type: blockType, Bytes: bytes})
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
}
