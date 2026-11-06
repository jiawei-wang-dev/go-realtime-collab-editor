package quictransport

import (
	"bytes"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/authentication"
	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/protocol"
	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/transport"

	quic "github.com/quic-go/quic-go"
)

func TestClientImplementsTransportAndNonBlockingQueue(t *testing.T) {
	var _ transport.Client = (*Client)(nil)
	client := &Client{
		principal:  authentication.Principal{UserID: "user"},
		send:       make(chan protocol.Message, 1),
		connection: &fakeConnection{},
		stream:     &fakeStream{},
	}
	if client.Identity() != "user" {
		t.Fatalf("identity = %q", client.Identity())
	}
	if !client.TrySend(protocol.Message{Type: protocol.TypeAuthOK}) {
		t.Fatal("first TrySend failed")
	}
	start := time.Now()
	if client.TrySend(protocol.Message{Type: protocol.TypeAuthOK}) {
		t.Fatal("full queue accepted a message")
	}
	if time.Since(start) > 50*time.Millisecond {
		t.Fatal("TrySend blocked on a full queue")
	}
}

func TestClientConcurrentCloseIsSafe(t *testing.T) {
	connection := &fakeConnection{}
	stream := &fakeStream{}
	client := &Client{
		principal: authentication.Principal{UserID: "user"}, send: make(chan protocol.Message, 1),
		connection: connection, stream: stream,
	}
	var wait sync.WaitGroup
	for range 100 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			client.Close(errors.New("closed"))
		}()
	}
	wait.Wait()
	if connection.closes.Load() != 1 || stream.closes.Load() != 1 {
		t.Fatalf("connection closes=%d stream closes=%d", connection.closes.Load(), stream.closes.Load())
	}
	if client.TrySend(protocol.Message{Type: protocol.TypeAuthOK}) {
		t.Fatal("closed client accepted message")
	}
}

type fakeConnection struct{ closes atomic.Int32 }

func (c *fakeConnection) CloseWithError(quic.ApplicationErrorCode, string) error {
	c.closes.Add(1)
	return nil
}

type fakeStream struct {
	bytes.Buffer
	closes atomic.Int32
}

func (s *fakeStream) Close() error                     { s.closes.Add(1); return nil }
func (s *fakeStream) CancelRead(quic.StreamErrorCode)  {}
func (s *fakeStream) CancelWrite(quic.StreamErrorCode) {}
func (s *fakeStream) SetReadDeadline(time.Time) error  { return nil }
func (s *fakeStream) SetWriteDeadline(time.Time) error { return nil }
