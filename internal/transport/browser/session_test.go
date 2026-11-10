package browser

import (
	"context"
	"io"
	"sync"
	"testing"

	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/protocol"
)

func TestFinishDrainsTerminalMessageBeforeClosingConnection(t *testing.T) {
	connection := &recordingMessageConn{}
	client := &sessionClient{
		connection: connection,
		send:       make(chan protocol.Message, 1),
		writeDone:  make(chan struct{}),
	}
	go client.writeLoop(context.Background())
	if !client.TrySend(protocol.Error(protocol.CodePermissionDenied, "invalid session")) {
		t.Fatal("terminal error was not queued")
	}

	client.finish()

	connection.mu.Lock()
	defer connection.mu.Unlock()
	if len(connection.messages) != 1 || connection.messages[0].Code != protocol.CodePermissionDenied {
		t.Fatalf("written messages = %#v", connection.messages)
	}
	if !connection.closed {
		t.Fatal("connection remained open after writer drained")
	}
}

type recordingMessageConn struct {
	mu       sync.Mutex
	messages []protocol.Message
	closed   bool
}

func (connection *recordingMessageConn) Read(context.Context, *protocol.Message) error {
	return io.EOF
}

func (connection *recordingMessageConn) Write(_ context.Context, message protocol.Message) error {
	connection.mu.Lock()
	defer connection.mu.Unlock()
	if connection.closed {
		return io.ErrClosedPipe
	}
	connection.messages = append(connection.messages, message)
	return nil
}

func (connection *recordingMessageConn) CloseNow() error {
	connection.mu.Lock()
	connection.closed = true
	connection.mu.Unlock()
	return nil
}
