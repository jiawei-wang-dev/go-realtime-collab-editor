package room

import (
	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/storage"
	"sync"
)

type Manager struct {
	mu      sync.RWMutex
	rooms   map[string]*Room
	storage *storage.Memory
}

func NewManager(store *storage.Memory) *Manager {
	return &Manager{rooms: make(map[string]*Room), storage: store}
}

func (m *Manager) Room(docID string) (*Room, error) {
	m.mu.RLock()
	room := m.rooms[docID]
	m.mu.RUnlock()
	if room != nil {
		return room, nil
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if room = m.rooms[docID]; room != nil {
		return room, nil
	}

	document, err := m.storage.Document(docID)
	if err != nil {
		return nil, err
	}
	room = newRoom(
		document,
		m.storage.Operations(docID),
		m.storage.Permissions(docID),
		m.storage,
	)
	m.rooms[docID] = room
	go room.Run()
	return room, nil
}
