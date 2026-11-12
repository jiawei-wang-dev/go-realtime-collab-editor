package mysql

import (
	"context"
	"database/sql"
	"errors"

	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/collab"
	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/storage"
)

var _ storage.HistoryStore = (*Store)(nil)

func (s *Store) ListHistory(ctx context.Context, id string, before uint64, limit int) ([]storage.HistoryEntry, error) {
	ctx, cancel := s.withTimeout(ctx)
	defer cancel()
	rows, err := s.db.QueryContext(ctx, `
		SELECT version, user_id, operation_kind FROM operation_logs
		WHERE document_id = ? AND version < ?
		ORDER BY version DESC LIMIT ?`, id, before, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	entries := make([]storage.HistoryEntry, 0, limit)
	for rows.Next() {
		var entry storage.HistoryEntry
		if err := rows.Scan(&entry.Version, &entry.UserID, &entry.Kind); err != nil {
			return nil, err
		}
		entries = append(entries, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(entries) < limit && before > 0 {
		var baseline uint64
		err := s.db.QueryRowContext(ctx, "SELECT version FROM snapshots WHERE document_id = ? ORDER BY version LIMIT 1", id).Scan(&baseline)
		if err == nil && baseline < before && (len(entries) == 0 || entries[len(entries)-1].Version > baseline) {
			entries = append(entries, storage.HistoryEntry{Version: baseline})
		} else if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
	}
	return entries, nil
}

func (s *Store) DocumentAtVersion(ctx context.Context, id string, version uint64) (collab.Document, error) {
	ctx, cancel := s.withTimeout(ctx)
	defer cancel()
	var snapshot storage.Snapshot
	err := s.db.QueryRowContext(ctx, `SELECT document_id, version, content FROM snapshots
		WHERE document_id = ? AND version <= ? ORDER BY version DESC LIMIT 1`, id, version).
		Scan(&snapshot.DocumentID, &snapshot.Version, &snapshot.Content)
	if errors.Is(err, sql.ErrNoRows) {
		return collab.Document{}, storage.ErrHistoryUnavailable
	}
	if err != nil {
		return collab.Document{}, err
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT version, request_id, user_id, base_version, operation_kind, position, text, delete_length
		FROM operation_logs WHERE document_id = ? AND version > ? AND version <= ?
		ORDER BY version ASC`, id, snapshot.Version, version)
	if err != nil {
		return collab.Document{}, err
	}
	defer rows.Close()
	var operations []collab.AppliedOperation
	for rows.Next() {
		var item collab.AppliedOperation
		if err := rows.Scan(&item.Version, &item.ID, &item.UserID, &item.BaseVersion, &item.Kind, &item.Position, &item.Text, &item.Length); err != nil {
			return collab.Document{}, err
		}
		operations = append(operations, item)
	}
	if err := rows.Err(); err != nil {
		return collab.Document{}, err
	}
	return storage.ReconstructVersion(snapshot, operations, version)
}
