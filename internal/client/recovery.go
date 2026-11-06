package client

import (
	"fmt"

	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/collab"
	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/protocol"
)

func applyOperations(content string, current uint64, operations []protocol.VersionedOperation, target uint64) (string, uint64, error) {
	for _, versioned := range operations {
		if versioned.Version != current+1 {
			return "", current, fmt.Errorf("operation version %d is not continuous after %d", versioned.Version, current)
		}
		updated, err := collab.Apply(content, versioned.Operation)
		if err != nil {
			return "", current, fmt.Errorf("apply version %d: %w", versioned.Version, err)
		}
		content = updated
		current = versioned.Version
	}
	if current != target {
		return "", current, fmt.Errorf("recovered version %d does not match target %d", current, target)
	}
	return content, current, nil
}
