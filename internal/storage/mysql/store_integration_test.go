package mysql_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/collab"
	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/migration"
	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/permission"
	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/protocol"
	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/room"
	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/storage"
	mysqlstorage "github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/storage/mysql"
)

var documentSequence atomic.Uint64

func TestMySQLFolderSharingFollowsFilesAndRevocation(t *testing.T) {
	store := openIntegrationStore(t)
	ctx := context.Background()
	owner, viewer := uniqueDocumentID(), uniqueDocumentID()
	root := storage.Folder{ID: uniqueDocumentID(), Name: "Project"}
	child := storage.Folder{ID: uniqueDocumentID(), ParentID: root.ID, Name: "Source"}
	if err := store.CreateFolder(ctx, root, owner); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateFolder(ctx, child, owner); err != nil {
		t.Fatal(err)
	}
	if err := store.SetFolderPermission(ctx, root.ID, owner, viewer, permission.RoleWrite); err != nil {
		t.Fatal(err)
	}
	docID := uniqueDocumentID()
	if _, err := store.CreateFolderDocument(ctx, docID, "", owner, child.ID, "main.go"); err != nil {
		t.Fatal(err)
	}
	folders, err := store.ListFolders(ctx, viewer)
	if err != nil || len(folders) != 2 {
		t.Fatalf("shared folders = %#v, err = %v", folders, err)
	}
	docs, err := store.ListDocuments(ctx, viewer)
	if err != nil || len(docs) != 1 || docs[0].Role != permission.RoleWrite || docs[0].FolderID != child.ID {
		t.Fatalf("shared documents = %#v, err = %v", docs, err)
	}
	if err := store.SetFolderPermission(ctx, root.ID, viewer, owner, permission.RoleRead); !errors.Is(err, storage.ErrPermissionDenied) {
		t.Fatalf("writer modifying shares: %v", err)
	}
	manager := room.NewManager(store)
	t.Cleanup(func() { _ = manager.Shutdown(context.Background()) })
	target, err := manager.Room(docID)
	if err != nil {
		t.Fatal(err)
	}
	client := integrationClient(t, target, viewer)
	if err := store.SetFolderPermission(ctx, root.ID, owner, viewer, permission.RoleRead); err != nil {
		t.Fatal(err)
	}
	if err := target.SubmitEdit(client, "room-downgrade", collab.Operation{
		Kind: collab.OperationInsert, Text: "blocked",
	}); err == nil {
		t.Fatal("room accepted edit after downgrade")
	}
	edit := integrationCommit(docID, "after-downgrade", 0, "blocked")
	edit.Operation.UserID = viewer
	if err := store.CommitEdit(ctx, edit); !errors.Is(err, storage.ErrPermissionDenied) {
		t.Fatalf("write after downgrade: %v", err)
	}
	changes, err := store.PreviewDocumentMove(ctx, docID, owner, "")
	if err != nil || len(changes) != 1 || changes[0] != (storage.AccessChange{UserID: viewer, Before: permission.RoleRead}) {
		t.Fatalf("move preview = %#v, err = %v", changes, err)
	}
	if err := store.MoveDocument(ctx, docID, owner, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetPermission(ctx, docID, viewer); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("permission after move: %v", err)
	}
	if err := store.MoveDocument(ctx, docID, owner, child.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.SetFolderPermission(ctx, root.ID, owner, viewer, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetPermission(ctx, docID, viewer); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("permission after revoke: %v", err)
	}
}

func TestMySQLCreateReadAndOwnerPermission(t *testing.T) {
	store := openIntegrationStore(t)
	docID := uniqueDocumentID()
	document, err := store.CreateDocument(context.Background(), docID, "abc", "owner")
	if err != nil {
		t.Fatal(err)
	}
	if document.Version != 0 {
		t.Fatalf("created version = %d", document.Version)
	}
	authority, err := store.GetWriteAuthority(context.Background(), docID)
	if err != nil {
		t.Fatal(err)
	}
	if !authority.IsZero() {
		t.Fatalf("new document authority = %#v, want unowned", authority)
	}
	loaded, err := store.GetDocument(context.Background(), docID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded != document {
		t.Fatalf("loaded = %#v, want %#v", loaded, document)
	}
	role, err := store.GetPermission(context.Background(), docID, "owner")
	if err != nil {
		t.Fatal(err)
	}
	if role != permission.RoleOwner {
		t.Fatalf("owner role = %q", role)
	}
	snapshot, err := store.GetLatestSnapshot(context.Background(), docID)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Version != 0 || snapshot.Content != "abc" {
		t.Fatalf("initial snapshot = %#v", snapshot)
	}
}

func TestMySQLSnapshotAndOperationReplay(t *testing.T) {
	store := openIntegrationStore(t)
	docID := uniqueDocumentID()
	if _, err := store.CreateDocument(context.Background(), docID, "a", "owner"); err != nil {
		t.Fatal(err)
	}
	activateAuthority(t, store, docID, storage.LocalAuthority)
	first := integrationCommit(docID, "replay-1", 0, "aX")
	first.Operation.Position = 1
	first.Operation.Text = "X"
	if err := store.CommitEdit(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveSnapshot(context.Background(), storage.SnapshotCommit{Authority: storage.LocalAuthority, Snapshot: storage.Snapshot{DocumentID: docID, Version: 1, Content: "aX"}}); err != nil {
		t.Fatal(err)
	}
	second := integrationCommit(docID, "replay-2", 1, "aXY")
	second.Operation.Position = 2
	second.Operation.Text = "Y"
	if err := store.CommitEdit(context.Background(), second); err != nil {
		t.Fatal(err)
	}
	state, err := store.LoadRecoveryState(context.Background(), docID, storage.LocalAuthority)
	if err != nil {
		t.Fatal(err)
	}
	if state.Snapshot.Version != 1 || len(state.Operations) != 1 || state.Operations[0].Version != 2 {
		t.Fatalf("recovery state = %#v", state)
	}
	if err := storage.ValidateRecoveryState(state); err != nil {
		t.Fatal(err)
	}
}

func TestMySQLEnsureBaselineForLegacyDocument(t *testing.T) {
	store := openIntegrationStore(t)
	docID := uniqueDocumentID()
	if _, err := store.CreateDocument(context.Background(), docID, "legacy", "owner"); err != nil {
		t.Fatal(err)
	}
	activateAuthority(t, store, docID, storage.LocalAuthority)
	if _, err := store.DB().ExecContext(context.Background(), `DELETE FROM snapshots WHERE document_id = ?`, docID); err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.EnsureBaselineSnapshot(context.Background(), docID, storage.LocalAuthority)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Version != 0 || snapshot.Content != "legacy" {
		t.Fatalf("baseline snapshot = %#v", snapshot)
	}
}

func TestMySQLRecoveryRejectsVersionGapAndContentMismatch(t *testing.T) {
	t.Run("version gap", func(t *testing.T) {
		store := openIntegrationStore(t)
		docID := uniqueDocumentID()
		_, _ = store.CreateDocument(context.Background(), docID, "", "owner")
		activateAuthority(t, store, docID, storage.LocalAuthority)
		if err := store.CommitEdit(context.Background(), integrationCommit(docID, "gap-1", 0, "A")); err != nil {
			t.Fatal(err)
		}
		if err := store.CommitEdit(context.Background(), integrationCommit(docID, "gap-2", 1, "AB")); err != nil {
			t.Fatal(err)
		}
		if _, err := store.DB().ExecContext(context.Background(), `DELETE FROM operation_logs WHERE document_id = ? AND version = 1`, docID); err != nil {
			t.Fatal(err)
		}
		manager := room.NewManager(store)
		if _, err := manager.Room(docID); err == nil || !strings.Contains(err.Error(), "version gap") {
			t.Fatalf("gap recovery error = %v", err)
		}
	})
	t.Run("content mismatch", func(t *testing.T) {
		store := openIntegrationStore(t)
		docID := uniqueDocumentID()
		_, _ = store.CreateDocument(context.Background(), docID, "correct", "owner")
		if _, err := store.DB().ExecContext(context.Background(), `UPDATE snapshots SET content = 'wrong' WHERE document_id = ?`, docID); err != nil {
			t.Fatal(err)
		}
		manager := room.NewManager(store)
		if _, err := manager.Room(docID); err == nil || !strings.Contains(err.Error(), "content") {
			t.Fatalf("content recovery error = %v", err)
		}
	})
}

func TestMySQLCommitEditAndOperationLog(t *testing.T) {
	store := openIntegrationStore(t)
	docID := uniqueDocumentID()
	if _, err := store.CreateDocument(context.Background(), docID, "abc", "owner"); err != nil {
		t.Fatal(err)
	}
	activateAuthority(t, store, docID, storage.LocalAuthority)
	commit := integrationCommit(docID, "edit-1", 0, "aXbc")
	commit.Operation.Position = 1
	commit.Operation.Text = "X"
	if err := store.CommitEdit(context.Background(), commit); err != nil {
		t.Fatal(err)
	}
	document, err := store.GetDocument(context.Background(), docID)
	if err != nil {
		t.Fatal(err)
	}
	if document.Content != "aXbc" || document.Version != 1 {
		t.Fatalf("document = %#v", document)
	}
	operations, err := store.LoadOperations(context.Background(), docID)
	if err != nil {
		t.Fatal(err)
	}
	if len(operations) != 1 || operations[0].ID != "edit-1" || operations[0].Version != 1 || operations[0].Position != 1 {
		t.Fatalf("operations = %#v", operations)
	}
}

func TestMySQLEditFailureRollsBackDocumentUpdate(t *testing.T) {
	store := openIntegrationStore(t)
	docID := uniqueDocumentID()
	if _, err := store.CreateDocument(context.Background(), docID, "abc", "owner"); err != nil {
		t.Fatal(err)
	}
	activateAuthority(t, store, docID, storage.LocalAuthority)
	commit := integrationCommit(docID, strings.Repeat("r", 300), 0, "changed")
	if err := store.CommitEdit(context.Background(), commit); err == nil {
		t.Fatal("oversized operation unexpectedly committed")
	}
	document, err := store.GetDocument(context.Background(), docID)
	if err != nil {
		t.Fatal(err)
	}
	if document.Content != "abc" || document.Version != 0 {
		t.Fatalf("document was not rolled back: %#v", document)
	}
	operations, err := store.LoadOperations(context.Background(), docID)
	if err != nil {
		t.Fatal(err)
	}
	if len(operations) != 0 {
		t.Fatalf("operation log was not rolled back: %#v", operations)
	}
}

func TestMySQLDuplicateRequestDoesNotApplyTwice(t *testing.T) {
	store := openIntegrationStore(t)
	docID := uniqueDocumentID()
	if _, err := store.CreateDocument(context.Background(), docID, "", "owner"); err != nil {
		t.Fatal(err)
	}
	activateAuthority(t, store, docID, storage.LocalAuthority)
	first := integrationCommit(docID, "same-request", 0, "A")
	if err := store.CommitEdit(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	second := integrationCommit(docID, "same-request", 1, "AB")
	if err := store.CommitEdit(context.Background(), second); !errors.Is(err, storage.ErrDuplicateRequest) {
		t.Fatalf("duplicate error = %v", err)
	}
	document, err := store.GetDocument(context.Background(), docID)
	if err != nil {
		t.Fatal(err)
	}
	if document.Content != "A" || document.Version != 1 {
		t.Fatalf("document = %#v", document)
	}
}

func TestMySQLConcurrentVersionUpdateDoesNotOverwrite(t *testing.T) {
	store := openIntegrationStore(t)
	docID := uniqueDocumentID()
	if _, err := store.CreateDocument(context.Background(), docID, "", "owner"); err != nil {
		t.Fatal(err)
	}
	activateAuthority(t, store, docID, storage.LocalAuthority)
	commits := []storage.EditCommit{
		integrationCommit(docID, "concurrent-a", 0, "A"),
		integrationCommit(docID, "concurrent-b", 0, "B"),
	}
	results := make(chan error, 2)
	var wait sync.WaitGroup
	for _, commit := range commits {
		wait.Add(1)
		go func(commit storage.EditCommit) {
			defer wait.Done()
			results <- store.CommitEdit(context.Background(), commit)
		}(commit)
	}
	wait.Wait()
	close(results)
	var successes, conflicts int
	for err := range results {
		if err == nil {
			successes++
		} else if errors.Is(err, storage.ErrVersionConflict) {
			conflicts++
		} else {
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("successes=%d conflicts=%d", successes, conflicts)
	}
}

func TestMySQLPermissionTransaction(t *testing.T) {
	store := openIntegrationStore(t)
	docID := uniqueDocumentID()
	if _, err := store.CreateDocument(context.Background(), docID, "", "owner"); err != nil {
		t.Fatal(err)
	}
	activateAuthority(t, store, docID, storage.LocalAuthority)
	if err := store.SetPermission(context.Background(), permissionCommit(storage.LocalAuthority, docID, "writer", "reader", permission.RoleRead)); !errors.Is(err, storage.ErrPermissionDenied) {
		t.Fatalf("non-owner error = %v", err)
	}
	if err := store.SetPermission(context.Background(), permissionCommit(storage.LocalAuthority, docID, "owner", "writer", permission.RoleWrite)); err != nil {
		t.Fatal(err)
	}
	if err := store.SetPermission(context.Background(), permissionCommit(storage.LocalAuthority, docID, "owner", "writer", permission.RoleRead)); err != nil {
		t.Fatal(err)
	}
	role, err := store.GetPermission(context.Background(), docID, "writer")
	if err != nil {
		t.Fatal(err)
	}
	if role != permission.RoleRead {
		t.Fatalf("writer role = %q", role)
	}
}

func TestMySQLSaveSnapshot(t *testing.T) {
	store := openIntegrationStore(t)
	docID := uniqueDocumentID()
	if _, err := store.CreateDocument(context.Background(), docID, "snapshot", "owner"); err != nil {
		t.Fatal(err)
	}
	activateAuthority(t, store, docID, storage.LocalAuthority)
	snapshot := storage.Snapshot{DocumentID: docID, Version: 0, Content: "snapshot"}
	if err := store.SaveSnapshot(context.Background(), storage.SnapshotCommit{Authority: storage.LocalAuthority, Snapshot: snapshot}); !errors.Is(err, storage.ErrAlreadyExists) {
		t.Fatalf("duplicate snapshot error = %v", err)
	}
	loaded, err := store.GetLatestSnapshot(context.Background(), docID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded != snapshot {
		t.Fatalf("initial snapshot = %#v, want %#v", loaded, snapshot)
	}
}

func TestMySQLFencingTakeoverRejectsOldOwner(t *testing.T) {
	store := openIntegrationStore(t)
	docID := uniqueDocumentID()
	if _, err := store.CreateDocument(context.Background(), docID, "", "owner"); err != nil {
		t.Fatal(err)
	}
	nodeA := storage.WriteAuthority{NodeID: "node-a", FencingToken: 1}
	nodeB := storage.WriteAuthority{NodeID: "node-b", FencingToken: 2}
	activateAuthority(t, store, docID, nodeA)

	first := integrationCommitWithAuthority(nodeA, docID, "node-a-edit", 0, "A")
	if err := store.CommitEdit(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	activateAuthority(t, store, docID, nodeB)

	oldEdit := integrationCommitWithAuthority(nodeA, docID, "stale-edit", 1, "stale")
	if err := store.CommitEdit(context.Background(), oldEdit); !errors.Is(err, storage.ErrFenced) {
		t.Fatalf("old owner edit = %v", err)
	}
	oldPermission := permissionCommit(nodeA, docID, "owner", "writer", permission.RoleWrite)
	if err := store.SetPermission(context.Background(), oldPermission); !errors.Is(err, storage.ErrFenced) {
		t.Fatalf("old owner permission = %v", err)
	}
	oldSnapshot := storage.SnapshotCommit{Authority: nodeA, Snapshot: storage.Snapshot{DocumentID: docID, Version: 1, Content: "A"}}
	if err := store.SaveSnapshot(context.Background(), oldSnapshot); !errors.Is(err, storage.ErrFenced) {
		t.Fatalf("old owner snapshot = %v", err)
	}

	second := integrationCommitWithAuthority(nodeB, docID, "node-b-edit", 1, "AB")
	if err := store.CommitEdit(context.Background(), second); err != nil {
		t.Fatal(err)
	}
	if err := store.ActivateOwnership(context.Background(), docID, nodeA); !errors.Is(err, storage.ErrOwnershipConflict) {
		t.Fatalf("token rollback activation = %v", err)
	}
	document, err := store.GetDocument(context.Background(), docID)
	if err != nil {
		t.Fatal(err)
	}
	if document.Content != "AB" || document.Version != 2 {
		t.Fatalf("document after takeover = %#v", document)
	}
	var tokens []uint64
	rows, err := store.DB().QueryContext(context.Background(), `SELECT fencing_token FROM operation_logs WHERE document_id = ? ORDER BY version`, docID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var token uint64
		if err := rows.Scan(&token); err != nil {
			t.Fatal(err)
		}
		tokens = append(tokens, token)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(tokens) != 2 || tokens[0] != 1 || tokens[1] != 2 {
		t.Fatalf("operation fencing tokens = %v", tokens)
	}
}

func TestMySQLConcurrentSameTokenActivationHasOneWinner(t *testing.T) {
	store := openIntegrationStore(t)
	docID := uniqueDocumentID()
	if _, err := store.CreateDocument(context.Background(), docID, "", "owner"); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	var wait sync.WaitGroup
	for _, nodeID := range []string{"node-a", "node-b"} {
		wait.Add(1)
		go func(nodeID string) {
			defer wait.Done()
			<-start
			results <- store.ActivateOwnership(context.Background(), docID, storage.WriteAuthority{NodeID: nodeID, FencingToken: 10})
		}(nodeID)
	}
	close(start)
	wait.Wait()
	close(results)
	var successes, conflicts int
	for err := range results {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, storage.ErrOwnershipConflict):
			conflicts++
		default:
			t.Fatalf("activation error = %v", err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("successes=%d conflicts=%d", successes, conflicts)
	}
}

func TestMySQLFencingTokenPersistsAcrossStoreRestart(t *testing.T) {
	dsn := integrationDSN(t)
	store := openStore(t, dsn)
	docID := uniqueDocumentID()
	if _, err := store.CreateDocument(context.Background(), docID, "", "owner"); err != nil {
		t.Fatal(err)
	}
	authority := storage.WriteAuthority{NodeID: "node-persistent", FencingToken: 41}
	activateAuthority(t, store, docID, authority)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened := openStore(t, dsn)
	t.Cleanup(func() { _ = reopened.Close() })
	current, err := reopened.GetWriteAuthority(context.Background(), docID)
	if err != nil {
		t.Fatal(err)
	}
	if current != authority {
		t.Fatalf("authority after restart = %#v, want %#v", current, authority)
	}
	if err := reopened.CommitEdit(context.Background(), integrationCommitWithAuthority(authority, docID, "after-restart", 0, "A")); err != nil {
		t.Fatal(err)
	}
}

func TestMySQLConcurrentOldWriteAndTakeoverRemainOrdered(t *testing.T) {
	store := openIntegrationStore(t)
	docID := uniqueDocumentID()
	if _, err := store.CreateDocument(context.Background(), docID, "", "owner"); err != nil {
		t.Fatal(err)
	}
	nodeA := storage.WriteAuthority{NodeID: "node-a", FencingToken: 1}
	nodeB := storage.WriteAuthority{NodeID: "node-b", FencingToken: 2}
	activateAuthority(t, store, docID, nodeA)

	start := make(chan struct{})
	writeResult := make(chan error, 1)
	activateResult := make(chan error, 1)
	go func() {
		<-start
		writeResult <- store.CommitEdit(context.Background(), integrationCommitWithAuthority(nodeA, docID, "racing-old", 0, "A"))
	}()
	go func() {
		<-start
		activateResult <- store.ActivateOwnership(context.Background(), docID, nodeB)
	}()
	close(start)
	writeErr := <-writeResult
	activateErr := <-activateResult
	if activateErr != nil {
		t.Fatalf("takeover activation = %v", activateErr)
	}
	if writeErr != nil && !errors.Is(writeErr, storage.ErrFenced) {
		t.Fatalf("racing old write = %v", writeErr)
	}

	document, err := store.GetDocument(context.Background(), docID)
	if err != nil {
		t.Fatal(err)
	}
	afterTakeover := integrationCommitWithAuthority(nodeA, docID, "after-takeover", document.Version, document.Content+"X")
	if err := store.CommitEdit(context.Background(), afterTakeover); !errors.Is(err, storage.ErrFenced) {
		t.Fatalf("old write after successful takeover = %v", err)
	}
}

func TestMySQLPersistenceAcrossStoreRestart(t *testing.T) {
	dsn := integrationDSN(t)
	store := openStore(t, dsn)
	docID := uniqueDocumentID()
	if _, err := store.CreateDocument(context.Background(), docID, "persistent", "owner"); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened := openStore(t, dsn)
	t.Cleanup(func() { _ = reopened.Close() })
	document, err := reopened.GetDocument(context.Background(), docID)
	if err != nil {
		t.Fatal(err)
	}
	if document.Content != "persistent" {
		t.Fatalf("document after restart = %#v", document)
	}
}

func TestMySQLRoomRecreationLoadsHistoryForStaleOT(t *testing.T) {
	store := openIntegrationStore(t)
	docID := uniqueDocumentID()
	if _, err := store.CreateDocument(context.Background(), docID, "ab", "owner"); err != nil {
		t.Fatal(err)
	}
	firstManager := room.NewManager(store)
	first, err := firstManager.Room(docID)
	if err != nil {
		t.Fatal(err)
	}
	firstOwner := integrationClient(t, first, "owner")
	if err := first.SubmitEdit(firstOwner, "first", collab.Operation{Kind: collab.OperationInsert, Position: 1, Text: "X"}); err != nil {
		t.Fatal(err)
	}
	<-firstOwner.Send
	shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := firstManager.Shutdown(shutdownCtx); err != nil {
		t.Fatal(err)
	}

	secondManager := room.NewManager(store)
	t.Cleanup(func() { _ = secondManager.Shutdown(context.Background()) })
	second, err := secondManager.Room(docID)
	if err != nil {
		t.Fatal(err)
	}
	secondOwner := integrationClient(t, second, "owner")
	if err := second.SubmitEdit(secondOwner, "stale", collab.Operation{
		BaseVersion: 0, Kind: collab.OperationInsert, Position: 2, Text: "Y",
	}); err != nil {
		t.Fatal(err)
	}
	message := <-secondOwner.Send
	if message.Type != protocol.TypeEditApplied || message.Operation == nil || message.Operation.Position != 3 || message.Version != 2 {
		t.Fatalf("stale edit = %#v", message)
	}
	document, err := store.GetDocument(context.Background(), docID)
	if err != nil {
		t.Fatal(err)
	}
	if document.Content != "aXbY" || document.Version != 2 {
		t.Fatalf("document = %#v", document)
	}
}

func TestMySQLRoomGeneratesSnapshotAndRecoversAfterRestart(t *testing.T) {
	store := openIntegrationStore(t)
	docID := uniqueDocumentID()
	if _, err := store.CreateDocument(context.Background(), docID, "", "owner"); err != nil {
		t.Fatal(err)
	}
	firstManager := room.NewManager(store, room.WithSnapshotPolicy(2, 10, time.Second))
	first, err := firstManager.Room(docID)
	if err != nil {
		t.Fatal(err)
	}
	owner := integrationClient(t, first, "owner")
	for version, text := range []string{"A", "B"} {
		if err := first.SubmitEdit(owner, fmt.Sprintf("snapshot-%d", version), collab.Operation{
			BaseVersion: uint64(version), Kind: collab.OperationInsert, Position: version, Text: text,
		}); err != nil {
			t.Fatal(err)
		}
		<-owner.Send
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		snapshot, err := store.GetLatestSnapshot(context.Background(), docID)
		if err == nil && snapshot.Version == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("version 2 snapshot not generated: %#v %v", snapshot, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := firstManager.Shutdown(shutdownCtx); err != nil {
		t.Fatal(err)
	}

	secondManager := room.NewManager(store, room.WithSnapshotPolicy(2, 10, time.Second))
	t.Cleanup(func() { _ = secondManager.Shutdown(context.Background()) })
	second, err := secondManager.Room(docID)
	if err != nil {
		t.Fatal(err)
	}
	if document := second.Snapshot(); document.Content != "AB" || document.Version != 2 {
		t.Fatalf("recovered document = %#v", document)
	}
	resumed := &room.Client{UserID: "owner", Send: make(chan protocol.Message, 2)}
	if err := second.ResumeContext(context.Background(), resumed, 0); err != nil {
		t.Fatal(err)
	}
	if message := <-resumed.Send; message.Type != protocol.TypeResyncRequired || message.Snapshot == nil || message.Snapshot.Version != 2 {
		t.Fatalf("restart resume = %#v", message)
	}
}

func openIntegrationStore(t *testing.T) *mysqlstorage.Store {
	t.Helper()
	store := openStore(t, integrationDSN(t))
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func openStore(t *testing.T, dsn string) *mysqlstorage.Store {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	store, err := mysqlstorage.Open(ctx, mysqlstorage.Config{
		DSN: dsn, MaxOpenConns: 10, MaxIdleConns: 5,
		ConnMaxLifetime: time.Minute, PingTimeout: 5 * time.Second, QueryTimeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := migration.Run(ctx, store.DB(), "../../../migrations"); err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	return store
}

func integrationDSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("MYSQL_TEST_DSN")
	if dsn == "" {
		t.Skip("MYSQL_TEST_DSN is not configured; skipping real MySQL integration test")
	}
	return dsn
}

func uniqueDocumentID() string {
	return fmt.Sprintf("integration-%d-%d", time.Now().UnixNano(), documentSequence.Add(1))
}

func integrationCommit(docID, requestID string, expectedVersion uint64, content string) storage.EditCommit {
	return integrationCommitWithAuthority(storage.LocalAuthority, docID, requestID, expectedVersion, content)
}

func integrationCommitWithAuthority(authority storage.WriteAuthority, docID, requestID string, expectedVersion uint64, content string) storage.EditCommit {
	nextVersion := expectedVersion + 1
	operation := collab.Operation{
		ID: requestID, UserID: "owner", BaseVersion: expectedVersion,
		Kind: collab.OperationInsert, Position: 0, Text: content,
	}
	return storage.EditCommit{
		Authority:       authority,
		ExpectedVersion: expectedVersion,
		Document:        collab.Document{ID: docID, Content: content, Version: nextVersion},
		Operation:       collab.AppliedOperation{Operation: operation, Version: nextVersion},
	}
}

func activateAuthority(t *testing.T, store *mysqlstorage.Store, docID string, authority storage.WriteAuthority) {
	t.Helper()
	if err := store.ActivateOwnership(context.Background(), docID, authority); err != nil {
		t.Fatal(err)
	}
}

func permissionCommit(authority storage.WriteAuthority, docID, actorID, userID string, role permission.Role) storage.PermissionCommit {
	return storage.PermissionCommit{
		Authority:  authority,
		DocumentID: docID,
		ActorID:    actorID,
		UserID:     userID,
		Role:       role,
	}
}

func integrationClient(t *testing.T, target *room.Room, userID string) *room.Client {
	t.Helper()
	client := &room.Client{UserID: userID, Send: make(chan protocol.Message, 16)}
	if err := target.Join(client); err != nil {
		t.Fatal(err)
	}
	if joined := <-client.Send; joined.Type != protocol.TypeJoined {
		t.Fatalf("join message = %#v", joined)
	}
	return client
}
