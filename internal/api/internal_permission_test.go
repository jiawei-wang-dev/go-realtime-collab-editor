package api

import (
	"crypto/tls"
	"crypto/x509"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/cluster"
	clusterrpc "github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/cluster/rpc"
)

func TestCallingNodeIdentityComesFromCertificateNotHeaders(t *testing.T) {
	oldURI, err := clusterrpc.IdentityURI("node-a", "old-process")
	if err != nil {
		t.Fatal(err)
	}
	certificate := &x509.Certificate{URIs: []*url.URL{oldURI}}
	membership := cluster.NewMembership()
	membership.Put(cluster.Node{
		NodeID: "node-a", Incarnation: "new-process", QUICEndpoint: "localhost:8443",
		HTTPEndpoint: "https://localhost:9443", Status: cluster.NodeReady, StartedAt: time.Now(),
	}, 1)
	server := &Server{nodeDirectory: membership}
	request := httptest.NewRequest("GET", "/internal", nil)
	request.TLS = &tls.ConnectionState{
		PeerCertificates: []*x509.Certificate{certificate},
		VerifiedChains:   [][]*x509.Certificate{{certificate}},
	}
	request.Header.Set(clusterrpc.NodeIDHeader, "node-a")
	request.Header.Set(clusterrpc.IncarnationHeader, "new-process")
	response := httptest.NewRecorder()
	if server.authenticateCallingNode(response, request) {
		t.Fatal("old incarnation certificate impersonated the current process through headers")
	}
	if response.Code != 403 {
		t.Fatalf("status = %d", response.Code)
	}
}
