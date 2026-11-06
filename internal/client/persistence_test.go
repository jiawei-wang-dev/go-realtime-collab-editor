package client

import (
	"context"
	"os"
	"os/exec"
	"sync/atomic"
	"testing"
	"time"

	clientstorage "github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/client/storage"
	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/collab"
	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/protocol"
)

func TestManagerRestoresSQLitePendingWithOriginalRequestID(t *testing.T) {
	directory := t.TempDir()
	firstStore, err := clientstorage.Open(context.Background(), directory, "profile")
	if err != nil {
		t.Fatal(err)
	}
	firstConnection := newFakeConnection(nil)
	firstConnection.send = func(message protocol.Message) {
		if message.Type == protocol.TypeJoin {
			firstConnection.deliver(protocol.Message{Type: protocol.TypeJoined, DocID: "doc", Content: "", Version: 0})
		}
	}
	first, _ := NewManager(Config{Dial: func(context.Context) (Connection, error) { return firstConnection, nil }, Tokens: managerToken(t), StateStore: firstStore})
	if err := first.Open("doc"); err != nil {
		t.Fatal(err)
	}
	waitForStatus(t, first, func(status Status) bool { return status.State == StateConnected })
	first.Disconnect()
	waitForStatus(t, first, func(status Status) bool { return status.State == StateDisconnected })
	if err := first.SubmitEditAt("survives-crash", 0, collab.Operation{Kind: collab.OperationInsert, Position: 0, Text: "A"}); err != nil {
		t.Fatal(err)
	}
	_ = first.Close()
	_ = firstStore.Close()

	secondStore, err := clientstorage.Open(context.Background(), directory, "profile")
	if err != nil {
		t.Fatal(err)
	}
	defer secondStore.Close()
	secondConnection := newFakeConnection(nil)
	requestIDs := make(chan string, 1)
	secondConnection.send = func(message protocol.Message) {
		switch message.Type {
		case protocol.TypeResume:
			secondConnection.deliver(protocol.Message{Type: protocol.TypeSync, DocID: "doc", FromVersion: 0, ToVersion: 0})
		case protocol.TypeEdit:
			requestIDs <- message.RequestID
			operation := *message.Operation
			secondConnection.deliver(protocol.Message{Type: protocol.TypeEditApplied, DocID: "doc", RequestID: message.RequestID, Version: 1, Operation: &operation})
		}
	}
	second, err := NewManager(Config{Dial: func(context.Context) (Connection, error) { return secondConnection, nil }, Tokens: managerToken(t), StateStore: secondStore})
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	waitForStatus(t, second, func(status Status) bool {
		return status.State == StateConnected && status.ServerVersion == 1 && len(status.Pending) == 0
	})
	select {
	case requestID := <-requestIDs:
		if requestID != "survives-crash" {
			t.Fatalf("request_id = %q", requestID)
		}
	case <-time.After(time.Second):
		t.Fatal("restored edit was not sent")
	}
	recovery, err := secondStore.Recovery(context.Background())
	if err != nil || recovery.Document == nil || recovery.Document.Content != "A" || recovery.Document.ServerVersion != 1 || len(recovery.Pending) != 0 {
		t.Fatalf("durable recovery = %#v, %v", recovery, err)
	}
}

func TestSQLiteFailurePreventsEditSend(t *testing.T) {
	store, err := clientstorage.Open(context.Background(), t.TempDir(), "profile")
	if err != nil {
		t.Fatal(err)
	}
	connection := newFakeConnection(nil)
	var edits atomic.Int32
	connection.send = func(message protocol.Message) {
		if message.Type == protocol.TypeJoin {
			connection.deliver(protocol.Message{Type: protocol.TypeJoined, DocID: "doc"})
		} else if message.Type == protocol.TypeEdit {
			edits.Add(1)
		}
	}
	manager, _ := NewManager(Config{Dial: func(context.Context) (Connection, error) { return connection, nil }, Tokens: managerToken(t), StateStore: store})
	_ = manager.Open("doc")
	waitForStatus(t, manager, func(status Status) bool { return status.State == StateConnected })
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if err := manager.SubmitEdit("not-sent", collab.Operation{Kind: collab.OperationInsert, Position: 0, Text: "X"}); err == nil {
		t.Fatal("edit succeeded after SQLite failure")
	}
	time.Sleep(10 * time.Millisecond)
	if edits.Load() != 0 {
		t.Fatal("edit was sent before local persistence")
	}
	_ = manager.Close()
}

func TestProcessExitReleasesProfileLockAndPreservesPending(t *testing.T) {
	if os.Getenv("COLLAB_CRASH_HELPER") == "1" {
		store, err := clientstorage.Open(context.Background(), os.Getenv("COLLAB_CRASH_DIR"), "crash-profile")
		if err != nil {
			os.Exit(2)
		}
		_ = store.SetActiveDocument(context.Background(), "doc")
		_ = store.SaveDocument(context.Background(), clientstorage.Document{DocID: "doc"})
		_ = store.AddPending(context.Background(), clientstorage.PendingEdit{
			DocID: "doc", RequestID: "crash-request", Operation: collab.Operation{Kind: collab.OperationInsert, Position: 0, Text: "X"}, Status: string(PendingQueued),
		})
		// Deliberately skip Close and all defers to model a process crash.
		os.Exit(0)
	}
	directory := t.TempDir()
	command := exec.Command(os.Args[0], "-test.run=TestProcessExitReleasesProfileLockAndPreservesPending")
	command.Env = append(os.Environ(), "COLLAB_CRASH_HELPER=1", "COLLAB_CRASH_DIR="+directory)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("crash helper: %v: %s", err, output)
	}
	store, err := clientstorage.Open(context.Background(), directory, "crash-profile")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	recovery, err := store.Recovery(context.Background())
	if err != nil || len(recovery.Pending) != 1 || recovery.Pending[0].RequestID != "crash-request" {
		t.Fatalf("post-crash recovery = %#v, %v", recovery, err)
	}
}
