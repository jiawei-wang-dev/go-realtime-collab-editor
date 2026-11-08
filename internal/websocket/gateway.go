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
	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/transport"
)

const defaultMaxMessageSize = 1 << 20
const cursorMessagesPerSecond = 30

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

type Gateway struct {
	config Config
}

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
	// The API's exact-origin middleware runs before this handler. Disabling the
	// library's host-only check avoids applying a second, different policy.
	connection, err := websocket.Accept(writer, request, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		return
	}
	connection.SetReadLimit(gateway.config.MaxMessageSize)
	client := &client{connection: connection, config: gateway.config, send: make(chan protocol.Message, gateway.config.SendQueueSize), writeDone: make(chan struct{})}
	client.run(request.Context())
}

type client struct {
	connection     *websocket.Conn
	config         Config
	principal      authentication.Principal
	room           *room.Room
	docID          string
	send           chan protocol.Message
	writeDone      chan struct{}
	unregister     func()
	mu             sync.RWMutex
	closed         bool
	joined         bool
	cursorWindow   time.Time
	cursorMessages int
}

var _ transport.Client = (*client)(nil)

func (client *client) Identity() string       { return client.principal.UserID }
func (client *client) SupportsPresence() bool { return true }
func (client *client) SupportsCursor() bool   { return true }
func (client *client) DisplayName() string    { return client.principal.Username }

func (client *client) TrySend(message protocol.Message) bool {
	client.mu.RLock()
	defer client.mu.RUnlock()
	if client.closed {
		return false
	}
	select {
	case client.send <- message:
		return true
	default:
		return false
	}
}

func (client *client) Close(reason error) {
	client.mu.Lock()
	if client.closed {
		client.mu.Unlock()
		return
	}
	client.closed = true
	close(client.send)
	client.mu.Unlock()
	// transport.Client.Close must not block the Room or session revocation path
	// while waiting for a peer close handshake.
	_ = reason
	client.connection.CloseNow()
}

func (client *client) run(parent context.Context) {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	go client.writeLoop(ctx)
	defer func() {
		if client.joined && client.room != nil {
			leaveCtx, leaveCancel := context.WithTimeout(context.Background(), client.config.OperationTimeout)
			_ = client.room.LeaveContext(leaveCtx, client)
			leaveCancel()
		}
		if client.unregister != nil {
			client.unregister()
		}
		client.Close(nil)
		<-client.writeDone
	}()

	if !client.authenticate(ctx) {
		return
	}
	for {
		var message protocol.Message
		if err := wsjson.Read(ctx, client.connection, &message); err != nil {
			return
		}
		if !client.process(message) {
			return
		}
	}
}

func (client *client) authenticate(parent context.Context) bool {
	ctx, cancel := context.WithTimeout(parent, client.config.AuthTimeout)
	defer cancel()
	var message protocol.Message
	if err := wsjson.Read(ctx, client.connection, &message); err != nil || message.Type != protocol.TypeAuth || message.Token == "" {
		client.sendError(protocol.CodeBadMessage, "AUTH with a bearer token is required")
		return false
	}
	principal, err := client.config.Authenticator.AuthenticateToken(ctx, message.Token)
	if err != nil || !principal.Trusted {
		client.sendError(protocol.CodePermissionDenied, "invalid or inactive session")
		return false
	}
	client.principal = principal
	if client.config.ConnectionRegistry != nil {
		client.unregister = client.config.ConnectionRegistry.Register(principal.SessionID, client, func() { client.Close(authentication.ErrSessionInvalid) })
	}
	return client.TrySend(protocol.Message{Type: protocol.TypeAuthOK})
}

