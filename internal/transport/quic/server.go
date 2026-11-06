package quictransport

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"sync"
	"time"

	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/authentication"
	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/cluster"
	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/protocol"
	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/room"
	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/storage"
	"go.opentelemetry.io/otel"

	quic "github.com/quic-go/quic-go"
)

const ALPN = "collab-editor-v1"

type Config struct {
	Address            string
	TLSConfig          *tls.Config
	HandshakeTimeout   time.Duration
	IdleTimeout        time.Duration
	KeepAlivePeriod    time.Duration
	AuthTimeout        time.Duration
	ReadTimeout        time.Duration
	WriteTimeout       time.Duration
	OperationTimeout   time.Duration
	MaxMessageSize     uint32
	SendQueueSize      int
	MaxConnections     int
	ConnectionRegistry *authentication.ConnectionRegistry
	OwnerResolver      cluster.OwnerResolver
	LocalNode          cluster.Node
	Observer           Observer
}

type Observer interface {
	QUICConnectionOpened()
	QUICConnectionClosed()
	QUICEvent(event, result string)
}

func DefaultConfig() Config {
	return Config{
		Address:          ":8443",
		HandshakeTimeout: 5 * time.Second,
		IdleTimeout:      60 * time.Second,
		KeepAlivePeriod:  20 * time.Second,
		AuthTimeout:      10 * time.Second,
		ReadTimeout:      2 * time.Minute,
		WriteTimeout:     10 * time.Second,
		OperationTimeout: 10 * time.Second,
		MaxMessageSize:   1024 * 1024,
		SendQueueSize:    32,
		MaxConnections:   1000,
	}
}

func LoadServerTLSConfig(certFile, keyFile, caFile string) (*tls.Config, error) {
	certificate, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, fmt.Errorf("load QUIC TLS certificate: %w", err)
	}
	caPool, err := loadCAPool(caFile)
	if err != nil {
		return nil, err
	}
	if len(certificate.Certificate) == 0 {
		return nil, errors.New("QUIC TLS certificate chain is empty")
	}
	leaf, err := x509.ParseCertificate(certificate.Certificate[0])
	if err != nil {
		return nil, fmt.Errorf("parse QUIC TLS certificate: %w", err)
	}
	now := time.Now()
	if now.Before(leaf.NotBefore) || now.After(leaf.NotAfter) {
		return nil, errors.New("QUIC TLS certificate is not currently valid")
	}
	if !certificateSupportsUsage(leaf, x509.ExtKeyUsageServerAuth) {
		return nil, errors.New("QUIC TLS certificate does not permit server authentication")
	}
	certificate.Leaf = leaf
	return &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{certificate},
		ClientCAs:    caPool,
		NextProtos:   []string{ALPN},
	}, nil
}

func certificateSupportsUsage(certificate *x509.Certificate, required x509.ExtKeyUsage) bool {
	if len(certificate.ExtKeyUsage) == 0 {
		return true
	}
	for _, usage := range certificate.ExtKeyUsage {
		if usage == required || usage == x509.ExtKeyUsageAny {
			return true
		}
	}
	return false
}

func LoadClientTLSConfig(caFile, serverName string) (*tls.Config, error) {
	if serverName == "" {
		return nil, errors.New("QUIC TLS server name is required")
	}
	caPool, err := loadCAPool(caFile)
	if err != nil {
		return nil, err
	}
	return &tls.Config{
		MinVersion: tls.VersionTLS13,
		RootCAs:    caPool,
		ServerName: serverName,
		NextProtos: []string{ALPN},
	}, nil
}

