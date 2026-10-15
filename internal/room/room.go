package room

import (
	"fmt"
	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/collab"
	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/permission"
	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/protocol"
	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/storage"
)

type Client struct {
	UserID string
	Send   chan protocol.Message
}

type event interface {
	isRoomEvent()
}

type joinEvent struct {
	client *Client
	reply  chan error
}

func (joinEvent) isRoomEvent() {}

type leaveEvent struct {
	client *Client
	reply  chan struct{}
}

func (leaveEvent) isRoomEvent() {}

type editEvent struct {
	client    *Client
	requestID string
	operation collab.Operation
	reply     chan error
}

func (editEvent) isRoomEvent() {}

type permissionChangedEvent struct {
	actorID string
	userID  string
	role    permission.Role
	reply   chan error
}

func (permissionChangedEvent) isRoomEvent() {}

type snapshotEvent struct {
	reply chan snapshot
}

func (snapshotEvent) isRoomEvent() {}

type permissionEvent struct {
	actorID string
	userID  string
	reply   chan permissionResult
}

func (permissionEvent) isRoomEvent() {}

type readDocumentEvent struct {
	userID string
	reply  chan readDocumentResult
}

func (readDocumentEvent) isRoomEvent() {}

type snapshot struct {
	document collab.Document
}

type readDocumentResult struct {
	document collab.Document
	err      error
}

type permissionResult struct {
	role permission.Role
	err  error
}

type Room struct {
	docID       string
	events      chan event
	clients     map[*Client]struct{}
	document    collab.Document
	history     []collab.AppliedOperation
	permissions map[string]permission.Role
	storage     *storage.Memory
}

func newRoom(
	document collab.Document,
	history []collab.AppliedOperation,
	permissions map[string]permission.Role,
	store *storage.Memory,
) *Room {
	return &Room{
		docID:       document.ID,
		events:      make(chan event),
		clients:     make(map[*Client]struct{}),
		document:    document,
		history:     history,
		permissions: permissions,
		storage:     store,
	}
}

func (r *Room) Run() {
	for event := range r.events {
		switch event := event.(type) {
		case joinEvent:
			r.handleJoin(event)
		case leaveEvent:
			r.handleLeave(event)
		case editEvent:
			r.handleEdit(event)
		case permissionChangedEvent:
			r.handlePermissionChanged(event)
		case snapshotEvent:
			event.reply <- snapshot{document: r.document}
		case permissionEvent:
			if !r.permissions[event.actorID].CanChangePermissions() {
				event.reply <- permissionResult{err: &CodedError{
					Code:    protocol.CodePermissionDenied,
					Message: "owner permission required",
				}}
				continue
			}
			event.reply <- permissionResult{role: r.permissions[event.userID]}
		case readDocumentEvent:
			role := r.permissions[event.userID]
			if !role.CanRead() {
				event.reply <- readDocumentResult{err: &CodedError{
					Code:    protocol.CodePermissionDenied,
					Message: "read permission required",
				}}
				continue
			}
			event.reply <- readDocumentResult{document: r.document}
		}
	}
}

func (r *Room) Join(client *Client) error {
	reply := make(chan error, 1)
	r.events <- joinEvent{client: client, reply: reply}
	return <-reply
}

func (r *Room) Leave(client *Client) {
	reply := make(chan struct{}, 1)
	r.events <- leaveEvent{client: client, reply: reply}
	<-reply
}

func (r *Room) SubmitEdit(client *Client, requestID string, operation collab.Operation) error {
	reply := make(chan error, 1)
	r.events <- editEvent{
		client:    client,
		requestID: requestID,
		operation: operation,
		reply:     reply,
	}
	return <-reply
}

func (r *Room) ChangePermission(actorID, userID string, role permission.Role) error {
	reply := make(chan error, 1)
	r.events <- permissionChangedEvent{
		actorID: actorID,
		userID:  userID,
		role:    role,
		reply:   reply,
	}
	return <-reply
}

func (r *Room) Snapshot() collab.Document {
	reply := make(chan snapshot, 1)
	r.events <- snapshotEvent{reply: reply}
	return (<-reply).document
}

