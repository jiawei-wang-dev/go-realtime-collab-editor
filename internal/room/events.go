package room

import (
	"context"

	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/collab"
	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/permission"
	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/protocol"
	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/storage"
	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/transport"
)

type event interface{ isRoomEvent() }

type joinEvent struct {
	client transport.Client
	reply  chan error
}

func (joinEvent) isRoomEvent() {}

type resumeEvent struct {
	client      transport.Client
	lastVersion uint64
	reply       chan error
}

func (resumeEvent) isRoomEvent() {}

type leaveEvent struct {
	client transport.Client
	reply  chan struct{}
}

func (leaveEvent) isRoomEvent() {}

type editEvent struct {
	ctx       context.Context
	client    transport.Client
	requestID string
	operation collab.Operation
	reply     chan error
}

func (editEvent) isRoomEvent() {}

type cursorEvent struct {
	client transport.Client
	cursor protocol.CursorState
	reply  chan error
}

func (cursorEvent) isRoomEvent() {}

type permissionChangedEvent struct {
	ctx     context.Context
	actorID string
	userID  string
	role    permission.Role
	reply   chan error
}

func (permissionChangedEvent) isRoomEvent() {}

type snapshotEvent struct{ reply chan snapshot }

func (snapshotEvent) isRoomEvent() {}

type snapshotSavedEvent struct {
	snapshot storage.Snapshot
	err      error
}

func (snapshotSavedEvent) isRoomEvent() {}

type snapshotRetryEvent struct{}

func (snapshotRetryEvent) isRoomEvent() {}

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
	err      error
}

type readDocumentResult struct {
	document collab.Document
	err      error
}

type permissionResult struct {
	role permission.Role
	err  error
}