func (client *client) process(message protocol.Message) bool {
	ctx, cancel := context.WithTimeout(context.Background(), client.config.OperationTimeout)
	defer cancel()
	release := func() {}
	var err error
	if guarded, ok := client.config.Authenticator.(authentication.PrincipalUseAuthenticator); ok {
		release, err = guarded.AcquirePrincipal(ctx, client.principal)
	} else {
		err = client.config.Authenticator.ValidatePrincipal(ctx, client.principal)
	}
	if err != nil {
		client.sendError(protocol.CodePermissionDenied, "session is not active")
		return false
	}
	defer release()

	switch message.Type {
	case protocol.TypeJoin, protocol.TypeResume:
		if client.joined || message.DocID == "" {
			client.sendError(protocol.CodeBadMessage, "a valid document may only be selected once")
			return true
		}
		if client.config.OwnerResolver != nil {
			owner, ownerErr := client.config.OwnerResolver.EnsureOwner(ctx, message.DocID)
			if ownerErr != nil || owner.State != cluster.OwnerActive {
				client.sendError(protocol.CodeServiceUnavailable, "document owner is unavailable")
				return false
			}
			if owner.NodeID != client.config.LocalNode.NodeID || owner.Incarnation != client.config.LocalNode.Incarnation {
				if client.config.NodeDirectory == nil {
					client.sendError(protocol.CodeServiceUnavailable, "document owner is unavailable")
					return false
				}
				node, found := client.config.NodeDirectory.Lookup(owner.NodeID, owner.Incarnation)
				if !found || !node.IsReady() || node.BrowserEndpoint == "" {
					client.sendError(protocol.CodeServiceUnavailable, "document owner is unavailable")
					return false
				}
				writeCtx, writeCancel := context.WithTimeout(ctx, client.config.WriteTimeout)
				_ = wsjson.Write(writeCtx, client.connection, protocol.Message{Type: protocol.TypeOwnerRedirect, DocID: message.DocID, Endpoint: node.BrowserEndpoint})
				writeCancel()
				return false
			}
		}
		target, openErr := client.config.Manager.Room(message.DocID)
		if openErr != nil {
			client.sendRoomError(openErr, "could not open document")
			return false
		}
		client.room, client.docID = target, message.DocID
		if message.Type == protocol.TypeJoin {
			err = target.JoinContext(ctx, client)
		} else {
			err = target.ResumeContext(ctx, client, message.LastVersion)
		}
		if err != nil {
			client.sendRoomError(err, "could not join document")
			return !errors.Is(err, room.ErrSlowClient)
		}
		client.joined = true
	case protocol.TypeEdit:
		if !client.joined || message.DocID != client.docID || message.RequestID == "" || message.Operation == nil {
			client.sendError(protocol.CodeBadMessage, "joined document, request_id, and operation are required")
			return true
		}
		operation := *message.Operation
		operation.BaseVersion = message.BaseVersion
		_ = client.room.SubmitEditContext(ctx, client, message.RequestID, operation)
	case protocol.TypeCursorUpdate:
		if !client.joined || message.DocID != client.docID || message.Cursor == nil {
			client.sendError(protocol.CodeBadMessage, "joined document and cursor are required")
			return true
		}
		if !client.allowCursorMessage() {
			return true
		}
		if err := client.room.SubmitCursorContext(ctx, client, *message.Cursor); err != nil {
			var coded *room.CodedError
			if errors.As(err, &coded) && coded.Code == protocol.CodeBadMessage {
				// Cursor presence is ephemeral. A cursor can become stale while an
				// edit acknowledgement and Monaco selection event cross in flight;
				// discard it without failing the document session.
				return true
			}
			client.sendRoomError(err, "invalid cursor update")
		}
	default:
		client.sendError(protocol.CodeBadMessage, "unsupported message type")
	}
	return true
}

func (client *client) allowCursorMessage() bool {
	now := time.Now()
	if client.cursorWindow.IsZero() || now.Sub(client.cursorWindow) >= time.Second {
		client.cursorWindow = now
		client.cursorMessages = 0
	}
	client.cursorMessages++
	return client.cursorMessages <= cursorMessagesPerSecond
}

func (client *client) writeLoop(ctx context.Context) {
	defer close(client.writeDone)
	for {
		select {
		case <-ctx.Done():
			return
		case message, ok := <-client.send:
			if !ok {
				return
			}
			writeCtx, cancel := context.WithTimeout(ctx, client.config.WriteTimeout)
			err := wsjson.Write(writeCtx, client.connection, message)
			cancel()
			if err != nil {
				client.Close(err)
				return
			}
		}
	}
}

func (client *client) sendRoomError(err error, fallback string) {
	var coded *room.CodedError
	if errors.As(err, &coded) {
		client.sendError(coded.Code, coded.Message)
	} else if !errors.Is(err, room.ErrSlowClient) {
		client.sendError(protocol.CodeInternalError, fallback)
	}
}

func (client *client) sendError(code, message string) {
	if !client.TrySend(protocol.Error(code, message)) {
		client.Close(room.ErrSlowClient)
	}
}
