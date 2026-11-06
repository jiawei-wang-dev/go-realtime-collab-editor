package quictransport

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/protocol"

	quic "github.com/quic-go/quic-go"
)

type DialConfig struct {
	TLSConfig        *tls.Config
	HandshakeTimeout time.Duration
	IdleTimeout      time.Duration
	KeepAlivePeriod  time.Duration
	ReadTimeout      time.Duration
	WriteTimeout     time.Duration
	MaxMessageSize   uint32
}

type Connection struct {
	connection quic.Connection
	stream     quic.Stream
	config     DialConfig
	writeMu    sync.Mutex
	closeOnce  sync.Once
}

type ProtocolError struct {
	Code    string
	Message string
}

func (e *ProtocolError) Error() string {
	return fmt.Sprintf("server rejected request (%s): %s", e.Code, e.Message)
}

func Dial(ctx context.Context, address string, config DialConfig) (*Connection, error) {
	if config.TLSConfig == nil {
		return nil, errors.New("QUIC client TLS config is required")
	}
	applyDialConfigDefaults(&config)
	tlsConfig := config.TLSConfig.Clone()
	tlsConfig.MinVersion = tls.VersionTLS13
	tlsConfig.NextProtos = []string{ALPN}
	if tlsConfig.InsecureSkipVerify {
		return nil, errors.New("InsecureSkipVerify is not allowed for QUIC clients")
	}
	dialCtx, cancel := context.WithTimeout(ctx, config.HandshakeTimeout)
	defer cancel()
	connection, err := quic.DialAddr(dialCtx, address, tlsConfig, &quic.Config{
		HandshakeIdleTimeout:  config.HandshakeTimeout,
		MaxIdleTimeout:        config.IdleTimeout,
		KeepAlivePeriod:       config.KeepAlivePeriod,
		MaxIncomingStreams:    -1,
		MaxIncomingUniStreams: -1,
		EnableDatagrams:       false,
	})
	if err != nil {
		return nil, fmt.Errorf("dial QUIC: %w", err)
	}
	stream, err := connection.OpenStreamSync(dialCtx)
	if err != nil {
		_ = connection.CloseWithError(applicationCloseCode, "could not open collaboration stream")
		return nil, fmt.Errorf("open QUIC stream: %w", err)
	}
	return &Connection{connection: connection, stream: stream, config: config}, nil
}

func applyDialConfigDefaults(config *DialConfig) {
	defaults := DefaultConfig()
	if config.HandshakeTimeout <= 0 {
		config.HandshakeTimeout = defaults.HandshakeTimeout
	}
	if config.IdleTimeout <= 0 {
		config.IdleTimeout = defaults.IdleTimeout
	}
	if config.KeepAlivePeriod <= 0 {
		config.KeepAlivePeriod = defaults.KeepAlivePeriod
	}
	if config.ReadTimeout <= 0 {
		config.ReadTimeout = defaults.ReadTimeout
	}
	if config.WriteTimeout <= 0 {
		config.WriteTimeout = defaults.WriteTimeout
	}
	if config.MaxMessageSize == 0 {
		config.MaxMessageSize = defaults.MaxMessageSize
	}
}

func (c *Connection) Authenticate(token string) error {
	if token == "" {
		return errors.New("access token is required")
	}
	if err := c.Send(protocol.Message{Type: protocol.TypeAuth, Token: token}); err != nil {
		return err
	}
	message, err := c.Receive()
	if err != nil {
		return err
	}
	if message.Type == protocol.TypeError {
		return &ProtocolError{Code: message.Code, Message: message.Message}
	}
	if message.Type != protocol.TypeAuthOK {
		return fmt.Errorf("unexpected authentication response %q", message.Type)
	}
	return nil
}

func (c *Connection) Send(message protocol.Message) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	_ = c.stream.SetWriteDeadline(time.Now().Add(c.config.WriteTimeout))
	return writeMessage(c.stream, message, c.config.MaxMessageSize)
}

func (c *Connection) Receive() (protocol.Message, error) {
	_ = c.stream.SetReadDeadline(time.Now().Add(c.config.ReadTimeout))
	return readMessage(c.stream, c.config.MaxMessageSize)
}

func (c *Connection) State() quic.ConnectionState { return c.connection.ConnectionState() }

func (c *Connection) LocalAddr() string  { return c.connection.LocalAddr().String() }
func (c *Connection) RemoteAddr() string { return c.connection.RemoteAddr().String() }

func (c *Connection) Close() error {
	var err error
	c.closeOnce.Do(func() {
		c.stream.CancelRead(streamCloseCode)
		c.stream.CancelWrite(streamCloseCode)
		_ = c.stream.Close()
		err = c.connection.CloseWithError(0, "client closed")
	})
	return err
}
