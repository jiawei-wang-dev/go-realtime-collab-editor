package mysql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/collab"
	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/permission"
	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/storage"
	"go.opentelemetry.io/otel"

	driver "github.com/go-sql-driver/mysql"
)

const duplicateEntry = 1062

type Config struct {
	DSN             string
	MaxOpenConns    int
	MaxIdleConns    int
	ConnMaxLifetime time.Duration
	PingTimeout     time.Duration
	QueryTimeout    time.Duration
	Observer        Observer
}

type Observer interface {
	MySQLOperation(operation string, duration time.Duration, err error)
}

type Store struct {
	db           *sql.DB
	queryTimeout time.Duration
	observer     Observer
}

var _ storage.Store = (*Store)(nil)

func Open(ctx context.Context, config Config) (*Store, error) {
	if config.DSN == "" {
		return nil, fmt.Errorf("mysql DSN is required")
	}
	if config.PingTimeout <= 0 {
		config.PingTimeout = 5 * time.Second
	}
	if config.QueryTimeout <= 0 {
		config.QueryTimeout = 5 * time.Second
	}
	db, err := sql.Open("mysql", config.DSN)
	if err != nil {
		return nil, fmt.Errorf("open mysql: %w", err)
	}
	db.SetMaxOpenConns(config.MaxOpenConns)
	db.SetMaxIdleConns(config.MaxIdleConns)
	db.SetConnMaxLifetime(config.ConnMaxLifetime)

	pingCtx, cancel := context.WithTimeout(ctx, config.PingTimeout)
	defer cancel()
	if err := db.PingContext(pingCtx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("connect mysql: %w", err)
	}
	return &Store{db: db, queryTimeout: config.QueryTimeout, observer: config.Observer}, nil
}

func (s *Store) Close() error { return s.db.Close() }
func (s *Store) DB() *sql.DB  { return s.db }

func (s *Store) CreateDocument(ctx context.Context, id, content, ownerID string) (_ collab.Document, resultErr error) {
	return s.createDocument(ctx, id, content, ownerID, "", "")
}

func (s *Store) CreateFolderDocument(ctx context.Context, id, content, ownerID, folderID, name string) (collab.Document, error) {
	return s.createDocument(ctx, id, content, ownerID, folderID, name)
}

func (s *Store) createDocument(ctx context.Context, id, content, ownerID, folderID, name string) (_ collab.Document, resultErr error) {
	ctx, span := otel.Tracer("collab-editor/mysql").Start(ctx, "mysql.create_document")
	defer span.End()
	started := time.Now()
	defer func() { s.observe("create_document", started, resultErr) }()
	if id == "" || ownerID == "" {
		return collab.Document{}, fmt.Errorf("document id and owner id are required")
	}
	ctx, cancel := s.withTimeout(ctx)
	defer cancel()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return collab.Document{}, fmt.Errorf("begin create document: %w", err)
	}
	defer tx.Rollback()

	if folderID != "" {
		if err := lockOwnedFolder(ctx, tx, folderID, ownerID); err != nil {
			return collab.Document{}, err
		}
	}
	if _, err = tx.ExecContext(ctx,
		`INSERT INTO documents (id, owner_id, content, version, folder_id, display_name)
		 VALUES (?, ?, ?, 0, NULLIF(?, ''), NULLIF(?, ''))`,
		id, ownerID, content, folderID, name,
	); err != nil {
		if isDuplicate(err) {
			return collab.Document{}, storage.ErrAlreadyExists
		}
		return collab.Document{}, fmt.Errorf("insert document: %w", err)
	}
	if _, err = tx.ExecContext(ctx,
		`INSERT INTO document_permissions (document_id, user_id, role) VALUES (?, ?, ?)`,
		id, ownerID, permission.RoleOwner,
	); err != nil {
		return collab.Document{}, fmt.Errorf("insert owner permission: %w", err)
	}
	if _, err = tx.ExecContext(ctx,
		`INSERT INTO snapshots (document_id, version, content) VALUES (?, 0, ?)`,
		id, content,
	); err != nil {
		return collab.Document{}, fmt.Errorf("insert initial snapshot: %w", err)
	}
	if err = tx.Commit(); err != nil {
		return collab.Document{}, fmt.Errorf("commit create document: %w", err)
	}
	return collab.Document{ID: id, Content: content}, nil
}

