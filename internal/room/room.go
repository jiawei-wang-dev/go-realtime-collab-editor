package room

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/collab"
	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/permission"
	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/protocol"
	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/storage"
	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/transport"
	"go.opentelemetry.io/otel"
)

type Room struct {
	docID          string
	events         chan event
	clients        map[transport.Client]struct{}
	cursors        map[transport.Client]protocol.CursorState
	clientCount    atomic.Int64
	document       collab.Document
	history        []collab.AppliedOperation
	historyFloor   uint64
	latestSnapshot storage.Snapshot
	permissions    map[string]permission.Role
	storage        storage.Store
	authority      storage.WriteAuthority

	ctx              context.Context
	cancel           context.CancelFunc
	done             chan struct{}
	idleTimeout      time.Duration
	snapshotInterval uint64
	maxHistory       int
	snapshotTimeout  time.Duration
	snapshotInFlight bool
	observer         Observer
	background       sync.WaitGroup
	onStopped        func(*Room)
}

func newRoom(
	parent context.Context,
	document collab.Document,
	history []collab.AppliedOperation,
	latestSnapshot storage.Snapshot,
	permissions map[string]permission.Role,
	store storage.Store,
	authority storage.WriteAuthority,
	idleTimeout time.Duration,
	snapshotInterval uint64,
	maxHistory int,
	snapshotTimeout time.Duration,
	observer Observer,
	onStopped func(*Room),
) *Room {
	ctx, cancel := context.WithCancel(parent)
	return &Room{
		docID:            document.ID,
		events:           make(chan event),
		clients:          make(map[transport.Client]struct{}),
		cursors:          make(map[transport.Client]protocol.CursorState),
		document:         document,
		history:          history,
		historyFloor:     latestSnapshot.Version,
		latestSnapshot:   latestSnapshot,
		permissions:      permissions,
		storage:          store,
		authority:        authority,
		ctx:              ctx,
		cancel:           cancel,
		done:             make(chan struct{}),
		idleTimeout:      idleTimeout,
		snapshotInterval: snapshotInterval,
		maxHistory:       maxHistory,
		snapshotTimeout:  snapshotTimeout,
		observer:         observer,
		onStopped:        onStopped,
	}
}

func (r *Room) Run() {
	idle := time.NewTimer(r.idleTimeout)
	defer idle.Stop()
	defer close(r.done)
	// Done is the externally visible reclamation barrier. Because deferred calls
	// run in LIFO order, register the Manager callback after close(done) so the
	// callback removes this exact Room instance before observers see Done close.
	defer r.onStopped(r)
	defer r.closeClients()
	defer r.background.Wait()

	for {
		if r.ctx.Err() != nil {
			return
		}
		select {
		case <-r.ctx.Done():
			return
		case <-idle.C:
			if len(r.clients) == 0 {
				r.cancel()
				return
			}
		case roomEvent := <-r.events:
			if r.ctx.Err() != nil {
				r.rejectClosedEvent(roomEvent)
				return
			}
			r.handle(roomEvent)
			resetIdleTimer(idle, r.idleTimeout, len(r.clients) == 0)
		}
	}
}

func (r *Room) handle(roomEvent event) {
	switch current := roomEvent.(type) {
	case joinEvent:
		r.handleJoin(current)
	case resumeEvent:
		r.handleResume(current)
	case leaveEvent:
		r.handleLeave(current)
	case editEvent:
		r.handleEdit(current)
	case cursorEvent:
		r.handleCursor(current)
	case permissionChangedEvent:
		r.handlePermissionChanged(current)
	case snapshotEvent:
		current.reply <- snapshot{document: r.document}
	case snapshotSavedEvent:
		r.handleSnapshotSaved(current)
	case snapshotRetryEvent:
		r.maybeSaveSnapshot()
	case permissionEvent:
		r.handlePermission(current)
	case readDocumentEvent:
		r.handleReadDocument(current)
	}
}

func (r *Room) Join(client transport.Client) error {
	return r.JoinContext(context.Background(), client)
}