func (r *Room) Permission(actorID, userID string) (permission.Role, error) {
	reply := make(chan permissionResult, 1)
	r.events <- permissionEvent{actorID: actorID, userID: userID, reply: reply}
	result := <-reply
	return result.role, result.err
}

func (r *Room) ReadDocument(userID string) (collab.Document, error) {
	reply := make(chan readDocumentResult, 1)
	r.events <- readDocumentEvent{userID: userID, reply: reply}
	result := <-reply
	return result.document, result.err
}

func (r *Room) handleJoin(event joinEvent) {
	if event.client == nil || event.client.UserID == "" || event.client.Send == nil {
		event.reply <- fmt.Errorf("invalid client")
		return
	}
	role := r.permissions[event.client.UserID]
	if !role.CanRead() {
		event.reply <- &CodedError{
			Code:    protocol.CodePermissionDenied,
			Message: "read permission required",
		}
		return
	}
	r.clients[event.client] = struct{}{}
	event.client.Send <- protocol.Message{
		Type:    protocol.TypeJoined,
		DocID:   r.docID,
		Version: r.document.Version,
		Content: r.document.Content,
		Role:    role,
	}
	event.reply <- nil
}

func (r *Room) handleLeave(event leaveEvent) {
	if _, ok := r.clients[event.client]; ok {
		delete(r.clients, event.client)
		close(event.client.Send)
	}
	event.reply <- struct{}{}
}

func (r *Room) handleEdit(event editEvent) {
	role := r.permissions[event.client.UserID]
	if !role.CanWrite() {
		err := &CodedError{Code: protocol.CodePermissionDenied, Message: "write permission required"}
		event.client.Send <- protocol.Error(err.Code, err.Message)
		event.reply <- err
		return
	}

	if event.operation.BaseVersion > r.document.Version {
		err := &CodedError{Code: protocol.CodeVersionAhead, Message: "base version is ahead of document version"}
		event.client.Send <- protocol.Error(err.Code, err.Message)
		event.reply <- err
		return
	}

	operation := event.operation
	operation.ID = event.requestID
	operation.UserID = event.client.UserID
	for _, applied := range r.history {
		if applied.Version <= operation.BaseVersion {
			continue
		}
		var err error
		operation, err = collab.Transform(operation, applied.Operation)
		if err != nil {
			r.rejectInvalidOperation(event, err)
			return
		}
	}

	content, err := collab.Apply(r.document.Content, operation)
	if err != nil {
		r.rejectInvalidOperation(event, err)
		return
	}

	r.document.Content = content
	r.document.Version++
	applied := collab.AppliedOperation{Operation: operation, Version: r.document.Version}
	r.history = append(r.history, applied)
	r.storage.SaveApplied(r.document, applied)

	message := protocol.Message{
		Type:      protocol.TypeEditApplied,
		RequestID: event.requestID,
		DocID:     r.docID,
		Version:   r.document.Version,
		Operation: &applied.Operation,
	}
	for client := range r.clients {
		client.Send <- message
	}
	event.reply <- nil
}

func (r *Room) rejectInvalidOperation(event editEvent, cause error) {
	err := &CodedError{Code: protocol.CodeInvalidOperation, Message: cause.Error()}
	event.client.Send <- protocol.Error(err.Code, err.Message)
	event.reply <- err
}

func (r *Room) handlePermissionChanged(event permissionChangedEvent) {
	if !r.permissions[event.actorID].CanChangePermissions() {
		event.reply <- &CodedError{
			Code:    protocol.CodePermissionDenied,
			Message: "owner permission required",
		}
		return
	}
	if event.userID == "" || !event.role.Valid() {
		event.reply <- &CodedError{
			Code:    protocol.CodeBadMessage,
			Message: "valid target user and role are required",
		}
		return
	}

	r.permissions[event.userID] = event.role
	r.storage.SetPermission(r.docID, event.userID, event.role)
	for client := range r.clients {
		if client.UserID == event.userID {
			client.Send <- protocol.Message{
				Type:  protocol.TypePermissionChanged,
				DocID: r.docID,
				Role:  event.role,
			}
		}
	}
	event.reply <- nil
}
