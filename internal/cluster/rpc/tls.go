package rpc

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/cluster"
)

const nodeIdentityTrustDomain = "collab-editor"

type PeerIdentity struct {
	NodeID      string
	Incarnation string
}

type TLSConfig struct {
	CAFile          string
	CertificateFile string
	KeyFile         string
}

type Credentials struct {
	certificate tls.Certificate
	roots       *x509.CertPool
	identity    PeerIdentity
}

func LoadCredentials(config TLSConfig) (*Credentials, error) {
	if config.CAFile == "" || config.CertificateFile == "" || config.KeyFile == "" {
		return nil, errors.New("internal RPC CA, certificate and key files are required")
	}
	caPEM, err := os.ReadFile(config.CAFile)
	if err != nil {
		return nil, fmt.Errorf("read internal RPC CA: %w", err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(caPEM) {
		return nil, errors.New("internal RPC CA file contains no valid certificate")
	}
	certificate, err := tls.LoadX509KeyPair(config.CertificateFile, config.KeyFile)
	if err != nil {
		return nil, fmt.Errorf("load internal RPC certificate: %w", err)
	}
	if len(certificate.Certificate) == 0 {
		return nil, errors.New("internal RPC certificate chain is empty")
	}
	leaf, err := x509.ParseCertificate(certificate.Certificate[0])
	if err != nil {
		return nil, fmt.Errorf("parse internal RPC certificate: %w", err)
	}
	now := time.Now()
	if now.Before(leaf.NotBefore) || now.After(leaf.NotAfter) {
		return nil, errors.New("internal RPC certificate is not currently valid")
	}
	if !hasExtKeyUsage(leaf, x509.ExtKeyUsageServerAuth) || !hasExtKeyUsage(leaf, x509.ExtKeyUsageClientAuth) {
		return nil, errors.New("internal RPC certificate must allow both server and client authentication")
	}
	identity, err := IdentityFromCertificate(leaf)
	if err != nil {
		return nil, err
	}
	certificate.Leaf = leaf
	return &Credentials{certificate: certificate, roots: roots, identity: identity}, nil
}

func (credentials *Credentials) Identity() PeerIdentity {
	return credentials.identity
}

func (credentials *Credentials) ValidateIdentity(node cluster.Node) error {
	if credentials == nil || credentials.identity.NodeID != node.NodeID || credentials.identity.Incarnation != node.Incarnation {
		return errors.New("internal RPC certificate identity does not match configured node identity")
	}
	return nil
}

func IdentityURI(nodeID, incarnation string) (*url.URL, error) {
	if err := cluster.ValidateProcessIdentity(nodeID, incarnation); err != nil {
		return nil, err
	}
	return &url.URL{
		Scheme: "spiffe",
		Host:   nodeIdentityTrustDomain,
		Path:   "/node/" + nodeID + "/incarnation/" + incarnation,
	}, nil
}

func IdentityFromCertificate(certificate *x509.Certificate) (PeerIdentity, error) {
	if certificate == nil {
		return PeerIdentity{}, errors.New("internal RPC peer certificate is missing")
	}
	var identity *PeerIdentity
	for _, candidate := range certificate.URIs {
		parsed, ok := parseIdentityURI(candidate)
		if !ok {
			continue
		}
		if identity != nil {
			return PeerIdentity{}, errors.New("internal RPC certificate contains multiple node identities")
		}
		identity = &parsed
	}
	if identity == nil {
		return PeerIdentity{}, errors.New("internal RPC certificate does not contain a valid node identity URI SAN")
	}
	return *identity, nil
}

func parseIdentityURI(identityURI *url.URL) (PeerIdentity, bool) {
	if identityURI == nil || identityURI.Scheme != "spiffe" || identityURI.Host != nodeIdentityTrustDomain ||
		identityURI.User != nil || identityURI.RawQuery != "" || identityURI.Fragment != "" || identityURI.Opaque != "" || identityURI.RawPath != "" {
		return PeerIdentity{}, false
	}
	parts := strings.Split(strings.TrimPrefix(identityURI.Path, "/"), "/")
	if len(parts) != 4 || parts[0] != "node" || parts[2] != "incarnation" {
		return PeerIdentity{}, false
	}
	if err := cluster.ValidateProcessIdentity(parts[1], parts[3]); err != nil {
		return PeerIdentity{}, false
	}
	canonical, err := IdentityURI(parts[1], parts[3])
	if err != nil || canonical.String() != identityURI.String() {
		return PeerIdentity{}, false
	}
	return PeerIdentity{NodeID: parts[1], Incarnation: parts[3]}, true
}

func hasExtKeyUsage(certificate *x509.Certificate, required x509.ExtKeyUsage) bool {
	for _, usage := range certificate.ExtKeyUsage {
		if usage == required || usage == x509.ExtKeyUsageAny {
			return true
		}
	}
	return false
}

func (credentials *Credentials) ServerConfig() *tls.Config {
	return &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{credentials.certificate},
		ClientCAs:    credentials.roots,
		ClientAuth:   tls.RequireAndVerifyClientCert,
	}
}

func (credentials *Credentials) ClientConfig(serverName string) *tls.Config {
	return &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{credentials.certificate},
		RootCAs:      credentials.roots,
		ServerName:   serverName,
	}
}