func loadCAPool(caFile string) (*x509.CertPool, error) {
	if caFile == "" {
		return nil, errors.New("QUIC CA file is required")
	}
	pemData, err := os.ReadFile(caFile)
	if err != nil {
		return nil, fmt.Errorf("read QUIC CA file: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pemData) {
		return nil, errors.New("QUIC CA file contains no valid certificates")
	}
	return pool, nil
}

type Server struct {
	listener      *quic.Listener
	manager       *room.Manager
	authenticator authentication.Authenticator
	config        Config

	mu          sync.Mutex
	clients     map[*Client]struct{}
	closing     bool
	connections int
	wait        sync.WaitGroup
}

func Listen(config Config, manager *room.Manager, authenticator authentication.Authenticator) (*Server, error) {
	if manager == nil || authenticator == nil {
		return nil, errors.New("QUIC manager and authenticator are required")
	}
	applyConfigDefaults(&config)
	if config.TLSConfig == nil {
		return nil, errors.New("QUIC TLS config is required")
	}
	tlsConfig := config.TLSConfig.Clone()
	tlsConfig.MinVersion = tls.VersionTLS13
	tlsConfig.NextProtos = []string{ALPN}
	listener, err := quic.ListenAddr(config.Address, tlsConfig, &quic.Config{
		HandshakeIdleTimeout:  config.HandshakeTimeout,
		MaxIdleTimeout:        config.IdleTimeout,
		KeepAlivePeriod:       config.KeepAlivePeriod,
		MaxIncomingStreams:    1,
		MaxIncomingUniStreams: -1,
		EnableDatagrams:       false,
	})
	if err != nil {
		return nil, fmt.Errorf("listen QUIC: %w", err)
	}
	return &Server{
		listener: listener, manager: manager, authenticator: authenticator,
		config: config, clients: make(map[*Client]struct{}),
	}, nil
}

func applyConfigDefaults(config *Config) {
	defaults := DefaultConfig()
	if config.Address == "" {
		config.Address = defaults.Address
	}
	if config.HandshakeTimeout <= 0 {
		config.HandshakeTimeout = defaults.HandshakeTimeout
	}
	if config.IdleTimeout <= 0 {
		config.IdleTimeout = defaults.IdleTimeout
	}
	if config.KeepAlivePeriod <= 0 {
		config.KeepAlivePeriod = defaults.KeepAlivePeriod
	}
	if config.AuthTimeout <= 0 {
		config.AuthTimeout = defaults.AuthTimeout
	}
	if config.ReadTimeout <= 0 {
		config.ReadTimeout = defaults.ReadTimeout
	}
	if config.WriteTimeout <= 0 {
		config.WriteTimeout = defaults.WriteTimeout
	}
	if config.OperationTimeout <= 0 {
		config.OperationTimeout = defaults.OperationTimeout
	}
	if config.MaxMessageSize == 0 {
		config.MaxMessageSize = defaults.MaxMessageSize
	}
	if config.SendQueueSize <= 0 {
		config.SendQueueSize = defaults.SendQueueSize
	}
	if config.MaxConnections <= 0 {
		config.MaxConnections = defaults.MaxConnections
	}
}

func (s *Server) Addr() net.Addr { return s.listener.Addr() }

func (s *Server) Serve(ctx context.Context) error {
	for {
		connection, err := s.listener.Accept(ctx)
		if err != nil {
			if ctx.Err() != nil || s.isClosing() {
				return nil
			}
			if s.config.Observer != nil {
				s.config.Observer.QUICEvent("accept", "error")
			}
			return fmt.Errorf("accept QUIC connection: %w", err)
		}
		if !s.reserveConnection() {
			_ = connection.CloseWithError(applicationCloseCode, "connection capacity reached")
			if s.config.Observer != nil {
				s.config.Observer.QUICEvent("connection", "rejected")
			}
			continue
		}
		s.mu.Lock()
		s.wait.Add(1)
		s.mu.Unlock()
		go func() {
			defer s.wait.Done()
			defer s.releaseConnection()
			s.handleConnection(connection)
		}()
	}
}

func (s *Server) reserveConnection() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closing || s.connections >= s.config.MaxConnections {
		return false
	}
	s.connections++
	return true
}

