package quictransport

import (
	"context"
	"errors"
	"io"
	"sync"
	"time"

	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/authentication"
	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/protocol"
	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/room"
	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/transport"

	quic "github.com/quic-go/quic-go"
)

const applicationCloseCode quic.ApplicationErrorCode = 0x100
const streamCloseCode quic.StreamErrorCode = 0x100

type Client struct {
	connection    clientConnection
	stream        clientStream
	room          *room.Room
	docID         string
	principal     authentication.Principal
	authenticator authentication.Authenticator
	config        Config
	send          chan protocol.Message

	mu         sync.RWMutex
	closed     bool
	joined     bool
	unregister func()
	writeDone  chan struct{}
}

type clientConnection interface {
	CloseWithError(quic.ApplicationErrorCode, string) error
}

type clientStream interface {
	io.Reader
	io.Writer
	Close() error
	CancelRead(quic.StreamErrorCode)
	CancelWrite(quic.StreamErrorCode)
	SetReadDeadline(time.Time) error
	SetWriteDeadline(time.Time) error
}

var _ transport.Client = (*Client)(nil)

func newClient(
	connection clientConnection,
	stream clientStream,
	targetRoom *room.Room,
	docID string,
	principal authentication.Principal,
	authenticator authentication.Authenticator,
	config Config,
) *Client {
	return &Client{
		connection:    connection,
		stream:        stream,
		room:          targetRoom,
		docID:         docID,
		principal:     principal,
		authenticator: authenticator,
		config:        config,
		send:          make(chan protocol.Message, config.SendQueueSize),
		writeDone:     make(chan struct{}),
	}
}

func (c *Client) Identity() string { return c.principal.UserID }

func (c *Client) TrySend(message protocol.Message) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.closed {
		return false
	}
	select {
	case c.send <- message:
		return true
	default:
		return false
	}
}

func (c *Client) Close(reason error) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.closed = true
	close(c.send)
	c.mu.Unlock()

	message := "connection closed"
	if reason != nil {
		message = reason.Error()
	}
	c.stream.CancelRead(streamCloseCode)
	c.stream.CancelWrite(streamCloseCode)
	_ = c.stream.Close()
	_ = c.connection.CloseWithError(applicationCloseCode, message)
}

func (c *Client) run(initial protocol.Message) {
	go c.writeLoop()
	defer func() {
		if c.joined {
			ctx, cancel := context.WithTimeout(context.Background(), c.config.WriteTimeout)
			_ = c.room.LeaveContext(ctx, c)
			cancel()
		}
		if c.unregister != nil {
			c.unregister()
		}
		c.Close(nil)
		<-c.writeDone
	}()

	if !c.processAuthenticatedMessage(initial) {
		return
	}
	for {
		_ = c.stream.SetReadDeadline(time.Now().Add(c.config.ReadTimeout))
		message, err := readMessage(c.stream, c.config.MaxMessageSize)
		if err != nil {
			if errors.Is(err, ErrBadMessage) {
				c.sendError(protocol.CodeBadMessage, err.Error())
				continue
			}
			return
		}
		if !c.processAuthenticatedMessage(message) {
			return
		}
	}
}

func (c *Client) writeLoop() {
	defer close(c.writeDone)
	for message := range c.send {
		validationCtx, cancel := context.WithTimeout(context.Background(), c.config.WriteTimeout)
		err := c.authenticator.ValidatePrincipal(validationCtx, c.principal)
		cancel()
		if err != nil {
			c.Close(err)
			return
		}
		_ = c.stream.SetWriteDeadline(time.Now().Add(c.config.WriteTimeout))
		if err := writeMessage(c.stream, message, c.config.MaxMessageSize); err != nil {
			c.Close(err)
			return
		}
	}
}

func (c *Client) processAuthenticatedMessage(message protocol.Message) bool {
	ctx, cancel := context.WithTimeout(context.Background(), c.config.OperationTimeout)
	release := func() {}
	var err error
	if guarded, ok := c.authenticator.(authentication.PrincipalUseAuthenticator); ok {
		release, err = guarded.AcquirePrincipal(ctx, c.principal)
	} else {
		err = c.authenticator.ValidatePrincipal(ctx, c.principal)
	}
	if err != nil {
		cancel()
		c.sendError(protocol.CodePermissionDenied, "session is not active")
		return false
	}
	defer release()
	defer cancel()

	switch message.Type {
	case protocol.TypeJoin:
		return c.handleJoin(ctx, message)
	case protocol.TypeResume:
		return c.handleResume(ctx, message)
	case protocol.TypeEdit:
		return c.handleEdit(ctx, message)
	default:
		c.sendError(protocol.CodeBadMessage, "unsupported message type")
		return true
	}
}

func (c *Client) handleJoin(ctx context.Context, message protocol.Message) bool {
	if c.joined {
		c.sendError(protocol.CodeBadMessage, "client already joined")
		return true
	}
	if message.DocID != c.docID {
		c.sendError(protocol.CodeBadMessage, "join doc_id does not match connection document")
		return true
	}
	if err := c.room.JoinContext(ctx, c); err != nil {
		c.sendRoomError(err, "could not join document")
		return !errors.Is(err, room.ErrSlowClient)
	}
	c.joined = true
	return true
}

func (c *Client) handleResume(ctx context.Context, message protocol.Message) bool {
	if c.joined {
		c.sendError(protocol.CodeBadMessage, "client already joined")
		return true
	}
	if message.DocID != c.docID {
		c.sendError(protocol.CodeBadMessage, "resume doc_id does not match connection document")
		return true
	}
	if err := c.room.ResumeContext(ctx, c, message.LastVersion); err != nil {
		c.sendRoomError(err, "could not resume document")
		return !errors.Is(err, room.ErrSlowClient)
	}
	c.joined = true
	return true
}

func (c *Client) handleEdit(ctx context.Context, message protocol.Message) bool {
	if !c.joined {
		c.sendError(protocol.CodeBadMessage, "join or resume is required before editing")
		return true
	}
	if message.DocID != c.docID {
		c.sendError(protocol.CodeBadMessage, "edit doc_id does not match joined document")
		return true
	}
	if message.RequestID == "" || message.Operation == nil {
		c.sendError(protocol.CodeBadMessage, "request_id and operation are required")
		return true
	}
	operation := *message.Operation
	operation.BaseVersion = message.BaseVersion
	_ = c.room.SubmitEditContext(ctx, c, message.RequestID, operation)
	return true
}

func (c *Client) sendRoomError(err error, fallback string) {
	var coded *room.CodedError
	if errors.As(err, &coded) {
		c.sendError(coded.Code, coded.Message)
	} else if !errors.Is(err, room.ErrSlowClient) {
		c.sendError(protocol.CodeInternalError, fallback)
	}
}

func (c *Client) sendError(code, message string) {
	if !c.TrySend(protocol.Error(code, message)) {
		c.Close(room.ErrSlowClient)
	}
}
