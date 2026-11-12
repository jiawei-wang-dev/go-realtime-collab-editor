package mysql

import (
	"context"
	"database/sql"
	"errors"

	driver "github.com/go-sql-driver/mysql"
	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/permission"
	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/storage"
)

func (s *Store) DeleteEmptyFolder(ctx context.Context, folderID, ownerID string) error {
	ctx, cancel := s.withTimeout(ctx)
	defer cancel()
	result, err := s.db.ExecContext(ctx, "DELETE FROM folders WHERE id = ? AND owner_id = ?", folderID, ownerID)
	var mysqlErr *driver.MySQLError
	if errors.As(err, &mysqlErr) && mysqlErr.Number == 1451 {
		return storage.ErrFolderNotEmpty
	}
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err == nil && count == 0 {
		return storage.ErrPermissionDenied
	}
	return err
}

func folderAccess(ctx context.Context, tx *sql.Tx, folderID string) ([]storage.FolderAccess, error) {
	rows, err := tx.QueryContext(ctx, `
		WITH RECURSIVE ancestors AS (
			SELECT id, parent_id, name FROM folders WHERE id = ?
			UNION ALL
			SELECT f.id, f.parent_id, f.name FROM folders f JOIN ancestors a ON f.id = a.parent_id
		)
		SELECT p.user_id, COALESCE(u.username, p.user_id), p.role, a.id, a.name
		FROM ancestors a JOIN folder_permissions p ON p.folder_id = a.id
		LEFT JOIN users u ON u.id COLLATE utf8mb4_bin = p.user_id
		ORDER BY 2, 5, 4`, folderID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []storage.FolderAccess{}
	for rows.Next() {
		var grant storage.FolderAccess
		if err := rows.Scan(&grant.UserID, &grant.Username, &grant.Role, &grant.FolderID, &grant.FolderName); err != nil {
			return nil, err
		}
		result = append(result, grant)
	}
	return result, rows.Err()
}

func (s *Store) ListFolderAccess(ctx context.Context, folderID, ownerID string) ([]storage.FolderAccess, error) {
	ctx, cancel := s.withTimeout(ctx)
	defer cancel()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if err := lockOwnedFolder(ctx, tx, folderID, ownerID); err != nil {
		return nil, err
	}
	return folderAccess(ctx, tx, folderID)
}

func (s *Store) ListDocumentAccess(ctx context.Context, documentID, ownerID string) ([]storage.FolderAccess, error) {
	ctx, cancel := s.withTimeout(ctx)
	defer cancel()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var folderID sql.NullString
	var role permission.Role
	err = tx.QueryRowContext(ctx, `SELECT d.folder_id, p.role FROM documents d JOIN document_permissions p
		ON p.document_id = d.id WHERE d.id = ? AND p.user_id = ? FOR UPDATE`, documentID, ownerID).Scan(&folderID, &role)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && !role.CanChangePermissions()) {
		return nil, storage.ErrPermissionDenied
	}
	if err != nil {
		return nil, err
	}
	result, err := folderAccess(ctx, tx, folderID.String)
	if err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT p.user_id, COALESCE(u.username, p.user_id), p.role
		FROM document_permissions p LEFT JOIN users u ON u.id COLLATE utf8mb4_bin = p.user_id
		WHERE p.document_id = ? ORDER BY 2, 1`, documentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var grant storage.FolderAccess
		if err := rows.Scan(&grant.UserID, &grant.Username, &grant.Role); err != nil {
			return nil, err
		}
		result = append(result, grant)
	}
	return result, rows.Err()
}

func (s *Store) MoveFolder(ctx context.Context, folderID, ownerID, parentID string, preview bool) ([]storage.AccessChange, error) {
	ctx, cancel := s.withTimeout(ctx)
	defer cancel()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	// Concurrent moves must not form a cycle between two otherwise valid targets.
	rows, err := tx.QueryContext(ctx, "SELECT id, COALESCE(parent_id, '') FROM folders WHERE owner_id = ? ORDER BY id FOR UPDATE", ownerID)
	if err != nil {
		return nil, err
	}
	parents := map[string]string{}
	for rows.Next() {
		var id, parent string
		if err := rows.Scan(&id, &parent); err != nil {
			rows.Close()
			return nil, err
		}
		parents[id] = parent
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if _, ok := parents[folderID]; !ok {
		return nil, storage.ErrPermissionDenied
	}
	for ancestor := parentID; ancestor != ""; ancestor = parents[ancestor] {
		if ancestor == folderID {
			return nil, storage.ErrFolderCycle
		}
		if _, ok := parents[ancestor]; !ok {
			return nil, storage.ErrPermissionDenied
		}
	}
	var before []storage.FolderAccess
	if preview {
		before, err = folderAccess(ctx, tx, folderID)
		if err != nil {
			return nil, err
		}
	}
	if _, err := tx.ExecContext(ctx, "UPDATE folders SET parent_id = NULLIF(?, '') WHERE id = ?", parentID, folderID); err != nil {
		if isDuplicate(err) {
			return nil, storage.ErrAlreadyExists
		}
		return nil, err
	}
	if !preview {
		return nil, tx.Commit()
	}
	after, err := folderAccess(ctx, tx, folderID)
	if err != nil {
		return nil, err
	}
	oldRoles, newRoles := map[string]permission.Role{}, map[string]permission.Role{}
	names := map[string]string{}
	for _, side := range []struct {
		grants []storage.FolderAccess
		roles  map[string]permission.Role
	}{{before, oldRoles}, {after, newRoles}} {
		for _, grant := range side.grants {
			names[grant.UserID] = grant.Username
			if grant.UserID != ownerID && (side.roles[grant.UserID] == "" || grant.Role.CanWrite()) {
				side.roles[grant.UserID] = grant.Role
			}
		}
	}
	changes := accessChanges(oldRoles, newRoles)
	for i := range changes {
		changes[i].Username = names[changes[i].UserID]
	}
	return changes, nil
}