func (s *Server) releaseConnection() {
	s.mu.Lock()
	if s.connections > 0 {
		s.connections--
	}
	s.mu.Unlock()
}

func (s *Server) handleConnection(connection quic.Connection) {
	_, span := otel.Tracer("collab-editor/quic").Start(connection.Context(), "quic.connection")
	defer span.End()
	stream, err := s.acceptCollaborationStream(connection)
	if err != nil {
		_ = connection.CloseWithError(applicationCloseCode, "authentication stream required")
		return
	}
	principal, ok := s.authenticateStream(connection, stream)
	if !ok {
		return
	}
	initial, targetRoom, ok := s.selectDocument(connection, stream)
	if !ok {
		return
	}
	s.runClient(connection, stream, targetRoom, initial, principal)
}

func (s *Server) acceptCollaborationStream(connection quic.Connection) (quic.Stream, error) {
	ctx, cancel := context.WithTimeout(connection.Context(), s.config.AuthTimeout)
	defer cancel()
	return connection.AcceptStream(ctx)
}

func (s *Server) authenticateStream(connection quic.Connection, stream quic.Stream) (authentication.Principal, bool) {
	_ = stream.SetReadDeadline(time.Now().Add(s.config.AuthTimeout))
	authMessage, err := readMessage(stream, s.config.MaxMessageSize)
	if err != nil || authMessage.Type != protocol.TypeAuth || authMessage.Token == "" {
		s.writeHandshakeError(stream, protocol.CodeBadMessage, "AUTH with a bearer token is required")
		_ = connection.CloseWithError(applicationCloseCode, "invalid authentication message")
		return authentication.Principal{}, false
	}
	ctx, cancel := context.WithTimeout(connection.Context(), s.config.AuthTimeout)
	principal, err := s.authenticator.AuthenticateToken(ctx, authMessage.Token)
	cancel()
	if err != nil || !principal.Trusted {
		s.writeHandshakeError(stream, protocol.CodePermissionDenied, "invalid or inactive session")
		_ = connection.CloseWithError(applicationCloseCode, "authentication failed")
		return authentication.Principal{}, false
	}
	_ = stream.SetWriteDeadline(time.Now().Add(s.config.WriteTimeout))
	if err := writeMessage(stream, protocol.Message{Type: protocol.TypeAuthOK}, s.config.MaxMessageSize); err != nil {
		_ = connection.CloseWithError(applicationCloseCode, "could not send authentication response")
		return authentication.Principal{}, false
	}
	return principal, true
}

