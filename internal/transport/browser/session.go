// Package browser contains the transport-independent browser collaboration
// session. WebSocket and WebTransport adapters only provide framed messages;
// authentication, owner routing, Room access, recovery, and revocation live
// here so their behavior cannot drift between transports.
package browser

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/authentication"
	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/cluster"
	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/protocol"
	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/room"
	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/transport"
)

const cursorMessagesPerSecond = 30

type MessageConn interface {
	Read(context.Context, *protocol.Message) error
	Write(context.Context, protocol.Message) error
	CloseNow() error
}

type Config struct {
	Manager            *room.Manager
	Authenticator      authentication.Authenticator
	ConnectionRegistry *authentication.ConnectionRegistry
	AuthTimeout        time.Duration
	OperationTimeout   time.Duration
	WriteTimeout       time.Duration
	SendQueueSize      int
	OwnerResolver      cluster.OwnerResolver
	LocalNode          cluster.Node
	NodeDirectory      cluster.NodeDirectory
}

func (config Config) Validate() error {
	if config.Manager == nil || config.Authenticator == nil {
		return errors.New("browser realtime manager and authenticator are required")
	}
	if config.AuthTimeout <= 0 || config.OperationTimeout <= 0 || config.WriteTimeout <= 0 || config.SendQueueSize <= 0 {
		return errors.New("browser realtime timeouts and send queue size must be positive")
	}
	return nil
}

func Serve(parent context.Context, connection MessageConn, config Config) {
	client := &sessionClient{connection: connection, config: config, send: make(chan protocol.Message, config.SendQueueSize), writeDone: make(chan struct{})}
	client.run(parent)
}

