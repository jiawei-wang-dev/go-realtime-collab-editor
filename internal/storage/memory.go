package storage

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/collab"
	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/permission"
)

type Memory struct {
	mu          sync.RWMutex
	documents   map[string]collab.Document
	operations  map[string][]collab.AppliedOperation
	permissions map[string]map[string]permission.Role
	requests    map[string]map[string]struct{}
	snapshots   map[string]map[uint64]Snapshot
	authorities map[string]WriteAuthority
	updatedAt   map[string]time.Time
}

var _ Store = (*Memory)(nil)

func NewMemory() *Memory {
	return &Memory{
		documents:   make(map[string]collab.Document),
		operations:  make(map[string][]collab.AppliedOperation),
		permissions: make(map[string]map[string]permission.Role),
		requests:    make(map[string]map[string]struct{}),
		snapshots:   make(map[string]map[uint64]Snapshot),
		authorities: make(map[string]WriteAuthority),
		updatedAt:   make(map[string]time.Time),
	}
}

func (m *Memory) GetWriteAuthority(ctx context.Context, docID string) (WriteAuthority, error) {
	if err := ctx.Err(); err != nil {
		return WriteAuthority{}, err
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	if _, ok := m.documents[docID]; !ok {
		return WriteAuthority{}, ErrNotFound
	}
	return m.authorities[docID], nil
}

func (m *Memory) ActivateOwnership(ctx context.Context, docID string, authority WriteAuthority) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := ValidateWriteAuthority(authority); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.documents[docID]; !ok {
		return ErrNotFound
	}
	if authority.FencingToken <= m.authorities[docID].FencingToken {
		return ErrOwnershipConflict
	}
	m.authorities[docID] = authority
	return nil
}

func (m *Memory) CreateDocument(ctx context.Context, id, content, ownerID string) (collab.Document, error) {
	if err := ctx.Err(); err != nil {
		return collab.Document{}, err
	}
	if id == "" || ownerID == "" {
		return collab.Document{}, fmt.Errorf("document id and owner id are required")
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.documents[id]; exists {
		return collab.Document{}, ErrAlreadyExists
	}
	document := collab.Document{ID: id, Content: content}
	m.documents[id] = document
	m.permissions[id] = map[string]permission.Role{ownerID: permission.RoleOwner}
	m.requests[id] = make(map[string]struct{})
	m.snapshots[id] = map[uint64]Snapshot{0: {DocumentID: id, Version: 0, Content: content}}
	m.updatedAt[id] = time.Now().UTC()
	return document, nil
}

func (m *Memory) ListDocuments(ctx context.Context, userID string) ([]DocumentSummary, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make([]DocumentSummary, 0)
	for id, document := range m.documents {
		if role := m.permissions[id][userID]; role.CanRead() {
			result = append(result, DocumentSummary{ID: id, Version: document.Version, Role: role, UpdatedAt: m.updatedAt[id]})
		}
	}
	return result, nil
}

func (m *Memory) DeleteDocument(ctx context.Context, commit DeleteDocumentCommit) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := ValidateWriteAuthority(commit.Authority); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.documents[commit.DocumentID]; !ok {
		return ErrNotFound
	}
	if m.authorities[commit.DocumentID] != commit.Authority {
		return ErrFenced
	}
	if !m.permissions[commit.DocumentID][commit.ActorID].CanChangePermissions() {
		return ErrPermissionDenied
	}
	delete(m.documents, commit.DocumentID)
	delete(m.operations, commit.DocumentID)
	delete(m.permissions, commit.DocumentID)
	delete(m.requests, commit.DocumentID)
	delete(m.snapshots, commit.DocumentID)
	delete(m.authorities, commit.DocumentID)
	delete(m.updatedAt, commit.DocumentID)
	return nil
}

