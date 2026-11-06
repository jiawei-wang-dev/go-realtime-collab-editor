package room

import (
	"sync"

	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/protocol"
)

// Client is the channel-backed transport used by in-process callers and tests.
// Network transports implement transport.Client directly.
type Client struct {
	UserID string
	Send   chan protocol.Message

	mu       sync.RWMutex
	closed   bool
	closeErr error
}

func (client *Client) Identity() string { return client.UserID }

func (client *Client) TrySend(message protocol.Message) bool {
	client.mu.RLock()
	defer client.mu.RUnlock()
	if client.closed {
		return false
	}
	select {
	case client.Send <- message:
		return true
	default:
		return false
	}
}

func (client *Client) Close(reason error) {
	client.mu.Lock()
	defer client.mu.Unlock()
	if client.closed {
		return
	}
	client.closed = true
	client.closeErr = reason
	close(client.Send)
}
