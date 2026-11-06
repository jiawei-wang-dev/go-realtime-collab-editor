package client

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/collab"
	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/protocol"
)

type fakeConnection struct {
	mu        sync.Mutex
	receive   chan receiveResult
	send      func(protocol.Message)
	sent      []protocol.Message
	closed    atomic.Bool
	authError error
}

func newFakeConnection(send func(protocol.Message)) *fakeConnection {
	return &fakeConnection{receive: make(chan receiveResult, 16), send: send}
}

func (c *fakeConnection) Authenticate(string) error { return c.authError }
func (c *fakeConnection) Send(message protocol.Message) error {
	if c.closed.Load() {
		return errors.New("closed")
	}
	c.mu.Lock()
	c.sent = append(c.sent, message)
	c.mu.Unlock()
	if c.send != nil {
		c.send(message)
	}
	return nil
}
func (c *fakeConnection) Receive() (protocol.Message, error) {
	result, ok := <-c.receive
	if !ok {
		return protocol.Message{}, errors.New("closed")
	}
	return result.message, result.err
}
func (c *fakeConnection) Close() error {
	if c.closed.CompareAndSwap(false, true) {
		close(c.receive)
	}
	return nil
}
func (c *fakeConnection) deliver(message protocol.Message) {
	c.receive <- receiveResult{message: message}
}
func (c *fakeConnection) fail(err error) { c.receive <- receiveResult{err: err} }

func managerToken(t *testing.T) *TokenManager {
	return NewTokenManager(clientToken(t, time.Now().Add(time.Hour)), "", nil, time.Minute)
}