func (m *Memory) LoadDocument(ctx context.Context, id string) (DocumentState, error) {
	if err := ctx.Err(); err != nil {
		return DocumentState{}, err
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	document, ok := m.documents[id]
	if !ok {
		return DocumentState{}, ErrNotFound
	}
	return DocumentState{
		Document:    document,
		Operations:  cloneOperations(m.operations[id]),
		Permissions: clonePermissions(m.permissions[id]),
	}, nil
}

func (m *Memory) LoadRecoveryState(ctx context.Context, id string, authority WriteAuthority) (RecoveryState, error) {
	if err := ctx.Err(); err != nil {
		return RecoveryState{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	document, ok := m.documents[id]
	if !ok {
		return RecoveryState{}, ErrNotFound
	}
	if m.authorities[id] != authority || !authority.Valid() {
		return RecoveryState{}, ErrFenced
	}
	snapshot := m.latestSnapshotLocked(id)
	if snapshot.DocumentID == "" {
		snapshot = Snapshot{DocumentID: id, Version: document.Version, Content: document.Content}
		if m.snapshots[id] == nil {
			m.snapshots[id] = make(map[uint64]Snapshot)
		}
		m.snapshots[id][snapshot.Version] = snapshot
	}
	return RecoveryState{
		Document:    document,
		Snapshot:    snapshot,
		Operations:  operationsAfter(m.operations[id], snapshot.Version),
		Permissions: clonePermissions(m.permissions[id]),
	}, nil
}

func (m *Memory) GetDocument(ctx context.Context, id string) (collab.Document, error) {
	state, err := m.LoadDocument(ctx, id)
	return state.Document, err
}

func (m *Memory) GetPermission(ctx context.Context, docID, userID string) (permission.Role, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	if _, ok := m.documents[docID]; !ok {
		return "", ErrNotFound
	}
	role, ok := m.permissions[docID][userID]
	if !ok {
		return "", ErrNotFound
	}
	return role, nil
}

func (m *Memory) GetPermissions(ctx context.Context, docID string) (map[string]permission.Role, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	if _, ok := m.documents[docID]; !ok {
		return nil, ErrNotFound
	}
	return clonePermissions(m.permissions[docID]), nil
}

func (m *Memory) SetPermission(ctx context.Context, commit PermissionCommit) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := ValidateWriteAuthority(commit.Authority); err != nil {
		return err
	}
	if commit.UserID == "" || !commit.Role.Valid() {
		return fmt.Errorf("valid target user and role are required")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.documents[commit.DocumentID]; !ok {
		return ErrNotFound
	}
	if m.authorities[commit.DocumentID] != commit.Authority {
		return ErrFenced
	}
	if !m.permissions[commit.DocumentID][commit.ActorID].CanChangePermissions() {
		return ErrPermissionDenied
	}
	m.permissions[commit.DocumentID][commit.UserID] = commit.Role
	return nil
}

func (m *Memory) CommitEdit(ctx context.Context, commit EditCommit) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := ValidateWriteAuthority(commit.Authority); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	current, ok := m.documents[commit.Document.ID]
	if !ok {
		return ErrNotFound
	}
	if m.authorities[commit.Document.ID] != commit.Authority {
		return ErrFenced
	}
	if _, exists := m.requests[commit.Document.ID][commit.Operation.ID]; exists {
		return ErrDuplicateRequest
	}
	if current.Version != commit.ExpectedVersion ||
		commit.Document.Version != commit.ExpectedVersion+1 ||
		commit.Operation.Version != commit.Document.Version {
		return ErrVersionConflict
	}

	m.documents[commit.Document.ID] = commit.Document
	m.updatedAt[commit.Document.ID] = time.Now().UTC()
	m.operations[commit.Document.ID] = append(m.operations[commit.Document.ID], commit.Operation)
	if m.requests[commit.Document.ID] == nil {
		m.requests[commit.Document.ID] = make(map[string]struct{})
	}
	m.requests[commit.Document.ID][commit.Operation.ID] = struct{}{}
	return nil
}

func (m *Memory) LoadOperations(ctx context.Context, docID string) ([]collab.AppliedOperation, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	if _, ok := m.documents[docID]; !ok {
		return nil, ErrNotFound
	}
	return cloneOperations(m.operations[docID]), nil
}

func (m *Memory) LoadOperationsAfter(ctx context.Context, docID string, version uint64) ([]collab.AppliedOperation, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	if _, ok := m.documents[docID]; !ok {
		return nil, ErrNotFound
	}
	return operationsAfter(m.operations[docID], version), nil
}

func (m *Memory) GetAppliedOperation(ctx context.Context, docID, requestID string) (collab.AppliedOperation, error) {
	if err := ctx.Err(); err != nil {
		return collab.AppliedOperation{}, err
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	if _, ok := m.documents[docID]; !ok {
		return collab.AppliedOperation{}, ErrNotFound
	}
	for _, applied := range m.operations[docID] {
		if applied.ID == requestID {
			return applied, nil
		}
	}
	return collab.AppliedOperation{}, ErrNotFound
}

func (m *Memory) GetLatestSnapshot(ctx context.Context, docID string) (Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return Snapshot{}, err
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	if _, ok := m.documents[docID]; !ok {
		return Snapshot{}, ErrNotFound
	}
	snapshot := m.latestSnapshotLocked(docID)
	if snapshot.DocumentID == "" {
		return Snapshot{}, ErrNotFound
	}
	return snapshot, nil
}

func (m *Memory) EnsureBaselineSnapshot(ctx context.Context, docID string, authority WriteAuthority) (Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return Snapshot{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	document, ok := m.documents[docID]
	if !ok {
		return Snapshot{}, ErrNotFound
	}
	if m.authorities[docID] != authority || !authority.Valid() {
		return Snapshot{}, ErrFenced
	}
	if snapshot := m.latestSnapshotLocked(docID); snapshot.DocumentID != "" {
		return snapshot, nil
	}
	snapshot := Snapshot{DocumentID: docID, Version: document.Version, Content: document.Content}
	if m.snapshots[docID] == nil {
		m.snapshots[docID] = make(map[uint64]Snapshot)
	}
	m.snapshots[docID][snapshot.Version] = snapshot
	return snapshot, nil
}

func (m *Memory) SaveSnapshot(ctx context.Context, commit SnapshotCommit) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := ValidateWriteAuthority(commit.Authority); err != nil {
		return err
	}
	snapshot := commit.Snapshot
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.documents[snapshot.DocumentID]; !ok {
		return ErrNotFound
	}
	if m.authorities[snapshot.DocumentID] != commit.Authority {
		return ErrFenced
	}
	if snapshot.Version > m.documents[snapshot.DocumentID].Version {
		return ErrVersionConflict
	}
	if m.snapshots[snapshot.DocumentID] == nil {
		m.snapshots[snapshot.DocumentID] = make(map[uint64]Snapshot)
	}
	if _, exists := m.snapshots[snapshot.DocumentID][snapshot.Version]; exists {
		return ErrAlreadyExists
	}
	m.snapshots[snapshot.DocumentID][snapshot.Version] = snapshot
	return nil
}

func (m *Memory) latestSnapshotLocked(docID string) Snapshot {
	var latest Snapshot
	for version, snapshot := range m.snapshots[docID] {
		if latest.DocumentID == "" || version > latest.Version {
			latest = snapshot
		}
	}
	return latest
}

func operationsAfter(source []collab.AppliedOperation, version uint64) []collab.AppliedOperation {
	result := make([]collab.AppliedOperation, 0, len(source))
	for _, applied := range source {
		if applied.Version > version {
			result = append(result, applied)
		}
	}
	return result
}

func (m *Memory) Close() error { return nil }

func cloneOperations(source []collab.AppliedOperation) []collab.AppliedOperation {
	return append([]collab.AppliedOperation(nil), source...)
}

func clonePermissions(source map[string]permission.Role) map[string]permission.Role {
	result := make(map[string]permission.Role, len(source))
	for userID, role := range source {
		result[userID] = role
	}
	return result
}