func (s *Store) ListDocuments(ctx context.Context, userID string) ([]storage.DocumentSummary, error) {
	ctx, cancel := s.withTimeout(ctx)
	defer cancel()
	rows, err := s.db.QueryContext(ctx, `
		WITH RECURSIVE visible_folders AS (
			SELECT folder_id, IF(role = 'write', 2, 1) AS strength
			FROM folder_permissions WHERE user_id = ?
			UNION ALL
			SELECT f.id, v.strength FROM folders f JOIN visible_folders v ON f.parent_id = v.folder_id
		), grants AS (
			SELECT document_id, CASE role WHEN 'owner' THEN 3 WHEN 'write' THEN 2 ELSE 1 END AS strength
			FROM document_permissions WHERE user_id = ?
			UNION ALL
			SELECT d.id, v.strength FROM documents d JOIN visible_folders v ON v.folder_id = d.folder_id
		), effective AS (
			SELECT document_id, MAX(strength) AS strength FROM grants GROUP BY document_id
		)
		SELECT d.id, d.version, ELT(e.strength, 'read', 'write', 'owner'), d.updated_at,
			COALESCE(d.folder_id, ''), COALESCE(d.display_name, d.id)
		FROM documents d JOIN effective e ON e.document_id = d.id
		ORDER BY d.updated_at DESC, d.id ASC`, userID, userID)
	if err != nil {
		return nil, fmt.Errorf("list documents: %w", err)
	}
	defer rows.Close()
	result := make([]storage.DocumentSummary, 0)
	for rows.Next() {
		var summary storage.DocumentSummary
		if err := rows.Scan(&summary.ID, &summary.Version, &summary.Role, &summary.UpdatedAt, &summary.FolderID, &summary.Name); err != nil {
			return nil, fmt.Errorf("scan document summary: %w", err)
		}
		result = append(result, summary)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate documents: %w", err)
	}
	return result, nil
}

func (s *Store) DeleteDocument(ctx context.Context, commit storage.DeleteDocumentCommit) error {
	if err := storage.ValidateWriteAuthority(commit.Authority); err != nil {
		return err
	}
	ctx, cancel := s.withTimeout(ctx)
	defer cancel()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin delete document: %w", err)
	}
	defer tx.Rollback()
	current, err := getWriteAuthorityForUpdate(ctx, tx, commit.DocumentID)
	if err != nil {
		return err
	}
	if current != commit.Authority {
		return storage.ErrFenced
	}
	var actorRole permission.Role
	if err := tx.QueryRowContext(ctx, `SELECT role FROM document_permissions WHERE document_id = ? AND user_id = ? FOR UPDATE`, commit.DocumentID, commit.ActorID).Scan(&actorRole); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return storage.ErrPermissionDenied
		}
		return fmt.Errorf("load delete permission: %w", err)
	}
	if !actorRole.CanChangePermissions() {
		return storage.ErrPermissionDenied
	}
	result, err := tx.ExecContext(ctx, `DELETE FROM documents WHERE id = ? AND owner_node_id = ? AND fencing_token = ?`, commit.DocumentID, commit.Authority.NodeID, commit.Authority.FencingToken)
	if err != nil {
		return fmt.Errorf("delete document: %w", err)
	}
	if rows, err := result.RowsAffected(); err != nil || rows != 1 {
		return storage.ErrFenced
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit delete document: %w", err)
	}
	return nil
}

func (s *Store) GetWriteAuthority(ctx context.Context, docID string) (storage.WriteAuthority, error) {
	ctx, cancel := s.withTimeout(ctx)
	defer cancel()
	return getWriteAuthority(ctx, s.db, docID)
}

