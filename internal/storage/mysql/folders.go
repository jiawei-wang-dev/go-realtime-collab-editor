package mysql

import (
	"context"
	"database/sql"
	"errors"
	"sort"

	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/permission"
	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/storage"
)

var _ storage.FolderStore = (*Store)(nil)

func (s *Store) RenameFolder(ctx context.Context, folderID, ownerID, name string) error {
	ctx, cancel := s.withTimeout(ctx)
	defer cancel()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := lockOwnedFolder(ctx, tx, folderID, ownerID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "UPDATE folders SET name = ? WHERE id = ?", name, folderID); err != nil {
		if isDuplicate(err) {
			return storage.ErrAlreadyExists
		}
		return err
	}
	return tx.Commit()
}

func (s *Store) DocumentName(ctx context.Context, documentID string) (string, error) {
	ctx, cancel := s.withTimeout(ctx)
	defer cancel()
	var name string
	err := s.db.QueryRowContext(ctx, "SELECT COALESCE(display_name, '') FROM documents WHERE id = ?", documentID).Scan(&name)
	return name, err
}

func (s *Store) RenameDocument(ctx context.Context, documentID, ownerID, name string) error {
	ctx, cancel := s.withTimeout(ctx)
	defer cancel()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := lockMove(ctx, tx, documentID, ownerID, ""); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "UPDATE documents SET display_name = ? WHERE id = ?", name, documentID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) MoveDocument(ctx context.Context, documentID, ownerID, folderID string) error {
	ctx, cancel := s.withTimeout(ctx)
	defer cancel()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := lockMove(ctx, tx, documentID, ownerID, folderID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "UPDATE documents SET folder_id = NULLIF(?, '') WHERE id = ?", folderID, documentID); err != nil {
		return err
	}
	return tx.Commit()
}

func lockMove(ctx context.Context, tx *sql.Tx, documentID, ownerID, folderID string) error {
	var owner string
	err := tx.QueryRowContext(ctx, "SELECT owner_id FROM documents WHERE id = ? FOR UPDATE", documentID).Scan(&owner)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && owner != ownerID) {
		return storage.ErrPermissionDenied
	}
	if err != nil {
		return err
	}
	if folderID != "" {
		return lockOwnedFolder(ctx, tx, folderID, ownerID)
	}
	return nil
}

func (s *Store) PreviewDocumentMove(ctx context.Context, documentID, ownerID, folderID string) ([]storage.AccessChange, error) {
	ctx, cancel := s.withTimeout(ctx)
	defer cancel()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if err := lockMove(ctx, tx, documentID, ownerID, folderID); err != nil {
		return nil, err
	}
	before, err := getPermissions(ctx, tx, documentID)
	if err != nil {
		return nil, err
	}
	after, err := documentPermissions(ctx, tx, documentID, true, folderID)
	if err != nil {
		return nil, err
	}
	changes := accessChanges(before, after)
	for i := range changes {
		err := tx.QueryRowContext(ctx, "SELECT username FROM users WHERE id = ?", changes[i].UserID).Scan(&changes[i].Username)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
	}
	return changes, nil
}

func accessChanges(before, after map[string]permission.Role) []storage.AccessChange {
	changes := make([]storage.AccessChange, 0)
	for id, role := range before {
		if after[id] != role {
			changes = append(changes, storage.AccessChange{UserID: id, Before: role, After: after[id]})
		}
	}
	for id, role := range after {
		if before[id] == "" {
			changes = append(changes, storage.AccessChange{UserID: id, After: role})
		}
	}
	sort.Slice(changes, func(i, j int) bool { return changes[i].UserID < changes[j].UserID })
	return changes
}

func lockOwnedFolder(ctx context.Context, tx *sql.Tx, folderID, actorID string) error {
	var owner string
	err := tx.QueryRowContext(ctx, "SELECT owner_id FROM folders WHERE id = ? FOR UPDATE", folderID).Scan(&owner)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && owner != actorID) {
		return storage.ErrPermissionDenied
	}
	return err
}

func (s *Store) ListFolders(ctx context.Context, userID string) ([]storage.Folder, error) {
	ctx, cancel := s.withTimeout(ctx)
	defer cancel()
	rows, err := s.db.QueryContext(ctx, `
		WITH RECURSIVE visible AS (
			SELECT id AS folder_id, 3 AS strength FROM folders WHERE owner_id = ?
			UNION DISTINCT
			SELECT folder_id, IF(role = 'write', 2, 1) FROM folder_permissions WHERE user_id = ?
			UNION DISTINCT
			SELECT f.id, v.strength FROM folders f JOIN visible v ON f.parent_id = v.folder_id
		), effective AS (
			SELECT folder_id, MAX(strength) AS strength FROM visible GROUP BY folder_id
		)
		SELECT f.id, COALESCE(p.folder_id, ''), f.name, ELT(e.strength, 'read', 'write', 'owner')
		FROM folders f JOIN effective e ON e.folder_id = f.id
		LEFT JOIN effective p ON p.folder_id = f.parent_id
		ORDER BY f.name, f.id`, userID, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]storage.Folder, 0)
	for rows.Next() {
		var folder storage.Folder
		if err := rows.Scan(&folder.ID, &folder.ParentID, &folder.Name, &folder.Role); err != nil {
			return nil, err
		}
		result = append(result, folder)
	}
	return result, rows.Err()
}

func (s *Store) CreateFolder(ctx context.Context, folder storage.Folder, ownerID string) error {
	ctx, cancel := s.withTimeout(ctx)
	defer cancel()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if folder.ParentID != "" {
		if err := lockOwnedFolder(ctx, tx, folder.ParentID, ownerID); err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO folders (id, owner_id, parent_id, name) VALUES (?, ?, NULLIF(?, ''), ?)",
		folder.ID, ownerID, folder.ParentID, folder.Name)
	if isDuplicate(err) {
		return storage.ErrAlreadyExists
	}
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) SetFolderPermission(ctx context.Context, folderID, actorID, userID string, role permission.Role) error {
	if userID == "" || (role != "" && role != permission.RoleRead && role != permission.RoleWrite) {
		return storage.ErrPermissionDenied
	}
	ctx, cancel := s.withTimeout(ctx)
	defer cancel()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := lockOwnedFolder(ctx, tx, folderID, actorID); err != nil {
		return err
	}
	if role == "" {
		_, err = tx.ExecContext(ctx, "DELETE FROM folder_permissions WHERE folder_id = ? AND user_id = ?", folderID, userID)
	} else {
		_, err = tx.ExecContext(ctx, `INSERT INTO folder_permissions (folder_id, user_id, role) VALUES (?, ?, ?)
			ON DUPLICATE KEY UPDATE role = VALUES(role)`, folderID, userID, role)
	}
	if err != nil {
		return err
	}
	return tx.Commit()
}
