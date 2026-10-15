package storage

import (
	"errors"
	"fmt"
	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/collab"
	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/permission"
	"sync"
)

var ErrDocumentNotFound = errors.New("document not found")

type Memory struct {
	mu          sync.RWMutex
	documents   map[string]collab.Document
	operations  map[string][]collab.AppliedOperation
	permissions map[string]map[string]permission.Role
}

func NewMemory() *Memory {
	return &Memory{
		documents:   make(map[string]collab.Document),
		operations:  make(map[string][]collab.AppliedOperation),
		permissions: make(map[string]map[string]permission.Role),
	}
}

func (m *Memory) CreateDocument(id, content, ownerID string) (collab.Document, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if id == "" || ownerID == "" {
		return collab.Document{}, fmt.Errorf("document id and owner id are required")
	}
	if _, exists := m.documents[id]; exists {
		return collab.Document{}, fmt.Errorf("document %q already exists", id)
	}

	document := collab.Document{ID: id, Content: content}
	m.documents[id] = document
	m.permissions[id] = map[string]permission.Role{ownerID: permission.RoleOwner}
	return document, nil
}

func (m *Memory) Document(id string) (collab.Document, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	document, ok := m.documents[id]
	if !ok {
		return collab.Document{}, ErrDocumentNotFound
	}
	return document, nil
}

func (m *Memory) SaveApplied(document collab.Document, operation collab.AppliedOperation) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.documents[document.ID] = document
	m.operations[document.ID] = append(m.operations[document.ID], operation)
}

func (m *Memory) Operations(docID string) []collab.AppliedOperation {
	m.mu.RLock()
	defer m.mu.RUnlock()

	return append([]collab.AppliedOperation(nil), m.operations[docID]...)
}

func (m *Memory) Permissions(docID string) map[string]permission.Role {
	m.mu.RLock()
	defer m.mu.RUnlock()

	result := make(map[string]permission.Role, len(m.permissions[docID]))
	for userID, role := range m.permissions[docID] {
		result[userID] = role
	}
	return result
}

func (m *Memory) SetPermission(docID, userID string, role permission.Role) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.permissions[docID] == nil {
		m.permissions[docID] = make(map[string]permission.Role)
	}
	m.permissions[docID][userID] = role
}