func (s *Store) ActivateOwnership(ctx context.Context, docID string, authority storage.WriteAuthority) error {
	if err := storage.ValidateWriteAuthority(authority); err != nil {
		return err
	}
	ctx, cancel := s.withTimeout(ctx)
	defer cancel()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin ownership activation: %w", err)
	}
	defer tx.Rollback()

	current, err := getWriteAuthorityForUpdate(ctx, tx, docID)
	if err != nil {
		return err
	}
	if authority.FencingToken <= current.FencingToken {
		return storage.ErrOwnershipConflict
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE documents
		SET owner_node_id = ?, fencing_token = ?, updated_at = CURRENT_TIMESTAMP
		WHERE id = ? AND fencing_token = ?`,
		authority.NodeID, authority.FencingToken, docID, current.FencingToken,
	)
	if err != nil {
		return fmt.Errorf("activate ownership: %w", err)
	}
	if rows, err := result.RowsAffected(); err != nil {
		return fmt.Errorf("check ownership activation: %w", err)
	} else if rows != 1 {
		return storage.ErrOwnershipConflict
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit ownership activation: %w", err)
	}
	return nil
}

func (s *Store) LoadRecoveryState(ctx context.Context, id string, authority storage.WriteAuthority) (_ storage.RecoveryState, resultErr error) {
	ctx, span := otel.Tracer("collab-editor/mysql").Start(ctx, "mysql.load_recovery")
	defer span.End()
	started := time.Now()
	defer func() { s.observe("load_recovery", started, resultErr) }()
	if _, err := s.EnsureBaselineSnapshot(ctx, id, authority); err != nil {
		return storage.RecoveryState{}, err
	}
	ctx, cancel := s.withTimeout(ctx)
	defer cancel()
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return storage.RecoveryState{}, fmt.Errorf("begin load recovery state: %w", err)
	}
	defer tx.Rollback()
	currentAuthority, err := getWriteAuthority(ctx, tx, id)
	if err != nil {
		return storage.RecoveryState{}, err
	}
	if currentAuthority != authority || !authority.Valid() {
		return storage.RecoveryState{}, storage.ErrFenced
	}
	document, err := getDocument(ctx, tx, id)
	if err != nil {
		return storage.RecoveryState{}, err
	}
	permissions, err := getPermissions(ctx, tx, id)
	if err != nil {
		return storage.RecoveryState{}, err
	}
	snapshot, err := getLatestSnapshot(ctx, tx, id)
	if err != nil {
		return storage.RecoveryState{}, err
	}
	operations, err := loadOperationsAfter(ctx, tx, id, snapshot.Version)
	if err != nil {
		return storage.RecoveryState{}, err
	}
	if err := tx.Commit(); err != nil {
		return storage.RecoveryState{}, fmt.Errorf("commit load recovery state: %w", err)
	}
	return storage.RecoveryState{Document: document, Snapshot: snapshot, Operations: operations, Permissions: permissions}, nil
}

func (s *Store) LoadDocument(ctx context.Context, id string) (storage.DocumentState, error) {
	ctx, cancel := s.withTimeout(ctx)
	defer cancel()
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return storage.DocumentState{}, fmt.Errorf("begin load document: %w", err)
	}
	defer tx.Rollback()

	document, err := getDocument(ctx, tx, id)
	if err != nil {
		return storage.DocumentState{}, err
	}
	permissions, err := getPermissions(ctx, tx, id)
	if err != nil {
		return storage.DocumentState{}, err
	}
	operations, err := loadOperations(ctx, tx, id)
	if err != nil {
		return storage.DocumentState{}, err
	}
	if err := tx.Commit(); err != nil {
		return storage.DocumentState{}, fmt.Errorf("commit load document: %w", err)
	}
	return storage.DocumentState{Document: document, Permissions: permissions, Operations: operations}, nil
}

func (s *Store) GetDocument(ctx context.Context, id string) (collab.Document, error) {
	ctx, cancel := s.withTimeout(ctx)
	defer cancel()
	return getDocument(ctx, s.db, id)
}

func (s *Store) GetPermission(ctx context.Context, docID, userID string) (permission.Role, error) {
	ctx, cancel := s.withTimeout(ctx)
	defer cancel()
	permissions, err := getPermissions(ctx, s.db, docID)
	if err != nil {
		return "", err
	}
	role, ok := permissions[userID]
	if !ok {
		return "", storage.ErrNotFound
	}
	return role, nil
}

func (s *Store) GetPermissions(ctx context.Context, docID string) (map[string]permission.Role, error) {
	ctx, cancel := s.withTimeout(ctx)
	defer cancel()
	var exists int
	if err := s.db.QueryRowContext(ctx, "SELECT 1 FROM documents WHERE id = ?", docID).Scan(&exists); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, storage.ErrNotFound
		}
		return nil, err
	}
	return getPermissions(ctx, s.db, docID)
}

func (s *Store) SetPermission(ctx context.Context, commit storage.PermissionCommit) (resultErr error) {
	ctx, span := otel.Tracer("collab-editor/mysql").Start(ctx, "mysql.set_permission")
	defer span.End()
	started := time.Now()
	defer func() { s.observe("set_permission", started, resultErr) }()
	if err := storage.ValidateWriteAuthority(commit.Authority); err != nil {
		return err
	}
	if commit.UserID == "" || (commit.Role != "" && !commit.Role.Valid()) {
		return fmt.Errorf("valid target user and role are required")
	}
	ctx, cancel := s.withTimeout(ctx)
	defer cancel()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin permission transaction: %w", err)
	}
	defer tx.Rollback()

	currentAuthority, err := getWriteAuthorityForUpdate(ctx, tx, commit.DocumentID)
	if err != nil {
		return err
	}
	if currentAuthority != commit.Authority {
		return storage.ErrFenced
	}
	var actorRole permission.Role
	if err := tx.QueryRowContext(ctx,
		`SELECT role FROM document_permissions WHERE document_id = ? AND user_id = ? FOR UPDATE`,
		commit.DocumentID, commit.ActorID,
	).Scan(&actorRole); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return storage.ErrPermissionDenied
		}
		return fmt.Errorf("load actor permission: %w", err)
	}
	if !actorRole.CanChangePermissions() {
		return storage.ErrPermissionDenied
	}
	if commit.Role == "" {
		var role permission.Role
		err := tx.QueryRowContext(ctx, "SELECT role FROM document_permissions WHERE document_id = ? AND user_id = ?",
			commit.DocumentID, commit.UserID).Scan(&role)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if role == permission.RoleOwner {
			return storage.ErrPermissionDenied
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM document_permissions WHERE document_id = ? AND user_id = ?", commit.DocumentID, commit.UserID); err != nil {
			return err
		}
		return tx.Commit()
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO document_permissions (document_id, user_id, role)
		VALUES (?, ?, ?)
		ON DUPLICATE KEY UPDATE role = VALUES(role), updated_at = CURRENT_TIMESTAMP`,
		commit.DocumentID, commit.UserID, commit.Role,
	); err != nil {
		return fmt.Errorf("set permission: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit permission transaction: %w", err)
	}
	return nil
}

