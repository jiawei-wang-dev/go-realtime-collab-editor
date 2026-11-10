package webtransport

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/binary"
	"encoding/json"
	"io"
	"math/big"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/authentication"
	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/collab"
	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/protocol"
	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/room"
	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/storage"
	browsertransport "github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/transport/browser"
	webtransportgo "github.com/quic-go/webtransport-go"
)

type integrationAuthenticator struct{}

func (integrationAuthenticator) AuthenticateToken(context.Context, string) (authentication.Principal, error) {
	return authentication.Principal{UserID: "owner", Username: "owner", SessionID: "session", Trusted: true}, nil
}
func (integrationAuthenticator) AuthenticateHTTP(*http.Request) (authentication.Principal, error) {
	return authentication.Principal{}, nil
}
func (integrationAuthenticator) ValidatePrincipal(context.Context, authentication.Principal) error {
	return nil
}

func TestRealWebTransportUsesSharedRoomProtocol(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	store := storage.NewMemory()
	if _, err := store.CreateDocument(ctx, "doc", "abc", "owner"); err != nil {
		t.Fatal(err)
	}
	manager := room.NewManager(store)
	t.Cleanup(func() { _ = manager.Shutdown(context.Background()) })
	registry := authentication.NewConnectionRegistry()
	server, err := Listen(Config{
		Address: "127.0.0.1:0", TLSConfig: integrationServerTLS(t),
		AllowedOrigins: []string{"https://editor.example.com"}, StreamTimeout: time.Second,
		IdleTimeout: 5 * time.Second, MaxMessageSize: 1 << 20,
		Session: browsertransport.Config{
			Manager: manager, Authenticator: integrationAuthenticator{}, ConnectionRegistry: registry,
			AuthTimeout: time.Second, OperationTimeout: time.Second, WriteTimeout: time.Second, SendQueueSize: 8,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.Serve(ctx) }()
	t.Cleanup(func() {
		_ = server.Close(context.Background())
		<-serveDone
	})

	dialer := &webtransportgo.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}
	t.Cleanup(func() { _ = dialer.Close() })
	header := http.Header{"Origin": []string{"https://editor.example.com"}}
	started := time.Now()
	defer func() { t.Logf("PERF_SAMPLE_NS=%d", time.Since(started).Nanoseconds()) }()
	response, session, err := dialer.Dial(ctx, "https://"+server.Addr().String()+Path, header)
	if err != nil {
		t.Fatalf("dial WebTransport: %v", err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("upgrade status = %d", response.StatusCode)
	}
	stream, err := session.OpenStreamSync(ctx)
	if err != nil {
		t.Fatal(err)
	}
	writeTestMessage(t, stream, protocol.Message{Type: protocol.TypeAuth, Token: "access"})
	if message := readTestMessage(t, stream); message.Type != protocol.TypeAuthOK {
		t.Fatalf("auth response = %#v", message)
	}
	writeTestMessage(t, stream, protocol.Message{Type: protocol.TypeJoin, DocID: "doc"})
	if message := readTestMessage(t, stream); message.Type != protocol.TypeJoined || message.Content != "abc" {
		t.Fatalf("join response = %#v", message)
	}
	if message := readTestMessage(t, stream); message.Type != protocol.TypePresenceChanged {
		t.Fatalf("presence response = %#v", message)
	}
	operation := collab.Operation{Kind: collab.OperationInsert, Position: 1, Text: "X"}
	writeTestMessage(t, stream, protocol.Message{Type: protocol.TypeEdit, DocID: "doc", RequestID: "wt-1", BaseVersion: 0, Operation: &operation})
	if message := readTestMessage(t, stream); message.Type != protocol.TypeEditApplied || message.Version != 1 {
		t.Fatalf("edit response = %#v", message)
	}
	registry.CloseSession("session")
	if _, err := stream.Read(make([]byte, 1)); err == nil {
		t.Fatal("revoked session kept WebTransport stream open")
	}
}

func writeTestMessage(t *testing.T, writer io.Writer, message protocol.Message) {
	t.Helper()
	payload, err := json.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}
	frame := make([]byte, 4+len(payload))
	binary.BigEndian.PutUint32(frame[:4], uint32(len(payload)))
	copy(frame[4:], payload)
	if _, err := writer.Write(frame); err != nil {
		t.Fatal(err)
	}
}

func readTestMessage(t *testing.T, reader io.Reader) protocol.Message {
	t.Helper()
	var header [4]byte
	if _, err := io.ReadFull(reader, header[:]); err != nil {
		t.Fatal(err)
	}
	payload := make([]byte, binary.BigEndian.Uint32(header[:]))
	if _, err := io.ReadFull(reader, payload); err != nil {
		t.Fatal(err)
	}
	var message protocol.Message
	if err := json.Unmarshal(payload, &message); err != nil {
		t.Fatal(err)
	}
	return message
}

func integrationServerTLS(t *testing.T) *tls.Config {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "localhost"},
		NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
	}
	certificateDER, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{{Certificate: [][]byte{certificateDER}, PrivateKey: key}}}
}
