package clientstorage

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/collab"
)

var tokenColumnPattern = regexp.MustCompile(`(?i)(access|refresh)[_-]?token`)

func TestSQLiteDocumentPendingAndAtomicCommit(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, t.TempDir(), "user-a")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.SetActiveDocument(ctx, "main.go"); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveDocument(ctx, Document{DocID: "main.go", Content: "A", ServerVersion: 1}); err != nil {
		t.Fatal(err)
	}
	operation := collab.Operation{Kind: collab.OperationInsert, Position: 1, Text: "B", BaseVersion: 1}
	if err := store.AddPending(ctx, PendingEdit{DocID: "main.go", RequestID: "request-1", BaseVersion: 1, Operation: operation, Status: "QUEUED"}); err != nil {
		t.Fatal(err)
	}
	if err := store.AddPending(ctx, PendingEdit{DocID: "main.go", RequestID: "request-1", BaseVersion: 1, Operation: operation, Status: "QUEUED"}); err == nil {
		t.Fatal("duplicate request_id was accepted")
	}
	pending, err := store.ListPending(ctx, "main.go")
	if err != nil || len(pending) != 1 || pending[0].RequestID != "request-1" || pending[0].Operation.Text != "B" {
		t.Fatalf("pending = %#v, %v", pending, err)
	}
	if err := store.UpdatePending(ctx, "request-1", "SENT", 1, ""); err != nil {
		t.Fatal(err)
	}
	if err := store.CommitApplied(ctx, Document{DocID: "main.go", Content: "AB", ServerVersion: 2}, "request-1"); err != nil {
		t.Fatal(err)
	}
	document, err := store.LoadDocument(ctx, "main.go")
	if err != nil || document.Content != "AB" || document.ServerVersion != 2 {
		t.Fatalf("document = %#v, %v", document, err)
	}
	pending, _ = store.ListPending(ctx, "main.go")
	if len(pending) != 0 {
		t.Fatalf("pending after commit = %#v", pending)
	}
}

func TestSQLiteRestartMigrationProfileIsolationAndLock(t *testing.T) {
	ctx := context.Background()
	directory := t.TempDir()
	first, err := Open(ctx, directory, "user-a")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Open(ctx, directory, "user-a"); !errors.Is(err, ErrProfileInUse) {
		t.Fatalf("second profile writer error = %v", err)
	}
	_ = first.SetActiveDocument(ctx, "doc")
	_ = first.SaveDocument(ctx, Document{DocID: "doc", Content: "persisted", ServerVersion: 7})
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(ctx, directory, "user-a")
	if err != nil {
		t.Fatal(err)
	}
	recovery, err := reopened.Recovery(ctx)
	if err != nil || recovery.Document == nil || recovery.Document.Content != "persisted" || recovery.Document.ServerVersion != 7 {
		t.Fatalf("recovery = %#v, %v", recovery, err)
	}
	defer reopened.Close()
	other, err := Open(ctx, directory, "user-b")
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	otherRecovery, err := other.Recovery(ctx)
	if err != nil || otherRecovery.Document != nil {
		t.Fatalf("profile isolation failed: %#v, %v", otherRecovery, err)
	}
}

func TestSQLitePathAndIntegrityFailure(t *testing.T) {
	if _, err := StatePath(t.TempDir(), "../escape"); err == nil {
		t.Fatal("unsafe profile accepted")
	}
	directory := t.TempDir()
	path, err := StatePath(directory, "corrupt")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("not sqlite"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(context.Background(), directory, "corrupt"); err == nil {
		t.Fatal("corrupt database was silently replaced")
	}
	content, err := os.ReadFile(path)
	if err != nil || string(content) != "not sqlite" {
		t.Fatal("corrupt database was not preserved")
	}
}

func TestSQLiteDoesNotContainTokenColumns(t *testing.T) {
	store, err := Open(context.Background(), t.TempDir(), "secure")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	rows, err := store.db.Query(`SELECT name,sql FROM sqlite_master WHERE type='table'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var name, definition string
		if err := rows.Scan(&name, &definition); err != nil {
			t.Fatal(err)
		}
		if name == "client_documents" || name == "pending_edits" || name == "refresh_attempts" {
			if containsTokenColumn(definition) {
				t.Fatalf("table %s contains token column: %s", name, definition)
			}
		}
	}
}

func TestSQLiteMigratesEarlierRefreshAttemptSchema(t *testing.T) {
	directory := t.TempDir()
	path, err := StatePath(directory, "legacy")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE refresh_attempts(request_id TEXT PRIMARY KEY,status TEXT NOT NULL,created_at TEXT NOT NULL,updated_at TEXT NOT NULL)`)
	_ = db.Close()
	if err != nil {
		t.Fatal(err)
	}
	store, err := Open(context.Background(), directory, "legacy")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.SaveRefreshAttempt(context.Background(), RefreshAttempt{RequestID: "migrated", CredentialRequestID: "prior", Status: "pending"}); err != nil {
		t.Fatal(err)
	}
	recovery, err := store.Recovery(context.Background())
	if err != nil || recovery.RefreshAttempt == nil || recovery.RefreshAttempt.CredentialRequestID != "prior" {
		t.Fatalf("migration recovery = %#v, %v", recovery, err)
	}
}

func containsTokenColumn(definition string) bool {
	return tokenColumnPattern.MatchString(definition)
}
