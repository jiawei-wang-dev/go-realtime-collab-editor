package storage

import (
	"context"
	"errors"

	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/collab"
)

var ErrHistoryUnavailable = errors.New("document history is unavailable at this version")

type HistoryEntry struct {
	Version uint64               `json:"version"`
	UserID  string               `json:"user_id,omitempty"`
	Kind    collab.OperationKind `json:"kind,omitempty"`
}

type HistoryStore interface {
	ListHistory(ctx context.Context, documentID string, before uint64, limit int) ([]HistoryEntry, error)
	DocumentAtVersion(ctx context.Context, documentID string, version uint64) (collab.Document, error)
}

func ReconstructVersion(snapshot Snapshot, operations []collab.AppliedOperation, version uint64) (collab.Document, error) {
	document := collab.Document{ID: snapshot.DocumentID, Content: snapshot.Content, Version: snapshot.Version}
	for _, operation := range operations {
		if operation.Version > version {
			break
		}
		if operation.Version != document.Version+1 {
			return collab.Document{}, ErrHistoryUnavailable
		}
		content, err := collab.Apply(document.Content, operation.Operation)
		if err != nil {
			return collab.Document{}, err
		}
		document.Content, document.Version = content, operation.Version
	}
	if document.Version != version {
		return collab.Document{}, ErrHistoryUnavailable
	}
	return document, nil
}
