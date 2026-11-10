package websocket

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/authentication"
	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/cluster"
	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/protocol"
	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/room"
	browsertransport "github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/transport/browser"
)

const defaultMaxMessageSize = 1 << 20

type Config struct {
	Manager            *room.Manager
	Authenticator      authentication.Authenticator
	ConnectionRegistry *authentication.ConnectionRegistry
	AllowedOrigins     []string
	AuthTimeout        time.Duration
	OperationTimeout   time.Duration
	WriteTimeout       time.Duration
	SendQueueSize      int
	MaxMessageSize     int64
	OwnerResolver      cluster.OwnerResolver
	LocalNode          cluster.Node
	NodeDirectory      cluster.NodeDirectory
}

type Gateway struct{ config Config }

func New(config Config) (*Gateway, error) {
	if config.Manager == nil || config.Authenticator == nil {
		return nil, errors.New("websocket manager and authenticator are required")
	}
	if config.AuthTimeout <= 0 {
		config.AuthTimeout = 10 * time.Second
	}
	if config.OperationTimeout <= 0 {
		config.OperationTimeout = 10 * time.Second
	}
	if config.WriteTimeout <= 0 {
		config.WriteTimeout = 10 * time.Second
	}
	if config.SendQueueSize <= 0 {
		config.SendQueueSize = 32
	}
	if config.MaxMessageSize <= 0 {
		config.MaxMessageSize = defaultMaxMessageSize
	}
	return &Gateway{config: config}, nil
}

func (gateway *Gateway) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	connection, err := websocket.Accept(writer, request, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		return
	}
	connection.SetReadLimit(gateway.config.MaxMessageSize)
	browsertransport.Serve(request.Context(), &websocketConn{connection: connection}, browsertransport.Config{
		Manager: gateway.config.Manager, Authenticator: gateway.config.Authenticator,
		ConnectionRegistry: gateway.config.ConnectionRegistry, AuthTimeout: gateway.config.AuthTimeout,
		OperationTimeout: gateway.config.OperationTimeout, WriteTimeout: gateway.config.WriteTimeout,
		SendQueueSize: gateway.config.SendQueueSize, OwnerResolver: gateway.config.OwnerResolver,
		LocalNode: gateway.config.LocalNode, NodeDirectory: gateway.config.NodeDirectory,
	})
}

type websocketConn struct {
	connection *websocket.Conn
	writeMu    sync.Mutex
}

func (connection *websocketConn) Read(ctx context.Context, message *protocol.Message) error {
	return wsjson.Read(ctx, connection.connection, message)
}

func (connection *websocketConn) Write(ctx context.Context, message protocol.Message) error {
	connection.writeMu.Lock()
	defer connection.writeMu.Unlock()
	return wsjson.Write(ctx, connection.connection, message)
}

func (connection *websocketConn) CloseNow() error { return connection.connection.CloseNow() }