func (s *Store) CommitEdit(ctx context.Context, commit storage.EditCommit) (resultErr error) {
	ctx, span := otel.Tracer("collab-editor/mysql").Start(ctx, "mysql.commit_edit")
	defer span.End()
	started := time.Now()
	defer func() { s.observe("commit_edit", started, resultErr) }()
	if err := storage.ValidateWriteAuthority(commit.Authority); err != nil {
		return err
	}
	if commit.Operation.ID == "" {
		return fmt.Errorf("request id is required")
	}
	ctx, cancel := s.withTimeout(ctx)
	defer cancel()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin edit transaction: %w", err)
	}
	defer tx.Rollback()

	var currentVersion, currentToken uint64
	var currentNode sql.NullString
	if err := tx.QueryRowContext(ctx,
		`SELECT version, owner_node_id, fencing_token FROM documents WHERE id = ? FOR UPDATE`, commit.Document.ID,
	).Scan(&currentVersion, &currentNode, &currentToken); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return storage.ErrNotFound
		}
		return fmt.Errorf("lock edit document: %w", err)
	}
	if !currentNode.Valid || currentNode.String != commit.Authority.NodeID || currentToken != commit.Authority.FencingToken {
		return storage.ErrFenced
	}
	roles, err := getPermissions(ctx, tx, commit.Document.ID)
	if err != nil {
		return err
	}
	if !roles[commit.Operation.UserID].CanWrite() {
		return storage.ErrPermissionDenied
	}
	var duplicate int
	err = tx.QueryRowContext(ctx,
		`SELECT 1 FROM operation_logs WHERE document_id = ? AND request_id = ? LIMIT 1`,
		commit.Document.ID, commit.Operation.ID,
	).Scan(&duplicate)
	if err == nil {
		return storage.ErrDuplicateRequest
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("check duplicate request: %w", err)
	}
	if currentVersion != commit.ExpectedVersion ||
		commit.Document.Version != commit.ExpectedVersion+1 ||
		commit.Operation.Version != commit.Document.Version {
		return storage.ErrVersionConflict
	}

	result, err := tx.ExecContext(ctx,
		`UPDATE documents SET content = ?, version = ?, updated_at = CURRENT_TIMESTAMP
		 WHERE id = ? AND version = ? AND owner_node_id = ? AND fencing_token = ?`,
		commit.Document.Content, commit.Document.Version, commit.Document.ID, commit.ExpectedVersion,
		commit.Authority.NodeID, commit.Authority.FencingToken,
	)
	if err != nil {
		return fmt.Errorf("update document: %w", err)
	}
	if rows, err := result.RowsAffected(); err != nil {
		return fmt.Errorf("check document update: %w", err)
	} else if rows != 1 {
		return storage.ErrVersionConflict
	}
	operation := commit.Operation.Operation
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO operation_logs
			(document_id, version, request_id, user_id, base_version, operation_kind, position, text, delete_length, fencing_token)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		commit.Document.ID,
		commit.Operation.Version,
		operation.ID,
		operation.UserID,
		operation.BaseVersion,
		operation.Kind,
		operation.Position,
		operation.Text,
		operation.Length,
		commit.Authority.FencingToken,
	); err != nil {
		if isDuplicate(err) {
			return storage.ErrDuplicateRequest
		}
		return fmt.Errorf("insert operation log: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit edit transaction: %w", err)
	}
	return nil
}