func (s *Server) selectDocument(connection quic.Connection, stream quic.Stream) (protocol.Message, *room.Room, bool) {
	_ = stream.SetReadDeadline(time.Now().Add(s.config.ReadTimeout))
	initial, err := readMessage(stream, s.config.MaxMessageSize)
	if err != nil || (initial.Type != protocol.TypeJoin && initial.Type != protocol.TypeResume) || initial.DocID == "" {
		s.writeHandshakeError(stream, protocol.CodeBadMessage, "JOIN or RESUME with doc_id is required after AUTH")
		_ = connection.CloseWithError(applicationCloseCode, "document selection required")
		return protocol.Message{}, nil, false
	}
	if s.config.OwnerResolver != nil {
		owner, resolveErr := s.config.OwnerResolver.EnsureOwner(connection.Context(), initial.DocID)
		if resolveErr != nil {
			s.writeHandshakeError(stream, protocol.CodeServiceUnavailable, "document owner is unavailable")
			_ = connection.CloseWithError(applicationCloseCode, "document owner is unavailable")
			return protocol.Message{}, nil, false
		}
		if !s.isLocalOwner(owner) {
			if s.config.Observer != nil {
				s.config.Observer.QUICEvent("redirect", "success")
			}
			slog.InfoContext(connection.Context(), "redirecting to document owner", "component", "quic", "event", "ownership_redirect", "doc_id", initial.DocID, "owner_node_id", owner.NodeID)
			_ = stream.SetWriteDeadline(time.Now().Add(s.config.WriteTimeout))
			if err := writeMessage(stream, protocol.Message{Type: protocol.TypeOwnerRedirect, DocID: initial.DocID, Endpoint: owner.QUICEndpoint}, s.config.MaxMessageSize); err != nil {
				_ = connection.CloseWithError(applicationCloseCode, "could not send owner redirect")
				return protocol.Message{}, nil, false
			}
			// Closing the sending side of the ordered stream lets QUIC deliver the
			// complete redirect frame before the client closes the connection.
			// A connection-level CloseWithError here can race the frame under load.
			_ = stream.Close()
			return protocol.Message{}, nil, false
		}
	}
	targetRoom, err := s.manager.Room(initial.DocID)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			s.writeHandshakeError(stream, protocol.CodeBadMessage, "document not found")
		} else {
			s.writeHandshakeError(stream, protocol.CodeInternalError, "could not open document")
		}
		_ = connection.CloseWithError(applicationCloseCode, "could not open document")
		return protocol.Message{}, nil, false
	}
	if s.config.OwnerResolver != nil {
		owner, resolveErr := s.config.OwnerResolver.Resolve(connection.Context(), initial.DocID)
		if resolveErr != nil || owner.State != cluster.OwnerActive || !s.isLocalOwner(owner) {
			s.writeHandshakeError(stream, protocol.CodeServiceUnavailable, "document owner is not active")
			_ = connection.CloseWithError(applicationCloseCode, "document owner is not active")
			return protocol.Message{}, nil, false
		}
	}
	return initial, targetRoom, true
}

func (s *Server) isLocalOwner(owner cluster.Owner) bool {
	return owner.NodeID == s.config.LocalNode.NodeID && owner.Incarnation == s.config.LocalNode.Incarnation
}

func (s *Server) runClient(connection quic.Connection, stream quic.Stream, targetRoom *room.Room, initial protocol.Message, principal authentication.Principal) {
	client := newClient(connection, stream, targetRoom, initial.DocID, principal, s.authenticator, s.config)
	if s.config.ConnectionRegistry != nil {
		client.unregister = s.config.ConnectionRegistry.Register(principal.SessionID, client, func() {
			client.Close(authentication.ErrSessionInvalid)
		})
	}
	if !s.addClient(client) {
		if client.unregister != nil {
			client.unregister()
		}
		client.Close(errors.New("server is shutting down"))
		return
	}
	defer s.removeClient(client)
	client.run(initial)
}

func (s *Server) writeHandshakeError(stream quic.Stream, code, message string) {
	_ = stream.SetWriteDeadline(time.Now().Add(s.config.WriteTimeout))
	_ = writeMessage(stream, protocol.Error(code, message), s.config.MaxMessageSize)
}

func (s *Server) addClient(client *Client) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closing {
		return false
	}
	s.clients[client] = struct{}{}
	if s.config.Observer != nil {
		s.config.Observer.QUICConnectionOpened()
		s.config.Observer.QUICEvent("connection", "opened")
	}
	return true
}

func (s *Server) removeClient(client *Client) {
	s.mu.Lock()
	_, existed := s.clients[client]
	delete(s.clients, client)
	s.mu.Unlock()
	if existed && s.config.Observer != nil {
		s.config.Observer.QUICConnectionClosed()
		s.config.Observer.QUICEvent("connection", "closed")
	}
}

func (s *Server) isClosing() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closing
}

func (s *Server) Close(ctx context.Context) error {
	s.mu.Lock()
	if !s.closing {
		s.closing = true
		_ = s.listener.Close()
	}
	clients := make([]*Client, 0, len(s.clients))
	for client := range s.clients {
		clients = append(clients, client)
	}
	s.mu.Unlock()
	for _, client := range clients {
		client.Close(errors.New("server shutdown"))
	}
	done := make(chan struct{})
	go func() {
		s.wait.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
