package storage

import (
	"fmt"

	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/collab"
)

// Replay applies already-transformed operations exactly as persisted. It does
// not run OT again or produce persistence side effects.
func Replay(snapshot Snapshot, operations []collab.AppliedOperation) (collab.Document, error) {
	content := snapshot.Content
	version := snapshot.Version
	for _, applied := range operations {
		if applied.Version != version+1 {
			return collab.Document{}, fmt.Errorf("replay version gap: expected %d, got %d", version+1, applied.Version)
		}
		next, err := collab.Apply(content, applied.Operation)
		if err != nil {
			return collab.Document{}, fmt.Errorf("replay version %d: %w", applied.Version, err)
		}
		content = next
		version = applied.Version
	}
	return collab.Document{ID: snapshot.DocumentID, Content: content, Version: version}, nil
}

func ValidateRecoveryState(state RecoveryState) error {
	if state.Snapshot.DocumentID != state.Document.ID {
		return fmt.Errorf("snapshot document %q does not match document %q", state.Snapshot.DocumentID, state.Document.ID)
	}
	if state.Snapshot.Version > state.Document.Version {
		return fmt.Errorf("snapshot version %d is ahead of document version %d", state.Snapshot.Version, state.Document.Version)
	}
	replayed, err := Replay(state.Snapshot, state.Operations)
	if err != nil {
		return err
	}
	if replayed.Version != state.Document.Version {
		return fmt.Errorf("replayed version %d does not match document version %d", replayed.Version, state.Document.Version)
	}
	if replayed.Content != state.Document.Content {
		return fmt.Errorf("replayed content does not match stored document")
	}
	return nil
}
