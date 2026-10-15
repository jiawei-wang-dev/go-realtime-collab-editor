package websocket

import (
	"bytes"
	"encoding/json"
	"errors"
	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/protocol"
	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/room"
	"io"

	gorilla "github.com/gorilla/websocket"
)

const clientSendBuffer = 32

type client struct {
	conn        *gorilla.Conn
	room        *room.Room
	participant *room.Client
	docID       string
	joined      bool
}

func newClient(conn *gorilla.Conn, targetRoom *room.Room, docID, userID string) *client {
	return &client{
		conn:  conn,
		room:  targetRoom,
		docID: docID,
		participant: &room.Client{
			UserID: userID,
			Send:   make(chan protocol.Message, clientSendBuffer),
		},
	}
}

func (c *client) readPump() {
	defer c.conn.Close()
	defer func() {
		if c.joined {
			c.room.Leave(c.participant)
			return
		}
		close(c.participant.Send)
	}()

	for {
		_, payload, err := c.conn.ReadMessage()
		if err != nil {
			return
		}
		var message protocol.Message
		if err := decodeMessage(payload, &message); err != nil {
			c.sendError(protocol.CodeBadMessage, err.Error())
			continue
		}

		switch message.Type {
		case protocol.TypeJoin:
			c.handleJoin(message)
		case protocol.TypeEdit:
			c.handleEdit(message)
		default:
			c.sendError(protocol.CodeBadMessage, "unsupported message type")
		}
	}
}

func (c *client) writePump() {
	defer c.conn.Close()
	for message := range c.participant.Send {
		if err := c.conn.WriteJSON(message); err != nil {
			return
		}
	}
}

func (c *client) handleJoin(message protocol.Message) {
	if c.joined {
		c.sendError(protocol.CodeBadMessage, "client already joined")
		return
	}
	if message.DocID != c.docID {
		c.sendError(protocol.CodeBadMessage, "join doc_id does not match WebSocket query")
		return
	}
	if err := c.room.Join(c.participant); err != nil {
		var coded *room.CodedError
		if errors.As(err, &coded) {
			c.sendError(coded.Code, coded.Message)
		} else {
			c.sendError(protocol.CodeInternalError, "could not join document")
		}
		return
	}
	c.joined = true
}

func (c *client) handleEdit(message protocol.Message) {
	if !c.joined {
		c.sendError(protocol.CodeBadMessage, "join is required before editing")
		return
	}
	if message.DocID != c.docID {
		c.sendError(protocol.CodeBadMessage, "edit doc_id does not match joined document")
		return
	}
	if message.RequestID == "" || message.Operation == nil {
		c.sendError(protocol.CodeBadMessage, "request_id and operation are required")
		return
	}

	operation := *message.Operation
	operation.BaseVersion = message.BaseVersion
	_ = c.room.SubmitEdit(c.participant, message.RequestID, operation)
}

func (c *client) sendError(code, message string) {
	c.participant.Send <- protocol.Error(code, message)
}

func decodeMessage(payload []byte, target *protocol.Message) error {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("message must contain exactly one JSON value")
	}
	return nil
}