func waitForStatus(t *testing.T, manager *Manager, predicate func(Status) bool) Status {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		status := manager.Status()
		if predicate(status) {
			return status
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("status timeout: %#v", manager.Status())
	return Status{}
}

func TestManagerReconnectsAndRecoversCommittedEditWithOriginalRequestID(t *testing.T) {
	var first, second *fakeConnection
	var dials atomic.Int32
	var editsMu sync.Mutex
	var editIDs []string
	first = newFakeConnection(func(message protocol.Message) {
		switch message.Type {
		case protocol.TypeJoin:
			first.deliver(protocol.Message{Type: protocol.TypeJoined, DocID: "doc", Content: "", Version: 0})
		case protocol.TypeEdit:
			editsMu.Lock()
			editIDs = append(editIDs, message.RequestID)
			editsMu.Unlock()
			first.fail(errors.New("ack lost"))
		}
	})
	second = newFakeConnection(func(message protocol.Message) {
		switch message.Type {
		case protocol.TypeResume:
			second.deliver(protocol.Message{Type: protocol.TypeSync, DocID: "doc", FromVersion: 0, ToVersion: 1, Operations: []protocol.VersionedOperation{{Version: 1, Operation: collab.Operation{Kind: collab.OperationInsert, Position: 0, Text: "X"}}}})
		case protocol.TypeEdit:
			editsMu.Lock()
			editIDs = append(editIDs, message.RequestID)
			editsMu.Unlock()
			operation := collab.Operation{Kind: collab.OperationInsert, Position: 0, Text: "X"}
			second.deliver(protocol.Message{Type: protocol.TypeEditApplied, DocID: "doc", RequestID: message.RequestID, Version: 1, Operation: &operation})
		}
	})
	manager, err := NewManager(Config{
		Dial: func(context.Context) (Connection, error) {
			if dials.Add(1) == 1 {
				return first, nil
			}
			return second, nil
		},
		Tokens: managerToken(t), Backoff: NewBackoff(time.Millisecond, time.Millisecond, 0),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	if err := manager.Open("doc"); err != nil {
		t.Fatal(err)
	}
	waitForStatus(t, manager, func(status Status) bool { return status.State == StateConnected })
	if err := manager.SubmitEdit("same-id", collab.Operation{Kind: collab.OperationInsert, Position: 0, Text: "X"}); err != nil {
		t.Fatal(err)
	}
	waitForStatus(t, manager, func(status Status) bool {
		return status.State == StateConnected && status.ServerVersion == 1 && len(status.Pending) == 0 && dials.Load() >= 2
	})
	content, version, ok := manager.Document()
	if !ok || content != "X" || version != 1 {
		t.Fatalf("document = %q,%d,%t", content, version, ok)
	}
	editsMu.Lock()
	defer editsMu.Unlock()
	if len(editIDs) != 2 || editIDs[0] != "same-id" || editIDs[1] != "same-id" {
		t.Fatalf("edit IDs = %#v", editIDs)
	}
}

func TestManagerRecoversUncommittedPendingEdit(t *testing.T) {
	var first, second *fakeConnection
	var dials atomic.Int32
	first = newFakeConnection(func(message protocol.Message) {
		if message.Type == protocol.TypeJoin {
			first.deliver(protocol.Message{Type: protocol.TypeJoined, DocID: "doc"})
		} else if message.Type == protocol.TypeEdit {
			first.fail(errors.New("not committed"))
		}
	})
	second = newFakeConnection(func(message protocol.Message) {
		if message.Type == protocol.TypeResume {
			second.deliver(protocol.Message{Type: protocol.TypeSync, DocID: "doc", FromVersion: 0, ToVersion: 0})
		} else if message.Type == protocol.TypeEdit {
			operation := *message.Operation
			second.deliver(protocol.Message{Type: protocol.TypeEditApplied, DocID: "doc", RequestID: message.RequestID, Version: 1, Operation: &operation})
		}
	})
	manager, _ := NewManager(Config{Dial: func(context.Context) (Connection, error) {
		if dials.Add(1) == 1 {
			return first, nil
		}
		return second, nil
	}, Tokens: managerToken(t), Backoff: NewBackoff(time.Millisecond, time.Millisecond, 0)})
	defer manager.Close()
	_ = manager.Open("doc")
	waitForStatus(t, manager, func(status Status) bool { return status.State == StateConnected })
	_ = manager.SubmitEdit("pending", collab.Operation{Kind: collab.OperationInsert, Position: 0, Text: "A"})
	waitForStatus(t, manager, func(status Status) bool { return status.ServerVersion == 1 && len(status.Pending) == 0 })
}

func TestManagerRetriesUnavailableNetworkAndCanBeStopped(t *testing.T) {
	var dials atomic.Int32
	connection := newFakeConnection(nil)
	connection.send = func(message protocol.Message) {
		if message.Type == protocol.TypeJoin {
			connection.deliver(protocol.Message{Type: protocol.TypeJoined, DocID: "doc"})
		}
	}
	manager, _ := NewManager(Config{Dial: func(context.Context) (Connection, error) {
		if dials.Add(1) < 3 {
			return nil, errors.New("network unavailable")
		}
		return connection, nil
	}, Tokens: managerToken(t), Backoff: NewBackoff(time.Millisecond, time.Millisecond, 0)})
	defer manager.Close()
	_ = manager.Open("doc")
	waitForStatus(t, manager, func(status Status) bool { return status.State == StateConnected })
	if dials.Load() != 3 {
		t.Fatalf("dials = %d", dials.Load())
	}
	manager.Disconnect()
	waitForStatus(t, manager, func(status Status) bool { return status.State == StateDisconnected })
	count := dials.Load()
	time.Sleep(10 * time.Millisecond)
	if dials.Load() != count {
		t.Fatal("disconnect did not stop reconnecting")
	}
}

func TestRecoveryFailureDoesNotEnterConnected(t *testing.T) {
	connection := newFakeConnection(nil)
	connection.send = func(message protocol.Message) {
		if message.Type == protocol.TypeJoin {
			connection.deliver(protocol.Message{Type: protocol.TypeSync, DocID: "doc", FromVersion: 0, ToVersion: 1})
		}
	}
	manager, _ := NewManager(Config{Dial: func(context.Context) (Connection, error) { return connection, nil }, Tokens: managerToken(t), Backoff: NewBackoff(time.Millisecond, time.Millisecond, 0)})
	defer manager.Close()
	_ = manager.Open("doc")
	status := waitForStatus(t, manager, func(status Status) bool { return status.State == StateDisconnected })
	if status.Authenticated {
		t.Fatal("failed recovery remained authenticated")
	}
}

func TestManagerAppliesSnapshotAndOperationSuffix(t *testing.T) {
	var first, second *fakeConnection
	var dials atomic.Int32
	first = newFakeConnection(func(message protocol.Message) {
		if message.Type == protocol.TypeJoin {
			first.deliver(protocol.Message{Type: protocol.TypeJoined, DocID: "doc", Content: "A", Version: 1})
		}
	})
	second = newFakeConnection(func(message protocol.Message) {
		if message.Type == protocol.TypeResume {
			second.deliver(protocol.Message{
				Type: protocol.TypeResyncRequired, DocID: "doc", Snapshot: &protocol.Snapshot{Version: 2, Content: "AB"}, CurrentVersion: 3,
				Operations: []protocol.VersionedOperation{{Version: 3, Operation: collab.Operation{Kind: collab.OperationInsert, Position: 2, Text: "C"}}},
			})
		}
	})
	manager, _ := NewManager(Config{Dial: func(context.Context) (Connection, error) {
		if dials.Add(1) == 1 {
			return first, nil
		}
		return second, nil
	}, Tokens: managerToken(t), Backoff: NewBackoff(time.Millisecond, time.Millisecond, 0)})
	defer manager.Close()
	_ = manager.Open("doc")
	waitForStatus(t, manager, func(status Status) bool { return status.State == StateConnected && status.ServerVersion == 1 })
	first.fail(errors.New("connection lost"))
	waitForStatus(t, manager, func(status Status) bool { return status.State == StateConnected && status.ServerVersion == 3 })
	content, _, _ := manager.Document()
	if content != "ABC" {
		t.Fatalf("content = %q", content)
	}
}

func TestManagerSendsMultiplePendingOperationsSequentially(t *testing.T) {
	connection := newFakeConnection(nil)
	var mu sync.Mutex
	var requestIDs []string
	connection.send = func(message protocol.Message) {
		switch message.Type {
		case protocol.TypeJoin:
			connection.deliver(protocol.Message{Type: protocol.TypeJoined, DocID: "doc"})
		case protocol.TypeEdit:
			mu.Lock()
			requestIDs = append(requestIDs, message.RequestID)
			mu.Unlock()
			operation := *message.Operation
			connection.deliver(protocol.Message{Type: protocol.TypeEditApplied, DocID: "doc", RequestID: message.RequestID, Version: uint64(len(requestIDs)), Operation: &operation})
		}
	}
	manager, _ := NewManager(Config{Dial: func(context.Context) (Connection, error) { return connection, nil }, Tokens: managerToken(t)})
	defer manager.Close()
	_ = manager.Open("doc")
	waitForStatus(t, manager, func(status Status) bool { return status.State == StateConnected })
	_ = manager.SubmitEdit("one", collab.Operation{Kind: collab.OperationInsert, Position: 0, Text: "A"})
	_ = manager.SubmitEdit("two", collab.Operation{Kind: collab.OperationInsert, Position: 1, Text: "B"})
	waitForStatus(t, manager, func(status Status) bool { return status.ServerVersion == 2 && len(status.Pending) == 0 })
	mu.Lock()
	defer mu.Unlock()
	if len(requestIDs) != 2 || requestIDs[0] != "one" || requestIDs[1] != "two" {
		t.Fatalf("request order = %#v", requestIDs)
	}
}

func TestSubmitEditAtPreservesExplicitStaleBaseVersion(t *testing.T) {
	connection := newFakeConnection(nil)
	baseVersions := make(chan uint64, 1)
	connection.send = func(message protocol.Message) {
		switch message.Type {
		case protocol.TypeJoin:
			connection.deliver(protocol.Message{Type: protocol.TypeJoined, DocID: "doc", Content: "AB", Version: 2})
		case protocol.TypeEdit:
			baseVersions <- message.BaseVersion
			operation := *message.Operation
			connection.deliver(protocol.Message{Type: protocol.TypeEditApplied, DocID: "doc", RequestID: message.RequestID, Version: 3, Operation: &operation})
		}
	}
	manager, _ := NewManager(Config{Dial: func(context.Context) (Connection, error) { return connection, nil }, Tokens: managerToken(t)})
	defer manager.Close()
	_ = manager.Open("doc")
	waitForStatus(t, manager, func(status Status) bool { return status.State == StateConnected })
	if err := manager.SubmitEditAt("stale", 1, collab.Operation{Kind: collab.OperationInsert, Position: 2, Text: "X"}); err != nil {
		t.Fatal(err)
	}
	select {
	case base := <-baseVersions:
		if base != 1 {
			t.Fatalf("base_version = %d", base)
		}
	case <-time.After(time.Second):
		t.Fatal("edit was not sent")
	}
}

func TestInvalidAccessTokenDoesNotRetryForever(t *testing.T) {
	var dials atomic.Int32
	manager, _ := NewManager(Config{
		Dial:   func(context.Context) (Connection, error) { dials.Add(1); return nil, errors.New("unexpected dial") },
		Tokens: NewTokenManager("malformed", "", nil, time.Minute), Backoff: NewBackoff(time.Millisecond, time.Millisecond, 0),
	})
	defer manager.Close()
	_ = manager.Open("doc")
	waitForStatus(t, manager, func(status Status) bool { return status.State == StateDisconnected && status.ReconnectAttempts > 0 })
	time.Sleep(10 * time.Millisecond)
	if dials.Load() != 0 {
		t.Fatalf("invalid token attempted %d dials", dials.Load())
	}
}

func TestManagerFollowsAuthenticatedOwnerRedirect(t *testing.T) {
	seed := newFakeConnection(nil)
	seed.send = func(message protocol.Message) {
		if message.Type == protocol.TypeJoin {
			seed.deliver(protocol.Message{Type: protocol.TypeOwnerRedirect, DocID: "doc", Endpoint: "owner.example:8443"})
		}
	}
	owner := newFakeConnection(nil)
	owner.send = func(message protocol.Message) {
		if message.Type == protocol.TypeJoin {
			owner.deliver(protocol.Message{Type: protocol.TypeJoined, DocID: "doc", Content: "ready", Version: 4})
		}
	}
	var mu sync.Mutex
	var endpoints []string
	manager, err := NewManager(Config{
		SeedEndpoint: "seed.example:8443",
		DialEndpoint: func(_ context.Context, endpoint string) (Connection, error) {
			mu.Lock()
			endpoints = append(endpoints, endpoint)
			mu.Unlock()
			if endpoint == "seed.example:8443" {
				return seed, nil
			}
			return owner, nil
		},
		Tokens: managerToken(t), Backoff: NewBackoff(time.Millisecond, time.Millisecond, 0),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	if err := manager.Open("doc"); err != nil {
		t.Fatal(err)
	}
	status := waitForStatus(t, manager, func(status Status) bool { return status.State == StateConnected })
	if status.OwnerEndpoint != "owner.example:8443" {
		t.Fatalf("owner endpoint = %q", status.OwnerEndpoint)
	}
	content, version, _ := manager.Document()
	if content != "ready" || version != 4 {
		t.Fatalf("document = %q version %d", content, version)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(endpoints) != 2 || endpoints[0] != "seed.example:8443" || endpoints[1] != "owner.example:8443" {
		t.Fatalf("dial endpoints = %#v", endpoints)
	}
}

func TestManagerRetriesRecoveringOwnerThroughSeedWithoutChangingPendingID(t *testing.T) {
	var seedDials atomic.Int32
	var ownerDials atomic.Int32
	requestIDs := make(chan string, 1)
	manager, err := NewManager(Config{
		SeedEndpoint: "seed.example:8443",
		DialEndpoint: func(_ context.Context, endpoint string) (Connection, error) {
			connection := newFakeConnection(nil)
			switch endpoint {
			case "seed.example:8443":
				attempt := seedDials.Add(1)
				connection.send = func(message protocol.Message) {
					if message.Type == protocol.TypeJoin && attempt == 1 {
						connection.deliver(protocol.Message{Type: protocol.TypeOwnerRedirect, DocID: "doc", Endpoint: "owner.example:8443"})
					} else if message.Type == protocol.TypeJoin {
						connection.deliver(protocol.Message{Type: protocol.TypeJoined, DocID: "doc"})
					} else if message.Type == protocol.TypeEdit {
						requestIDs <- message.RequestID
						operation := *message.Operation
						connection.deliver(protocol.Message{Type: protocol.TypeEditApplied, DocID: "doc", RequestID: message.RequestID, Version: 1, Operation: &operation})
					}
				}
			case "owner.example:8443":
				ownerDials.Add(1)
				connection.send = func(message protocol.Message) {
					if message.Type == protocol.TypeJoin {
						connection.deliver(protocol.Error(protocol.CodeServiceUnavailable, "owner is recovering"))
					}
				}
			default:
				return nil, fmt.Errorf("unexpected endpoint %q", endpoint)
			}
			return connection, nil
		},
		Tokens: managerToken(t), Backoff: NewBackoff(time.Millisecond, time.Millisecond, 0),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	if err := manager.Open("doc"); err != nil {
		t.Fatal(err)
	}
	if err := manager.SubmitEdit("stable-request-id", collab.Operation{Kind: collab.OperationInsert, Position: 0, Text: "X"}); err != nil {
		t.Fatal(err)
	}
	waitForStatus(t, manager, func(status Status) bool {
		return status.State == StateConnected && status.ServerVersion == 1 && len(status.Pending) == 0
	})
	if seedDials.Load() < 2 || ownerDials.Load() != 1 {
		t.Fatalf("seed dials = %d, owner dials = %d", seedDials.Load(), ownerDials.Load())
	}
	select {
	case requestID := <-requestIDs:
		if requestID != "stable-request-id" {
			t.Fatalf("request id = %q", requestID)
		}
	default:
		t.Fatal("pending edit was not sent after recovery")
	}
}

func TestManagerRejectsRedirectLoopAndInvalidEndpoint(t *testing.T) {
	tests := []struct {
		name     string
		endpoint string
		want     error
	}{
		{name: "loop", endpoint: "seed.example:8443", want: ErrRedirectLoop},
		{name: "invalid", endpoint: "https://attacker.example:8443/path", want: ErrInvalidRedirect},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dialStarted := make(chan struct{})
			allowDial := make(chan struct{})
			terminalStatus := make(chan Status, 1)
			var dialOnce sync.Once
			var dialCount atomic.Int32
			connection := newFakeConnection(nil)
			connection.send = func(message protocol.Message) {
				if message.Type == protocol.TypeJoin {
					connection.deliver(protocol.Message{Type: protocol.TypeOwnerRedirect, DocID: "doc", Endpoint: test.endpoint})
				}
			}
			manager, err := NewManager(Config{
				SeedEndpoint: "seed.example:8443",
				DialEndpoint: func(ctx context.Context, endpoint string) (Connection, error) {
					if endpoint != "seed.example:8443" {
						t.Errorf("dialed untrusted redirect endpoint %q", endpoint)
					}
					dialCount.Add(1)
					dialOnce.Do(func() { close(dialStarted) })
					select {
					case <-allowDial:
						return connection, nil
					case <-ctx.Done():
						return nil, ctx.Err()
					}
				},
				Tokens: managerToken(t), Backoff: NewBackoff(time.Millisecond, time.Millisecond, 0),
				OnStateChange: func(status Status) {
					if status.State == StateDisconnected && status.LastDisconnectReason != "" {
						select {
						case terminalStatus <- status:
						default:
						}
					}
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			defer manager.Close()
			if err := manager.Open("doc"); err != nil {
				t.Fatal(err)
			}
			select {
			case <-dialStarted:
			case <-time.After(time.Second):
				t.Fatal("connection attempt did not start")
			}
			if err := manager.SubmitEditAt("redirect-pending", 0, collab.Operation{Kind: collab.OperationInsert, Position: 0, Text: "X"}); err != nil {
				t.Fatal(err)
			}
			close(allowDial)
			var status Status
			select {
			case status = <-terminalStatus:
			case <-time.After(time.Second):
				t.Fatal("terminal redirect status was not published")
			}
			if !errors.Is(errors.New(status.LastDisconnectReason), test.want) && !strings.Contains(status.LastDisconnectReason, test.want.Error()) {
				t.Fatalf("disconnect reason = %q, want %v", status.LastDisconnectReason, test.want)
			}
			if dialCount.Load() != 1 {
				t.Fatalf("dial count = %d, want 1", dialCount.Load())
			}
			if len(status.Pending) != 1 || status.Pending[0].RequestID != "redirect-pending" || status.Pending[0].State != PendingQueued {
				t.Fatalf("pending operation changed during redirect rejection: %#v", status.Pending)
			}
			manager.mu.Lock()
			automaticReconnect := manager.automaticReconnect
			manager.mu.Unlock()
			if automaticReconnect {
				t.Fatal("terminal redirect error left automatic reconnect enabled")
			}
		})
	}
}
