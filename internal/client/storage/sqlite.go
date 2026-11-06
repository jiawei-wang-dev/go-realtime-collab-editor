package clientstorage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"github.com/gofrs/flock"
	_ "modernc.org/sqlite"
)

const timestampFormat = time.RFC3339Nano

var validProfile = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

type SQLite struct {
	db   *sql.DB
	lock *flock.Flock
	path string
}

func StatePath(stateDir, profile string) (string, error) {
	if !validProfile.MatchString(profile) {
		return "", errors.New("profile must contain only letters, digits, dot, underscore, or hyphen")
	}
	if stateDir == "" {
		root, err := os.UserConfigDir()
		if err != nil {
			return "", fmt.Errorf("locate user config directory: %w", err)
		}
		stateDir = filepath.Join(root, "collab-editor")
	}
	absolute, err := filepath.Abs(filepath.Join(stateDir, "profiles", profile))
	if err != nil {
		return "", err
	}
	return filepath.Join(absolute, "state.db"), nil
}

func Open(ctx context.Context, stateDir, profile string) (*SQLite, error) {
	path, err := StatePath(stateDir, profile)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create client state directory: %w", err)
	}
	profileLock := flock.New(path + ".lock")
	locked, err := profileLock.TryLock()
	if err != nil {
		return nil, fmt.Errorf("lock client profile: %w", err)
	}
	if !locked {
		return nil, ErrProfileInUse
	}
	release := func() { _ = profileLock.Unlock() }
	db, err := sql.Open("sqlite", path)
	if err != nil {
		release()
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	clientStore := &SQLite{db: db, lock: profileLock, path: path}
	if err := clientStore.initialize(ctx); err != nil {
		_ = db.Close()
		release()
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = clientStore.Close()
		return nil, fmt.Errorf("secure client database permissions: %w", err)
	}
	return clientStore, nil
}

func (s *SQLite) Path() string { return s.path }

func (s *SQLite) initialize(ctx context.Context) error {
	for _, pragma := range []string{
		"PRAGMA journal_mode = WAL",
		"PRAGMA foreign_keys = ON",
		"PRAGMA busy_timeout = 5000",
		"PRAGMA synchronous = FULL",
	} {
		if _, err := s.db.ExecContext(ctx, pragma); err != nil {
			return fmt.Errorf("configure SQLite: %w", err)
		}
	}
	var integrity string
	if err := s.db.QueryRowContext(ctx, "PRAGMA quick_check").Scan(&integrity); err != nil {
		return fmt.Errorf("%w: %v", ErrCorrupt, err)
	}
	if integrity != "ok" {
		return fmt.Errorf("%w: %s", ErrCorrupt, integrity)
	}
	if _, err := s.db.ExecContext(ctx, schema); err != nil {
		return fmt.Errorf("migrate client database: %w", err)
	}
	if err := s.ensureRefreshCredentialColumn(ctx); err != nil {
		return fmt.Errorf("migrate refresh attempts: %w", err)
	}
	return nil
}

func (s *SQLite) ensureRefreshCredentialColumn(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, `PRAGMA table_info(refresh_attempts)`)
	if err != nil {
		return err
	}
	found := false
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, dataType string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &dataType, &notNull, &defaultValue, &primaryKey); err != nil {
			rows.Close()
			return err
		}
		if name == "credential_request_id" {
			found = true
		}
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if found {
		return nil
	}
	_, err = s.db.ExecContext(ctx, `ALTER TABLE refresh_attempts ADD COLUMN credential_request_id TEXT NOT NULL DEFAULT ''`)
	return err
}

func (s *SQLite) Recovery(ctx context.Context) (Recovery, error) {
	var recovery Recovery
	err := s.db.QueryRowContext(ctx, "SELECT value FROM client_meta WHERE key = 'active_document'").Scan(&recovery.ActiveDocument)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return Recovery{}, err
	}
	if recovery.ActiveDocument != "" {
		document, loadErr := s.LoadDocument(ctx, recovery.ActiveDocument)
		if loadErr == nil {
			recovery.Document = &document
		} else if !errors.Is(loadErr, ErrNotFound) {
			return Recovery{}, loadErr
		}
		pending, loadErr := s.ListPending(ctx, recovery.ActiveDocument)
		if loadErr != nil {
			return Recovery{}, loadErr
		}
		recovery.Pending = pending
	}
	var attempt RefreshAttempt
	var created, updated string
	err = s.db.QueryRowContext(ctx, `SELECT request_id,credential_request_id,status,created_at,updated_at FROM refresh_attempts ORDER BY created_at LIMIT 1`).Scan(&attempt.RequestID, &attempt.CredentialRequestID, &attempt.Status, &created, &updated)
	if err == nil {
		attempt.CreatedAt, _ = time.Parse(timestampFormat, created)
		attempt.UpdatedAt, _ = time.Parse(timestampFormat, updated)
		recovery.RefreshAttempt = &attempt
	} else if !errors.Is(err, sql.ErrNoRows) {
		return Recovery{}, err
	}
	return recovery, nil
}