func (s *Store) LoadOperations(ctx context.Context, docID string) ([]collab.AppliedOperation, error) {
	ctx, cancel := s.withTimeout(ctx)
	defer cancel()
	if _, err := s.GetDocument(ctx, docID); err != nil {
		return nil, err
	}
	return loadOperations(ctx, s.db, docID)
}

func (s *Store) LoadOperationsAfter(ctx context.Context, docID string, version uint64) ([]collab.AppliedOperation, error) {
	ctx, cancel := s.withTimeout(ctx)
	defer cancel()
	if _, err := getDocument(ctx, s.db, docID); err != nil {
		return nil, err
	}
	return loadOperationsAfter(ctx, s.db, docID, version)
}

func (s *Store) GetAppliedOperation(ctx context.Context, docID, requestID string) (collab.AppliedOperation, error) {
	ctx, cancel := s.withTimeout(ctx)
	defer cancel()
	var applied collab.AppliedOperation
	err := s.db.QueryRowContext(ctx, `
		SELECT version, request_id, user_id, base_version, operation_kind, position, text, delete_length
		FROM operation_logs WHERE document_id = ? AND request_id = ?`, docID, requestID).Scan(
		&applied.Version,
		&applied.ID,
		&applied.UserID,
		&applied.BaseVersion,
		&applied.Kind,
		&applied.Position,
		&applied.Text,
		&applied.Length,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return collab.AppliedOperation{}, storage.ErrNotFound
	}
	if err != nil {
		return collab.AppliedOperation{}, fmt.Errorf("get applied operation: %w", err)
	}
	return applied, nil
}

func (s *Store) GetLatestSnapshot(ctx context.Context, docID string) (storage.Snapshot, error) {
	ctx, cancel := s.withTimeout(ctx)
	defer cancel()
	return getLatestSnapshot(ctx, s.db, docID)
}