func (r *Room) JoinContext(ctx context.Context, client transport.Client) error {
	reply := make(chan error, 1)
	if err := r.dispatch(ctx, joinEvent{client: client, reply: reply}); err != nil {
		return err
	}
	return waitError(ctx, r.ctx, reply)
}

func (r *Room) ResumeContext(ctx context.Context, client transport.Client, lastVersion uint64) error {
	reply := make(chan error, 1)
	if err := r.dispatch(ctx, resumeEvent{client: client, lastVersion: lastVersion, reply: reply}); err != nil {
		return err
	}
	return waitError(ctx, r.ctx, reply)
}

func (r *Room) Leave(client transport.Client) { _ = r.LeaveContext(context.Background(), client) }

func (r *Room) LeaveContext(ctx context.Context, client transport.Client) error {
	reply := make(chan struct{}, 1)
	if err := r.dispatch(ctx, leaveEvent{client: client, reply: reply}); err != nil {
		return err
	}
	select {
	case <-reply:
		return nil
	case <-r.ctx.Done():
		return ErrRoomClosed
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (r *Room) SubmitEdit(client transport.Client, requestID string, operation collab.Operation) error {
	return r.SubmitEditContext(context.Background(), client, requestID, operation)
}

func (r *Room) SubmitEditContext(ctx context.Context, client transport.Client, requestID string, operation collab.Operation) error {
	reply := make(chan error, 1)
	if err := r.dispatch(ctx, editEvent{ctx: ctx, client: client, requestID: requestID, operation: operation, reply: reply}); err != nil {
		return err
	}
	return waitError(ctx, r.ctx, reply)
}

func (r *Room) SubmitCursorContext(ctx context.Context, client transport.Client, cursor protocol.CursorState) error {
	reply := make(chan error, 1)
	if err := r.dispatch(ctx, cursorEvent{client: client, cursor: cursor, reply: reply}); err != nil {
		return err
	}
	return waitError(ctx, r.ctx, reply)
}

func (r *Room) ChangePermission(actorID, userID string, role permission.Role) error {
	return r.ChangePermissionContext(context.Background(), actorID, userID, role)
}

func (r *Room) ChangePermissionContext(ctx context.Context, actorID, userID string, role permission.Role) error {
	reply := make(chan error, 1)
	if err := r.dispatch(ctx, permissionChangedEvent{ctx: ctx, actorID: actorID, userID: userID, role: role, reply: reply}); err != nil {
		return err
	}
	return waitError(ctx, r.ctx, reply)
}

func (r *Room) Snapshot() collab.Document {
	document, _ := r.SnapshotContext(context.Background())
	return document
}

func (r *Room) SnapshotContext(ctx context.Context) (collab.Document, error) {
	reply := make(chan snapshot, 1)
	if err := r.dispatch(ctx, snapshotEvent{reply: reply}); err != nil {
		return collab.Document{}, err
	}
	select {
	case result := <-reply:
		return result.document, result.err
	case <-r.ctx.Done():
		return collab.Document{}, ErrRoomClosed
	case <-ctx.Done():
		return collab.Document{}, ctx.Err()
	}
}

func (r *Room) Permission(actorID, userID string) (permission.Role, error) {
	return r.PermissionContext(context.Background(), actorID, userID)
}

func (r *Room) PermissionContext(ctx context.Context, actorID, userID string) (permission.Role, error) {
	reply := make(chan permissionResult, 1)
	if err := r.dispatch(ctx, permissionEvent{actorID: actorID, userID: userID, reply: reply}); err != nil {
		return "", err
	}
	select {
	case result := <-reply:
		return result.role, result.err
	case <-r.ctx.Done():
		return "", ErrRoomClosed
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

func (r *Room) ReadDocument(userID string) (collab.Document, error) {
	return r.ReadDocumentContext(context.Background(), userID)
}

func (r *Room) ReadDocumentContext(ctx context.Context, userID string) (collab.Document, error) {
	reply := make(chan readDocumentResult, 1)
	if err := r.dispatch(ctx, readDocumentEvent{userID: userID, reply: reply}); err != nil {
		return collab.Document{}, err
	}
	select {
	case result := <-reply:
		return result.document, result.err
	case <-r.ctx.Done():
		return collab.Document{}, ErrRoomClosed
	case <-ctx.Done():
		return collab.Document{}, ctx.Err()
	}
}

func (r *Room) Stop(ctx context.Context) error {
	r.cancel()
	select {
	case <-r.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (r *Room) Done() <-chan struct{} { return r.done }

func (r *Room) Stopped() bool {
	select {
	case <-r.ctx.Done():
		return true
	default:
		return false
	}
}

func (r *Room) dispatch(ctx context.Context, roomEvent event) error {
	if r.ctx.Err() != nil {
		return ErrRoomClosed
	}
	select {
	case r.events <- roomEvent:
		return nil
	case <-r.ctx.Done():
		return ErrRoomClosed
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (r *Room) handleJoin(event joinEvent) {
	if event.client == nil || event.client.Identity() == "" {
		event.reply <- fmt.Errorf("invalid client")
		return
	}
	role := r.permissions[event.client.Identity()]
	if !role.CanRead() {
		event.reply <- &CodedError{Code: protocol.CodePermissionDenied, Message: "read permission required"}
		return
	}
	if _, exists := r.clients[event.client]; !exists {
		r.clients[event.client] = struct{}{}
		r.clientCount.Add(1)
	}
	if !event.client.TrySend(protocol.Message{Type: protocol.TypeJoined, DocID: r.docID, Version: r.document.Version, Content: r.document.Content, Role: role}) {
		r.disconnect(event.client, ErrSlowClient)
		event.reply <- ErrSlowClient
		return
	}
	r.broadcastPresence()
	r.sendCursorSnapshot(event.client)
	event.reply <- nil
}

func (r *Room) handleResume(event resumeEvent) {
	if event.client == nil || event.client.Identity() == "" {
		event.reply <- fmt.Errorf("invalid client")
		return
	}
	if _, exists := r.clients[event.client]; exists {
		event.reply <- &CodedError{Code: protocol.CodeBadMessage, Message: "client already joined"}
		return
	}
	role := r.permissions[event.client.Identity()]
	if !role.CanRead() {
		event.reply <- &CodedError{Code: protocol.CodePermissionDenied, Message: "read permission required"}
		return
	}

	message := r.resumeMessage(event.lastVersion)
	message.Role = role
	// Queue the initial state before registering the client so every later
	// broadcast is ordered after the initial snapshot.
	if !event.client.TrySend(message) {
		event.client.Close(ErrSlowClient)
		event.reply <- ErrSlowClient
		return
	}
	r.clients[event.client] = struct{}{}
	r.clientCount.Add(1)
	r.broadcastPresence()
	r.sendCursorSnapshot(event.client)
	event.reply <- nil
}

func (r *Room) resumeMessage(lastVersion uint64) protocol.Message {
	if lastVersion >= r.historyFloor && lastVersion <= r.document.Version {
		return protocol.Message{
			Type:        protocol.TypeSync,
			DocID:       r.docID,
			FromVersion: lastVersion,
			ToVersion:   r.document.Version,
			Operations:  r.versionedOperationsAfter(lastVersion),
		}
	}
	return protocol.Message{
		Type:  protocol.TypeResyncRequired,
		DocID: r.docID,
		Snapshot: &protocol.Snapshot{
			Version: r.latestSnapshot.Version,
			Content: r.latestSnapshot.Content,
		},
		Operations:     r.versionedOperationsAfter(r.latestSnapshot.Version),
		CurrentVersion: r.document.Version,
	}
}

func (r *Room) handlePermission(event permissionEvent) {
	if !r.permissions[event.actorID].CanChangePermissions() {
		event.reply <- permissionResult{err: &CodedError{Code: protocol.CodePermissionDenied, Message: "owner permission required"}}
		return
	}
	event.reply <- permissionResult{role: r.permissions[event.userID]}
}

func (r *Room) handleReadDocument(event readDocumentEvent) {
	if !r.permissions[event.userID].CanRead() {
		event.reply <- readDocumentResult{err: &CodedError{Code: protocol.CodePermissionDenied, Message: "read permission required"}}
		return
	}
	event.reply <- readDocumentResult{document: r.document}
}

func (r *Room) handleLeave(event leaveEvent) {
	if _, ok := r.clients[event.client]; ok {
		r.removeCursor(event.client)
		delete(r.clients, event.client)
		r.clientCount.Add(-1)
		event.client.Close(nil)
		r.broadcastPresence()
	}
	event.reply <- struct{}{}
}

func (r *Room) handleCursor(event cursorEvent) {
	if _, joined := r.clients[event.client]; !joined {
		event.reply <- ErrRoomClosed
		return
	}
	if !r.permissions[event.client.Identity()].CanRead() {
		event.reply <- &CodedError{Code: protocol.CodePermissionDenied, Message: "read permission required"}
		return
	}
	cursorClient, supported := event.client.(transport.CursorClient)
	if !supported || !cursorClient.SupportsCursor() {
		event.reply <- &CodedError{Code: protocol.CodeBadMessage, Message: "cursor presence is not supported"}
		return
	}
	if previous, exists := r.cursors[event.client]; exists && event.cursor.Sequence <= previous.Sequence {
		event.reply <- nil
		return
	}
	transformed, err := r.transformCursor(event.cursor)
	if err != nil {
		event.reply <- err
		return
	}
	transformed.UserID = event.client.Identity()
	transformed.Username = cursorClient.DisplayName()
	r.cursors[event.client] = transformed
	r.broadcastCursor(protocol.Message{Type: protocol.TypeCursorChanged, DocID: r.docID, Cursor: &transformed}, event.client)
	event.reply <- nil
}

func (r *Room) transformCursor(cursor protocol.CursorState) (protocol.CursorState, error) {
	if cursor.Anchor < 0 || cursor.Head < 0 || cursor.Version > r.document.Version || cursor.Version < r.historyFloor {
		return protocol.CursorState{}, &CodedError{Code: protocol.CodeBadMessage, Message: "cursor version or position is invalid"}
	}
	for _, applied := range r.history {
		if applied.Version <= cursor.Version {
			continue
		}
		cursor.Anchor = transformCursorPosition(cursor.Anchor, applied.Operation)
		cursor.Head = transformCursorPosition(cursor.Head, applied.Operation)
	}
	cursor.Version = r.document.Version
	contentLength := len([]rune(r.document.Content))
	if cursor.Anchor > contentLength || cursor.Head > contentLength {
		return protocol.CursorState{}, &CodedError{Code: protocol.CodeBadMessage, Message: "cursor position is outside document"}
	}
	return cursor, nil
}

func transformCursorPosition(position int, operation collab.Operation) int {
	switch operation.Kind {
	case collab.OperationInsert:
		if operation.Position <= position {
			return position + len([]rune(operation.Text))
		}
	case collab.OperationDelete:
		end := operation.Position + operation.Length
		if position >= end {
			return position - operation.Length
		}
		if position > operation.Position {
			return operation.Position
		}
	}
	return position
}

func (r *Room) transformStoredCursors(operation collab.Operation) {
	for client, cursor := range r.cursors {
		cursor.Anchor = transformCursorPosition(cursor.Anchor, operation)
		cursor.Head = transformCursorPosition(cursor.Head, operation)
		cursor.Version = r.document.Version
		r.cursors[client] = cursor
		current := cursor
		r.broadcastCursor(protocol.Message{Type: protocol.TypeCursorChanged, DocID: r.docID, Cursor: &current}, client)
	}
}

func (r *Room) sendCursorSnapshot(client transport.Client) {
	cursorClient, ok := client.(transport.CursorClient)
	if !ok || !cursorClient.SupportsCursor() {
		return
	}
	for owner, cursor := range r.cursors {
		if owner == client {
			continue
		}
		current := cursor
		if !client.TrySend(protocol.Message{Type: protocol.TypeCursorChanged, DocID: r.docID, Cursor: &current}) {
			r.disconnect(client, ErrSlowClient)
			return
		}
	}
}

func (r *Room) broadcastCursor(message protocol.Message, except transport.Client) {
	for client := range r.clients {
		if client == except {
			continue
		}
		cursorClient, ok := client.(transport.CursorClient)
		if !ok || !cursorClient.SupportsCursor() {
			continue
		}
		if !client.TrySend(message) {
			r.disconnect(client, ErrSlowClient)
		}
	}
}

func (r *Room) removeCursor(client transport.Client) {
	cursor, exists := r.cursors[client]
	if !exists {
		return
	}
	delete(r.cursors, client)
	r.broadcastCursor(protocol.Message{Type: protocol.TypeCursorRemoved, DocID: r.docID, Cursor: &protocol.CursorState{UserID: cursor.UserID}}, client)
}

func (r *Room) broadcastPresence() {
	for {
		unique := make(map[string]struct{}, len(r.clients))
		for client := range r.clients {
			unique[client.Identity()] = struct{}{}
		}
		userIDs := make([]string, 0, len(unique))
		for userID := range unique {
			userIDs = append(userIDs, userID)
		}
		sort.Strings(userIDs)
		message := protocol.Message{Type: protocol.TypePresenceChanged, DocID: r.docID, UserIDs: userIDs}
		var slowClients []transport.Client
		for client := range r.clients {
			presenceClient, ok := client.(transport.PresenceClient)
			if !ok || !presenceClient.SupportsPresence() {
				continue
			}
			if !client.TrySend(message) {
				slowClients = append(slowClients, client)
			}
		}
		if len(slowClients) == 0 {
			return
		}
		for _, client := range slowClients {
			r.disconnect(client, ErrSlowClient)
		}
	}
}

func (r *Room) handleEdit(event editEvent) {
	traceCtx, span := otel.Tracer("collab-editor/room").Start(event.ctx, "room.edit")
	defer span.End()
	event.ctx = traceCtx
	if _, joined := r.clients[event.client]; !joined {
		event.reply <- ErrRoomClosed
		return
	}
	if validationErr := r.validateEditPermission(event); validationErr != nil {
		r.reject(event, validationErr)
		return
	}
	if r.handleExistingEdit(event) {
		return
	}
	if validationErr := r.validateNewEdit(event); validationErr != nil {
		r.reject(event, validationErr)
		return
	}

	nextDocument, applied, err := r.prepareEdit(event)
	if err != nil {
		r.rejectInvalidOperation(event, err)
		return
	}
	if err := r.commitEdit(event.ctx, nextDocument, applied); err != nil {
		if errors.Is(err, storage.ErrDuplicateRequest) {
			r.handleDuplicateAfterCommit(event)
			return
		}
		r.rejectStorageEdit(event, err)
		return
	}

	r.document = nextDocument
	r.history = append(r.history, applied)
	r.broadcast(protocol.Message{Type: protocol.TypeEditApplied, RequestID: event.requestID, DocID: r.docID, Version: r.document.Version, Operation: &applied.Operation})
	r.transformStoredCursors(applied.Operation)
	r.maybeSaveSnapshot()
	event.reply <- nil
}

func (r *Room) validateEditPermission(event editEvent) *CodedError {
	if !r.permissions[event.client.Identity()].CanWrite() {
		return &CodedError{Code: protocol.CodePermissionDenied, Message: "write permission required"}
	}
	return nil
}

func (r *Room) validateNewEdit(event editEvent) *CodedError {
	if len(r.history) >= r.maxHistory {
		return &CodedError{Code: protocol.CodeServiceUnavailable, Message: "operation history limit reached while snapshot is unavailable"}
	}
	if event.operation.BaseVersion > r.document.Version {
		return &CodedError{Code: protocol.CodeVersionAhead, Message: "base version is ahead of document version"}
	}
	if event.operation.BaseVersion < r.historyFloor {
		return &CodedError{Code: protocol.CodeResyncRequired, Message: "base version is older than retained history"}
	}
	return nil
}

func (r *Room) handleExistingEdit(event editEvent) bool {
	existing, err := r.lookupAppliedOperation(event.ctx, event.requestID)
	if errors.Is(err, storage.ErrNotFound) {
		return false
	}
	if err != nil {
		r.reject(event, &CodedError{Code: protocol.CodeInternalError, Message: "could not verify request_id"})
		return true
	}
	if existing.UserID != event.client.Identity() {
		r.reject(event, &CodedError{Code: protocol.CodeDuplicateRequest, Message: "request_id belongs to another user"})
		return true
	}
	r.sendAppliedEdit(event, existing)
	return true
}

func (r *Room) prepareEdit(event editEvent) (collab.Document, collab.AppliedOperation, error) {
	operation := event.operation
	operation.ID = event.requestID
	operation.UserID = event.client.Identity()
	for _, previous := range r.history {
		if previous.Version <= operation.BaseVersion {
			continue
		}
		transformed, err := collab.Transform(operation, previous.Operation)
		if err != nil {
			return collab.Document{}, collab.AppliedOperation{}, err
		}
		operation = transformed
	}
	content, err := collab.Apply(r.document.Content, operation)
	if err != nil {
		return collab.Document{}, collab.AppliedOperation{}, err
	}
	nextDocument := r.document
	nextDocument.Content = content
	nextDocument.Version++
	return nextDocument, collab.AppliedOperation{Operation: operation, Version: nextDocument.Version}, nil
}

func (r *Room) commitEdit(ctx context.Context, document collab.Document, operation collab.AppliedOperation) error {
	commitCtx, cancel := r.storageContext(ctx)
	defer cancel()
	return r.storage.CommitEdit(commitCtx, storage.EditCommit{
		Authority:       r.authority,
		ExpectedVersion: r.document.Version,
		Document:        document,
		Operation:       operation,
	})
}

func (r *Room) sendAppliedEdit(event editEvent, applied collab.AppliedOperation) {
	operation := applied.Operation
	message := protocol.Message{Type: protocol.TypeEditApplied, RequestID: applied.ID, DocID: r.docID, Version: applied.Version, Operation: &operation}
	if !event.client.TrySend(message) {
		r.disconnect(event.client, ErrSlowClient)
		event.reply <- ErrSlowClient
		return
	}
	event.reply <- nil
}

func (r *Room) lookupAppliedOperation(ctx context.Context, requestID string) (collab.AppliedOperation, error) {
	lookupCtx, cancel := r.storageContext(ctx)
	defer cancel()
	return r.storage.GetAppliedOperation(lookupCtx, r.docID, requestID)
}

func (r *Room) handleDuplicateAfterCommit(event editEvent) {
	existing, err := r.lookupAppliedOperation(event.ctx, event.requestID)
	if err != nil || existing.UserID != event.client.Identity() {
		r.rejectStorageEdit(event, storage.ErrDuplicateRequest)
		return
	}
	r.sendAppliedEdit(event, existing)
}

func (r *Room) maybeSaveSnapshot() {
	if r.snapshotInFlight || r.document.Version-r.historyFloor < r.snapshotInterval {
		return
	}
	snapshot := storage.Snapshot{DocumentID: r.docID, Version: r.document.Version, Content: r.document.Content}
	r.snapshotInFlight = true
	r.background.Add(1)
	go r.saveSnapshot(snapshot)
}

func (r *Room) saveSnapshot(snapshot storage.Snapshot) {
	defer r.background.Done()
	ctx, cancel := context.WithTimeout(r.ctx, r.snapshotTimeout)
	defer cancel()
	ctx, span := otel.Tracer("collab-editor/room").Start(ctx, "room.snapshot")
	defer span.End()
	err := r.storage.SaveSnapshot(ctx, storage.SnapshotCommit{Authority: r.authority, Snapshot: snapshot})
	if errors.Is(err, storage.ErrAlreadyExists) {
		err = nil
	}
	_ = r.dispatch(context.Background(), snapshotSavedEvent{snapshot: snapshot, err: err})
}

func (r *Room) handleSnapshotSaved(event snapshotSavedEvent) {
	r.snapshotInFlight = false
	if event.err != nil {
		if r.observer != nil {
			r.observer.SnapshotCompleted(false)
		}
		if errors.Is(event.err, storage.ErrFenced) {
			slog.Error("room self fenced", "component", "room", "event", "self_fenced", "doc_id", r.docID, "operation", "snapshot")
			r.cancel()
			return
		}
		slog.Error("snapshot save failed", "component", "room", "event", "snapshot_failed", "doc_id", r.docID, "version", event.snapshot.Version, "error", event.err)
		r.background.Add(1)
		go r.retrySnapshotAfter(time.Second)
		return
	}
	if r.observer != nil {
		r.observer.SnapshotCompleted(true)
	}
	if event.snapshot.Version <= r.historyFloor {
		return
	}
	r.latestSnapshot = event.snapshot
	r.historyFloor = event.snapshot.Version
	firstRetained := 0
	for firstRetained < len(r.history) && r.history[firstRetained].Version <= r.historyFloor {
		firstRetained++
	}
	r.history = append([]collab.AppliedOperation(nil), r.history[firstRetained:]...)
	// If another snapshot interval accumulated while the write was in flight,
	// schedule the next consistent checkpoint immediately.
	r.maybeSaveSnapshot()
}

func (r *Room) retrySnapshotAfter(delay time.Duration) {
	defer r.background.Done()
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		_ = r.dispatch(context.Background(), snapshotRetryEvent{})
	case <-r.ctx.Done():
	}
}

func (r *Room) versionedOperationsAfter(version uint64) []protocol.VersionedOperation {
	operations := make([]protocol.VersionedOperation, 0, len(r.history))
	for _, applied := range r.history {
		if applied.Version > version {
			operation := applied.Operation
			operation.ID = ""
			operation.UserID = ""
			operation.BaseVersion = 0
			operations = append(operations, protocol.VersionedOperation{Version: applied.Version, RequestID: applied.ID, Operation: operation})
		}
	}
	return operations
}

func (r *Room) rejectInvalidOperation(event editEvent, cause error) {
	r.reject(event, &CodedError{Code: protocol.CodeInvalidOperation, Message: cause.Error()})
}

func (r *Room) reject(event editEvent, err *CodedError) {
	if !event.client.TrySend(protocol.Error(err.Code, err.Message)) {
		r.disconnect(event.client, ErrSlowClient)
	}
	event.reply <- err
}

func (r *Room) rejectStorageEdit(event editEvent, cause error) {
	var coded *CodedError
	switch {
	case errors.Is(cause, storage.ErrFenced):
		coded = &CodedError{Code: protocol.CodeServiceUnavailable, Message: "room ownership is no longer valid"}
		slog.Error("room self fenced", "component", "room", "event", "self_fenced", "doc_id", r.docID, "operation", "edit", "request_id", event.requestID)
		r.cancel()
	case errors.Is(cause, storage.ErrDuplicateRequest):
		coded = &CodedError{Code: protocol.CodeDuplicateRequest, Message: "request_id has already been applied"}
	case errors.Is(cause, storage.ErrVersionConflict):
		coded = &CodedError{Code: protocol.CodeVersionConflict, Message: "stored document version changed"}
	default:
		coded = &CodedError{Code: protocol.CodeInternalError, Message: "could not persist edit"}
	}
	r.reject(event, coded)
}

func (r *Room) handlePermissionChanged(event permissionChangedEvent) {
	if !r.permissions[event.actorID].CanChangePermissions() {
		event.reply <- &CodedError{Code: protocol.CodePermissionDenied, Message: "owner permission required"}
		return
	}
	if event.userID == "" || !event.role.Valid() {
		event.reply <- &CodedError{Code: protocol.CodeBadMessage, Message: "valid target user and role are required"}
		return
	}

	commitCtx, cancel := r.storageContext(event.ctx)
	err := r.storage.SetPermission(commitCtx, storage.PermissionCommit{
		Authority:  r.authority,
		DocumentID: r.docID,
		ActorID:    event.actorID,
		UserID:     event.userID,
		Role:       event.role,
	})
	cancel()
	if err != nil {
		if errors.Is(err, storage.ErrFenced) {
			event.reply <- &CodedError{Code: protocol.CodeServiceUnavailable, Message: "room ownership is no longer valid"}
			slog.Error("room self fenced", "component", "room", "event", "self_fenced", "doc_id", r.docID, "operation", "permission")
			r.cancel()
			return
		}
		if errors.Is(err, storage.ErrPermissionDenied) {
			event.reply <- &CodedError{Code: protocol.CodePermissionDenied, Message: "owner permission required"}
			return
		}
		event.reply <- &CodedError{Code: protocol.CodeInternalError, Message: "could not persist permission"}
		return
	}

	r.permissions[event.userID] = event.role
	for client := range r.clients {
		if client.Identity() == event.userID && !client.TrySend(protocol.Message{Type: protocol.TypePermissionChanged, DocID: r.docID, Role: event.role}) {
			r.disconnect(client, ErrSlowClient)
		}
	}
	event.reply <- nil
}

func (r *Room) broadcast(message protocol.Message) {
	for client := range r.clients {
		if !client.TrySend(message) {
			r.disconnect(client, ErrSlowClient)
		}
	}
}

func (r *Room) disconnect(client transport.Client, reason error) {
	if _, exists := r.clients[client]; exists {
		r.removeCursor(client)
		delete(r.clients, client)
		r.clientCount.Add(-1)
	}
	if errors.Is(reason, ErrSlowClient) && r.observer != nil {
		r.observer.SlowClientDisconnected()
	}
	client.Close(reason)
}

func (r *Room) ClientCount() int64 { return r.clientCount.Load() }

func (r *Room) EventQueueDepth() int { return len(r.events) }

func (r *Room) closeClients() {
	for client := range r.clients {
		r.disconnect(client, ErrRoomClosed)
	}
}

func (r *Room) rejectClosedEvent(roomEvent event) {
	switch current := roomEvent.(type) {
	case joinEvent:
		current.reply <- ErrRoomClosed
	case resumeEvent:
		current.reply <- ErrRoomClosed
	case leaveEvent:
		current.reply <- struct{}{}
	case editEvent:
		current.reply <- ErrRoomClosed
	case cursorEvent:
		current.reply <- ErrRoomClosed
	case permissionChangedEvent:
		current.reply <- ErrRoomClosed
	case snapshotEvent:
		current.reply <- snapshot{err: ErrRoomClosed}
	case snapshotSavedEvent:
		return
	case snapshotRetryEvent:
		return
	case permissionEvent:
		current.reply <- permissionResult{err: ErrRoomClosed}
	case readDocumentEvent:
		current.reply <- readDocumentResult{err: ErrRoomClosed}
	}
}

func waitError(ctx, roomCtx context.Context, reply <-chan error) error {
	select {
	case err := <-reply:
		return err
	case <-roomCtx.Done():
		return ErrRoomClosed
	case <-ctx.Done():
		return ctx.Err()
	}
}

func resetIdleTimer(timer *time.Timer, timeout time.Duration, idle bool) {
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
	if idle {
		timer.Reset(timeout)
	}
}

func (r *Room) storageContext(requestCtx context.Context) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(requestCtx)
	stop := context.AfterFunc(r.ctx, cancel)
	return ctx, func() {
		stop()
		cancel()
	}
}