func (s *SQLite) LoadDocument(ctx context.Context, docID string) (Document, error) {
	var document Document
	var updated string
	var version int64
	err := s.db.QueryRowContext(ctx, `SELECT doc_id,server_version,content,updated_at FROM client_documents WHERE doc_id=?`, docID).Scan(&document.DocID, &version, &document.Content, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return Document{}, ErrNotFound
	}
	if err != nil {
		return Document{}, err
	}
	document.ServerVersion = uint64(version)
	document.UpdatedAt, _ = time.Parse(timestampFormat, updated)
	return document, nil
}

func (s *SQLite) SetActiveDocument(ctx context.Context, docID string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO client_meta(key,value) VALUES('active_document',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, docID)
	return err
}

func (s *SQLite) SaveDocument(ctx context.Context, document Document) error {
	if document.UpdatedAt.IsZero() {
		document.UpdatedAt = time.Now().UTC()
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO client_documents(doc_id,server_version,content,updated_at) VALUES(?,?,?,?) ON CONFLICT(doc_id) DO UPDATE SET server_version=excluded.server_version,content=excluded.content,updated_at=excluded.updated_at`, document.DocID, document.ServerVersion, document.Content, document.UpdatedAt.Format(timestampFormat))
	return err
}

func (s *SQLite) AddPending(ctx context.Context, pending PendingEdit) error {
	payload, err := json.Marshal(pending.Operation)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	if pending.CreatedAt.IsZero() {
		pending.CreatedAt = now
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO pending_edits(doc_id,request_id,base_version,operation_json,status,retry_count,error,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?)`, pending.DocID, pending.RequestID, pending.BaseVersion, string(payload), pending.Status, pending.RetryCount, pending.Error, pending.CreatedAt.Format(timestampFormat), now.Format(timestampFormat))
	return err
}

func (s *SQLite) ListPending(ctx context.Context, docID string) ([]PendingEdit, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,doc_id,request_id,base_version,operation_json,status,retry_count,error,created_at,updated_at FROM pending_edits WHERE doc_id=? ORDER BY id`, docID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []PendingEdit
	for rows.Next() {
		var pending PendingEdit
		var operation, created, updated string
		var baseVersion int64
		if err := rows.Scan(&pending.ID, &pending.DocID, &pending.RequestID, &baseVersion, &operation, &pending.Status, &pending.RetryCount, &pending.Error, &created, &updated); err != nil {
			return nil, err
		}
		pending.BaseVersion = uint64(baseVersion)
		if err := json.Unmarshal([]byte(operation), &pending.Operation); err != nil {
			return nil, fmt.Errorf("decode pending %s: %w", pending.RequestID, err)
		}
		pending.CreatedAt, _ = time.Parse(timestampFormat, created)
		pending.UpdatedAt, _ = time.Parse(timestampFormat, updated)
		result = append(result, pending)
	}
	return result, rows.Err()
}

func (s *SQLite) UpdatePending(ctx context.Context, requestID, status string, retryCount int, message string) error {
	result, err := s.db.ExecContext(ctx, `UPDATE pending_edits SET status=?,retry_count=?,error=?,updated_at=? WHERE request_id=?`, status, retryCount, message, time.Now().UTC().Format(timestampFormat), requestID)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err == nil && rows == 0 {
		return ErrNotFound
	}
	return err
}

func (s *SQLite) CommitApplied(ctx context.Context, document Document, requestID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now().UTC().Format(timestampFormat)
	if _, err := tx.ExecContext(ctx, `INSERT INTO client_documents(doc_id,server_version,content,updated_at) VALUES(?,?,?,?) ON CONFLICT(doc_id) DO UPDATE SET server_version=excluded.server_version,content=excluded.content,updated_at=excluded.updated_at`, document.DocID, document.ServerVersion, document.Content, now); err != nil {
		return err
	}
	if requestID != "" {
		if _, err := tx.ExecContext(ctx, `DELETE FROM pending_edits WHERE doc_id=? AND request_id=?`, document.DocID, requestID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *SQLite) SaveRefreshAttempt(ctx context.Context, attempt RefreshAttempt) error {
	now := time.Now().UTC()
	if attempt.CreatedAt.IsZero() {
		attempt.CreatedAt = now
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO refresh_attempts(request_id,credential_request_id,status,created_at,updated_at) VALUES(?,?,?,?,?) ON CONFLICT(request_id) DO UPDATE SET credential_request_id=excluded.credential_request_id,status=excluded.status,updated_at=excluded.updated_at`, attempt.RequestID, attempt.CredentialRequestID, attempt.Status, attempt.CreatedAt.Format(timestampFormat), now.Format(timestampFormat))
	return err
}

func (s *SQLite) ClearRefreshAttempt(ctx context.Context, requestID string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM refresh_attempts WHERE request_id=?`, requestID)
	return err
}

func (s *SQLite) Close() error {
	err := s.db.Close()
	unlockErr := s.lock.Unlock()
	return errors.Join(err, unlockErr)
}

var _ Store = (*SQLite)(nil)
