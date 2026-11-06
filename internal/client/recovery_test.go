package client

import (
	"strings"
	"testing"

	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/collab"
	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/protocol"
)

func TestApplyOperationsValidatesContinuityAndTarget(t *testing.T) {
	operations := []protocol.VersionedOperation{
		{Version: 2, Operation: collab.Operation{Kind: collab.OperationInsert, Position: 1, Text: "B"}},
		{Version: 3, Operation: collab.Operation{Kind: collab.OperationDelete, Position: 0, Length: 1}},
	}
	content, version, err := applyOperations("A", 1, operations, 3)
	if err != nil || content != "B" || version != 3 {
		t.Fatalf("applyOperations = %q,%d,%v", content, version, err)
	}
	operations[1].Version = 4
	if _, _, err := applyOperations("A", 1, operations, 4); err == nil || !strings.Contains(err.Error(), "continuous") {
		t.Fatalf("gap error = %v", err)
	}
	if _, _, err := applyOperations("A", 1, operations[:1], 3); err == nil || !strings.Contains(err.Error(), "target") {
		t.Fatalf("target error = %v", err)
	}
}
