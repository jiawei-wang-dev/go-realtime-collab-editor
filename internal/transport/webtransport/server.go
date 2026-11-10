package webtransport

import (
	"context"
	"crypto/tls"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/protocol"
	browsertransport "github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/transport/browser"
	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"
	webtransportgo "github.com/quic-go/webtransport-go"
)

const Path = "/api/webtransport"

type Config struct {
	Address        string
	TLSConfig      *tls.Config
	AllowedOrigins []string
	Session        browsertransport.Config
	StreamTimeout  time.Duration
	IdleTimeout    time.Duration
	MaxMessageSize uint32
}

type Server struct {
	config   Config
	packet   net.PacketConn
	server   *webtransportgo.Server
	mu       sync.Mutex
	sessions map[*webtransportgo.Session]struct{}
	close    sync.Once
}

func Listen(config Config) (*Server, error) {
	if config.Address == "" || config.TLSConfig == nil {
		return nil, errors.New("webtransport address and TLS configuration are required")
	}
	if err := config.Session.Validate(); err != nil {
		return nil, err
	}
	if config.StreamTimeout <= 0 || config.IdleTimeout <= 0 || config.MaxMessageSize == 0 {
		return nil, errors.New("webtransport timeouts and message size must be positive")
	}
	udpAddress, err := net.ResolveUDPAddr("udp", config.Address)
	if err != nil {
		return nil, fmt.Errorf("resolve webtransport address: %w", err)
	}
	packet, err := net.ListenUDP("udp", udpAddress)
	if err != nil {
		return nil, fmt.Errorf("listen webtransport: %w", err)
	}
	server := &Server{config: config, packet: packet, sessions: make(map[*webtransportgo.Session]struct{})}
	mux := http.NewServeMux()
	mux.HandleFunc(Path, server.handle)
	tlsConfig := config.TLSConfig.Clone()
	tlsConfig.MinVersion = tls.VersionTLS13
	tlsConfig.NextProtos = []string{http3.NextProtoH3}
	server.server = &webtransportgo.Server{
		H3: &http3.Server{
			TLSConfig: tlsConfig,
			Handler:   mux,
			QUICConfig: &quic.Config{
				MaxIdleTimeout:  config.IdleTimeout,
				KeepAlivePeriod: config.IdleTimeout / 3,
			},
		},
		CheckOrigin: server.allowOrigin,
	}
	return server, nil
}

func (server *Server) Addr() net.Addr { return server.packet.LocalAddr() }

func (server *Server) Serve(ctx context.Context) error {
	go func() {
		<-ctx.Done()
		_ = server.Close(context.Background())
	}()
	err := server.server.Serve(server.packet)
	if errors.Is(err, context.Canceled) || errors.Is(err, http.ErrServerClosed) || errors.Is(err, net.ErrClosed) {
		return nil
	}
	return err
}

func (server *Server) Close(context.Context) error {
	var err error
	server.close.Do(func() {
		server.mu.Lock()
		sessions := make([]*webtransportgo.Session, 0, len(server.sessions))
		for session := range server.sessions {
			sessions = append(sessions, session)
		}
		server.sessions = nil
		server.mu.Unlock()
		for _, session := range sessions {
			_ = session.CloseWithError(0, "server shutdown")
		}
		err = errors.Join(server.server.Close(), server.packet.Close())
	})
	return err
}

func (server *Server) allowOrigin(request *http.Request) bool {
	origin := request.Header.Get("Origin")
	if origin == "" {
		return false
	}
	for _, allowed := range server.config.AllowedOrigins {
		if origin == allowed {
			return true
		}
	}
	return false
}

func (server *Server) handle(writer http.ResponseWriter, request *http.Request) {
	session, err := server.server.Upgrade(writer, request)
	if err != nil {
		slog.Warn("webtransport upgrade rejected", "component", "webtransport", "event", "upgrade_rejected", "origin", request.Header.Get("Origin"), "remote_addr", request.RemoteAddr, "error", err)
		http.Error(writer, "webtransport upgrade rejected", http.StatusBadRequest)
		return
	}
	slog.Info("webtransport session accepted", "component", "webtransport", "event", "session_accepted", "origin", request.Header.Get("Origin"), "remote_addr", request.RemoteAddr)
	server.mu.Lock()
	if server.sessions == nil {
		server.mu.Unlock()
		_ = session.CloseWithError(0, "server shutdown")
		return
	}
	server.sessions[session] = struct{}{}
	server.mu.Unlock()
	defer func() {
		server.mu.Lock()
		delete(server.sessions, session)
		server.mu.Unlock()
		_ = session.CloseWithError(0, "session complete")
	}()

	streamContext, cancel := context.WithTimeout(session.Context(), server.config.StreamTimeout)
	stream, err := session.AcceptStream(streamContext)
	cancel()
	if err != nil {
		return
	}
	connection := &streamConnection{session: session, stream: stream, maxMessageSize: server.config.MaxMessageSize}
	browsertransport.Serve(session.Context(), connection, server.config.Session)
}

type streamConnection struct {
	session        *webtransportgo.Session
	stream         *webtransportgo.Stream
	maxMessageSize uint32
	writeMu        sync.Mutex
	closeOnce      sync.Once
}

func (connection *streamConnection) Read(ctx context.Context, message *protocol.Message) error {
	if deadline, ok := ctx.Deadline(); ok {
		_ = connection.stream.SetReadDeadline(deadline)
	} else {
		_ = connection.stream.SetReadDeadline(time.Time{})
	}
	var header [4]byte
	if _, err := io.ReadFull(connection.stream, header[:]); err != nil {
		return err
	}
	length := binary.BigEndian.Uint32(header[:])
	if length == 0 || length > connection.maxMessageSize {
		return fmt.Errorf("webtransport message size %d is invalid", length)
	}
	payload := make([]byte, length)
	if _, err := io.ReadFull(connection.stream, payload); err != nil {
		return err
	}
	if err := json.Unmarshal(payload, message); err != nil {
		return fmt.Errorf("decode webtransport message: %w", err)
	}
	return nil
}

func (connection *streamConnection) Write(ctx context.Context, message protocol.Message) error {
	payload, err := json.Marshal(message)
	if err != nil {
		return err
	}
	if len(payload) == 0 || len(payload) > int(connection.maxMessageSize) {
		return fmt.Errorf("webtransport message size %d is invalid", len(payload))
	}
	connection.writeMu.Lock()
	defer connection.writeMu.Unlock()
	if deadline, ok := ctx.Deadline(); ok {
		_ = connection.stream.SetWriteDeadline(deadline)
	} else {
		_ = connection.stream.SetWriteDeadline(time.Time{})
	}
	frame := make([]byte, 4+len(payload))
	binary.BigEndian.PutUint32(frame[:4], uint32(len(payload)))
	copy(frame[4:], payload)
	for len(frame) > 0 {
		written, writeErr := connection.stream.Write(frame)
		if writeErr != nil {
			return writeErr
		}
		if written == 0 {
			return io.ErrShortWrite
		}
		frame = frame[written:]
	}
	return nil
}

func (connection *streamConnection) CloseNow() error {
	var err error
	connection.closeOnce.Do(func() {
		connection.stream.CancelRead(0)
		// Close the write side after the session writer has drained. Cancelling it
		// here discards a terminal protocol error that was already accepted by the
		// browser session (for example an authentication rejection).
		_ = connection.stream.Close()
		err = connection.session.CloseWithError(0, "session closed")
	})
	return err
}
