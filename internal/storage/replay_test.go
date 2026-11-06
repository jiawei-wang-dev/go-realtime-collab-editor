package storage

import (
	"strings"
	"testing"

	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/collab"
)

func TestReplayUsesAppliedOperationsInVersionOrder(t *testing.T) {
	snapshot := Snapshot{DocumentID: "doc", Version: 1, Content: "aX"}
	operations := []collab.AppliedOperation{{
		Operation: collab.Operation{Kind: collab.OperationInsert, Position: 2, Text: "Y"},
		Version:   2,
	}}
	document, err := Replay(snapshot, operations)
	if err != nil {
		t.Fatal(err)
	}
	if document.Content != "aXY" || document.Version != 2 {
		t.Fatalf("replayed document = %#v", document)
	}
}

func TestReplayRejectsVersionGap(t *testing.T) {
	_, err := Replay(Snapshot{DocumentID: "doc", Version: 1, Content: "a"}, []collab.AppliedOperation{{
		Operation: collab.Operation{Kind: collab.OperationInsert, Position: 1, Text: "b"},
		Version:   3,
	}})
	if err == nil || !strings.Contains(err.Error(), "version gap") {
		t.Fatalf("gap error = %v", err)
	}
}

func TestValidateRecoveryStateRejectsContentMismatch(t *testing.T) {
	state := RecoveryState{
		Document: DocumentForTest("doc", "wrong", 1),
		Snapshot: Snapshot{DocumentID: "doc", Version: 0, Content: "a"},
		Operations: []collab.AppliedOperation{{
			Operation: collab.Operation{Kind: collab.OperationInsert, Position: 1, Text: "b"},
			Version:   1,
		}},
	}
	if err := ValidateRecoveryState(state); err == nil || !strings.Contains(err.Error(), "content") {
		t.Fatalf("content mismatch error = %v", err)
	}
}

func DocumentForTest(id, content string, version uint64) collab.Document {
	return collab.Document{ID: id, Content: content, Version: version}
}