func (s *Store) EnsureBaselineSnapshot(ctx context.Context, docID string, authority storage.WriteAuthority) (storage.Snapshot, error) {
	if err := storage.ValidateWriteAuthority(authority); err != nil {
		return storage.Snapshot{}, err
	}
	ctx, cancel := s.withTimeout(ctx)
	defer cancel()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return storage.Snapshot{}, fmt.Errorf("begin baseline snapshot: %w", err)
	}
	defer tx.Rollback()
	var document collab.Document
	var currentNode sql.NullString
	var currentToken uint64
	if err := tx.QueryRowContext(ctx,
		`SELECT id, content, version, owner_node_id, fencing_token FROM documents WHERE id = ? FOR UPDATE`, docID,
	).Scan(&document.ID, &document.Content, &document.Version, &currentNode, &currentToken); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return storage.Snapshot{}, storage.ErrNotFound
		}
		return storage.Snapshot{}, fmt.Errorf("lock baseline document: %w", err)
	}
	if !currentNode.Valid || currentNode.String != authority.NodeID || currentToken != authority.FencingToken {
		return storage.Snapshot{}, storage.ErrFenced
	}
	snapshot, err := getLatestSnapshot(ctx, tx, docID)
	if err == nil {
		if err := tx.Commit(); err != nil {
			return storage.Snapshot{}, fmt.Errorf("commit existing baseline snapshot: %w", err)
		}
		return snapshot, nil
	}
	if !errors.Is(err, storage.ErrNotFound) {
		return storage.Snapshot{}, err
	}
	snapshot = storage.Snapshot{DocumentID: docID, Version: document.Version, Content: document.Content}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO snapshots (document_id, version, content) VALUES (?, ?, ?)`,
		snapshot.DocumentID, snapshot.Version, snapshot.Content,
	); err != nil {
		return storage.Snapshot{}, fmt.Errorf("insert baseline snapshot: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return storage.Snapshot{}, fmt.Errorf("commit baseline snapshot: %w", err)
	}
	return snapshot, nil
}

func (s *Store) SaveSnapshot(ctx context.Context, commit storage.SnapshotCommit) (resultErr error) {
	ctx, span := otel.Tracer("collab-editor/mysql").Start(ctx, "mysql.save_snapshot")
	defer span.End()
	started := time.Now()
	defer func() { s.observe("save_snapshot", started, resultErr) }()
	if err := storage.ValidateWriteAuthority(commit.Authority); err != nil {
		return err
	}
	snapshot := commit.Snapshot
	ctx, cancel := s.withTimeout(ctx)
	defer cancel()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin save snapshot: %w", err)
	}
	defer tx.Rollback()
	var currentVersion, currentToken uint64
	var currentNode sql.NullString
	if err := tx.QueryRowContext(ctx,
		`SELECT version, owner_node_id, fencing_token FROM documents WHERE id = ? FOR UPDATE`, snapshot.DocumentID,
	).Scan(&currentVersion, &currentNode, &currentToken); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return storage.ErrNotFound
		}
		return fmt.Errorf("lock snapshot document: %w", err)
	}
	if !currentNode.Valid || currentNode.String != commit.Authority.NodeID || currentToken != commit.Authority.FencingToken {
		return storage.ErrFenced
	}
	if snapshot.Version > currentVersion {
		return storage.ErrVersionConflict
	}
	_, err = tx.ExecContext(ctx,
		`INSERT INTO snapshots (document_id, version, content) VALUES (?, ?, ?)`,
		snapshot.DocumentID, snapshot.Version, snapshot.Content,
	)
	if isDuplicate(err) {
		return storage.ErrAlreadyExists
	}
	if err != nil {
		return fmt.Errorf("save snapshot: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit snapshot: %w", err)
	}
	return nil
}

func (s *Store) withTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, s.queryTimeout)
}

func (s *Store) observe(operation string, started time.Time, err error) {
	if s.observer != nil {
		s.observer.MySQLOperation(operation, time.Since(started), err)
	}
}

type queryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func getWriteAuthority(ctx context.Context, query queryer, docID string) (storage.WriteAuthority, error) {
	var nodeID sql.NullString
	var token uint64
	if err := query.QueryRowContext(ctx,
		`SELECT owner_node_id, fencing_token FROM documents WHERE id = ?`, docID,
	).Scan(&nodeID, &token); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return storage.WriteAuthority{}, storage.ErrNotFound
		}
		return storage.WriteAuthority{}, fmt.Errorf("get write authority: %w", err)
	}
	if !nodeID.Valid {
		return storage.WriteAuthority{FencingToken: token}, nil
	}
	return storage.WriteAuthority{NodeID: nodeID.String, FencingToken: token}, nil
}

func getWriteAuthorityForUpdate(ctx context.Context, tx *sql.Tx, docID string) (storage.WriteAuthority, error) {
	var nodeID sql.NullString
	var token uint64
	if err := tx.QueryRowContext(ctx,
		`SELECT owner_node_id, fencing_token FROM documents WHERE id = ? FOR UPDATE`, docID,
	).Scan(&nodeID, &token); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return storage.WriteAuthority{}, storage.ErrNotFound
		}
		return storage.WriteAuthority{}, fmt.Errorf("lock write authority: %w", err)
	}
	if !nodeID.Valid {
		return storage.WriteAuthority{FencingToken: token}, nil
	}
	return storage.WriteAuthority{NodeID: nodeID.String, FencingToken: token}, nil
}

func getDocument(ctx context.Context, query queryer, id string) (collab.Document, error) {
	var document collab.Document
	if err := query.QueryRowContext(ctx,
		`SELECT id, content, version FROM documents WHERE id = ?`, id,
	).Scan(&document.ID, &document.Content, &document.Version); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return collab.Document{}, storage.ErrNotFound
		}
		return collab.Document{}, fmt.Errorf("get document: %w", err)
	}
	return document, nil
}

func getPermissions(ctx context.Context, query queryer, docID string) (map[string]permission.Role, error) {
	return documentPermissions(ctx, query, docID, false, "")
}

func documentPermissions(ctx context.Context, query queryer, docID string, overrideFolder bool, folderID string) (map[string]permission.Role, error) {
	rows, err := query.QueryContext(ctx,
		`WITH RECURSIVE ancestors AS (
			SELECT f.id, f.parent_id FROM folders f JOIN documents d
				ON f.id = IF(?, NULLIF(?, ''), d.folder_id) WHERE d.id = ?
			UNION ALL
			SELECT f.id, f.parent_id FROM folders f JOIN ancestors a ON a.parent_id = f.id
		), grants AS (
			SELECT user_id, CASE role WHEN 'owner' THEN 3 WHEN 'write' THEN 2 ELSE 1 END AS strength
			FROM document_permissions WHERE document_id = ?
			UNION ALL
			SELECT p.user_id, IF(p.role = 'write', 2, 1)
			FROM folder_permissions p JOIN ancestors a ON a.id = p.folder_id
		)
		SELECT user_id, ELT(MAX(strength), 'read', 'write', 'owner') FROM grants GROUP BY user_id`, overrideFolder, folderID, docID, docID,
	)
	if err != nil {
		return nil, fmt.Errorf("load permissions: %w", err)
	}
	defer rows.Close()
	result := make(map[string]permission.Role)
	for rows.Next() {
		var userID string
		var role permission.Role
		if err := rows.Scan(&userID, &role); err != nil {
			return nil, fmt.Errorf("scan permission: %w", err)
		}
		result[userID] = role
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate permissions: %w", err)
	}
	return result, nil
}

func loadOperations(ctx context.Context, query queryer, docID string) ([]collab.AppliedOperation, error) {
	rows, err := query.QueryContext(ctx, `
		SELECT version, request_id, user_id, base_version, operation_kind, position, text, delete_length
		FROM operation_logs
		WHERE document_id = ?
		ORDER BY version ASC`, docID)
	if err != nil {
		return nil, fmt.Errorf("load operations: %w", err)
	}
	return scanOperations(rows, "")
}

func loadOperationsAfter(ctx context.Context, query queryer, docID string, version uint64) ([]collab.AppliedOperation, error) {
	rows, err := query.QueryContext(ctx, `
		SELECT version, request_id, user_id, base_version, operation_kind, position, text, delete_length
		FROM operation_logs
		WHERE document_id = ? AND version > ?
		ORDER BY version ASC`, docID, version)
	if err != nil {
		return nil, fmt.Errorf("load operations after version: %w", err)
	}
	return scanOperations(rows, " after version")
}

func scanOperations(rows *sql.Rows, errorSuffix string) ([]collab.AppliedOperation, error) {
	defer rows.Close()
	var result []collab.AppliedOperation
	for rows.Next() {
		var applied collab.AppliedOperation
		if err := rows.Scan(
			&applied.Version,
			&applied.ID,
			&applied.UserID,
			&applied.BaseVersion,
			&applied.Kind,
			&applied.Position,
			&applied.Text,
			&applied.Length,
		); err != nil {
			return nil, fmt.Errorf("scan operation%s: %w", errorSuffix, err)
		}
		result = append(result, applied)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate operations%s: %w", errorSuffix, err)
	}
	return result, nil
}

func getLatestSnapshot(ctx context.Context, query queryer, docID string) (storage.Snapshot, error) {
	var snapshot storage.Snapshot
	err := query.QueryRowContext(ctx, `
		SELECT document_id, version, content
		FROM snapshots WHERE document_id = ?
		ORDER BY version DESC LIMIT 1`, docID).Scan(&snapshot.DocumentID, &snapshot.Version, &snapshot.Content)
	if errors.Is(err, sql.ErrNoRows) {
		return storage.Snapshot{}, storage.ErrNotFound
	}
	if err != nil {
		return storage.Snapshot{}, fmt.Errorf("get latest snapshot: %w", err)
	}
	return snapshot, nil
}

func isDuplicate(err error) bool {
	var mysqlError *driver.MySQLError
	return errors.As(err, &mysqlError) && mysqlError.Number == duplicateEntry
}