type sessionClient struct {
	connection     MessageConn
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

var _ transport.Client = (*sessionClient)(nil)

func (client *sessionClient) Identity() string       { return client.principal.UserID }
func (client *sessionClient) SupportsPresence() bool { return true }
func (client *sessionClient) SupportsCursor() bool   { return true }
func (client *sessionClient) DisplayName() string    { return client.principal.Username }

func (client *sessionClient) TrySend(message protocol.Message) bool {
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

func (client *sessionClient) Close(reason error) {
	client.mu.Lock()
	if client.closed {
		client.mu.Unlock()
		return
	}
	client.closed = true
	close(client.send)
	client.mu.Unlock()
	_ = reason
	_ = client.connection.CloseNow()
}

func (client *sessionClient) run(parent context.Context) {
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
		client.finish()
	}()
	if !client.authenticate(ctx) {
		return
	}
	for {
		var message protocol.Message
		if err := client.connection.Read(ctx, &message); err != nil || !client.process(message) {
			return
		}
	}
}

// finish drains messages already accepted by the session before closing the
// transport. This preserves terminal protocol errors while Close remains the
// immediate path used for revocation, slow clients, and transport failures.
func (client *sessionClient) finish() {
	client.mu.Lock()
	if !client.closed {
		client.closed = true
		close(client.send)
	}
	client.mu.Unlock()
	<-client.writeDone
	_ = client.connection.CloseNow()
}

func (client *sessionClient) authenticate(parent context.Context) bool {
	ctx, cancel := context.WithTimeout(parent, client.config.AuthTimeout)
	defer cancel()
	var message protocol.Message
	if err := client.connection.Read(ctx, &message); err != nil || message.Type != protocol.TypeAuth || message.Token == "" {
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

func (client *sessionClient) process(message protocol.Message) bool {
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
		return client.selectDocument(ctx, message)
	case protocol.TypeEdit:
		if !client.joined || message.DocID != client.docID || message.RequestID == "" || message.Operation == nil {
			client.sendError(protocol.CodeBadMessage, "joined document, request_id, and operation are required")
			return true
		}
		operation := *message.Operation
		operation.BaseVersion = message.BaseVersion
		_ = client.room.SubmitEditContext(ctx, client, message.RequestID, operation)
	case protocol.TypeCursorUpdate:
		client.processCursor(ctx, message)
	default:
		client.sendError(protocol.CodeBadMessage, "unsupported message type")
	}
	return true
}

func (client *sessionClient) selectDocument(ctx context.Context, message protocol.Message) bool {
	if client.joined || message.DocID == "" {
		client.sendError(protocol.CodeBadMessage, "a valid document may only be selected once")
		return true
	}
	if client.config.OwnerResolver != nil {
		owner, err := client.config.OwnerResolver.EnsureOwner(ctx, message.DocID)
		if err != nil || owner.State != cluster.OwnerActive {
			client.sendError(protocol.CodeServiceUnavailable, "document owner is unavailable")
			return false
		}
		if owner.NodeID != client.config.LocalNode.NodeID || owner.Incarnation != client.config.LocalNode.Incarnation {
			return client.redirectToOwner(ctx, message.DocID, owner)
		}
	}
	target, err := client.config.Manager.Room(message.DocID)
	if err != nil {
		client.sendRoomError(err, "could not open document")
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
	return true
}

func (client *sessionClient) redirectToOwner(ctx context.Context, docID string, owner cluster.Owner) bool {
	if client.config.NodeDirectory == nil {
		client.sendError(protocol.CodeServiceUnavailable, "document owner is unavailable")
		return false
	}
	node, found := client.config.NodeDirectory.Lookup(owner.NodeID, owner.Incarnation)
	if !found || !node.IsReady() || node.BrowserEndpoint == "" {
		client.sendError(protocol.CodeServiceUnavailable, "document owner is unavailable")
		return false
	}
	slog.Info("browser session redirected to document owner",
		"component", "browser_transport",
		"event", "owner_redirect",
		"doc_id", docID,
		"from_node_id", client.config.LocalNode.NodeID,
		"owner_node_id", owner.NodeID,
		"owner_incarnation", owner.Incarnation,
		"browser_endpoint", node.BrowserEndpoint,
	)
	writeCtx, cancel := context.WithTimeout(ctx, client.config.WriteTimeout)
	err := client.connection.Write(writeCtx, protocol.Message{Type: protocol.TypeOwnerRedirect, DocID: docID, Endpoint: node.BrowserEndpoint})
	cancel()
	if err != nil {
		client.Close(err)
	}
	return false
}

func (client *sessionClient) processCursor(ctx context.Context, message protocol.Message) {
	if !client.joined || message.DocID != client.docID || message.Cursor == nil {
		client.sendError(protocol.CodeBadMessage, "joined document and cursor are required")
		return
	}
	if !client.allowCursorMessage() {
		return
	}
	if err := client.room.SubmitCursorContext(ctx, client, *message.Cursor); err != nil {
		var coded *room.CodedError
		if errors.As(err, &coded) && coded.Code == protocol.CodeBadMessage {
			return
		}
		client.sendRoomError(err, "invalid cursor update")
	}
}

func (client *sessionClient) allowCursorMessage() bool {
	now := time.Now()
	if client.cursorWindow.IsZero() || now.Sub(client.cursorWindow) >= time.Second {
		client.cursorWindow = now
		client.cursorMessages = 0
	}
	client.cursorMessages++
	return client.cursorMessages <= cursorMessagesPerSecond
}

func (client *sessionClient) writeLoop(ctx context.Context) {
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
			err := client.connection.Write(writeCtx, message)
			cancel()
			if err != nil {
				client.Close(err)
				return
			}
		}
	}
}

func (client *sessionClient) sendRoomError(err error, fallback string) {
	var coded *room.CodedError
	if errors.As(err, &coded) {
		client.sendError(coded.Code, coded.Message)
	} else if !errors.Is(err, room.ErrSlowClient) {
		client.sendError(protocol.CodeInternalError, fallback)
	}
}

func (client *sessionClient) sendError(code, message string) {
	if !client.TrySend(protocol.Error(code, message)) {
		client.Close(room.ErrSlowClient)
	}
}
