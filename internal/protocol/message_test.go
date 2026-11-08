package protocol

import (
	"encoding/json"

	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/permission"
	"testing"
)

func TestJoinedMessageIncludesZeroVersionAndEmptyContent(t *testing.T) {
	payload, err := json.Marshal(Message{
		Type:  TypeJoined,
		DocID: "empty",
		Role:  permission.RoleOwner,
	})
	if err != nil {
		t.Fatal(err)
	}

	var decoded map[string]any
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatal(err)
	}
	if _, ok := decoded["version"]; !ok {
		t.Fatalf("joined payload omits version: %s", payload)
	}
	if _, ok := decoded["content"]; !ok {
		t.Fatalf("joined payload omits content: %s", payload)
	}
}

func TestSyncMessageIncludesZeroVersionsAndEmptyOperations(t *testing.T) {
	payload, err := json.Marshal(Message{Type: TypeSync, DocID: "doc", Role: permission.RoleWrite})
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"from_version", "to_version", "operations", "role"} {
		if _, ok := decoded[field]; !ok {
			t.Fatalf("sync payload omits %s: %s", field, payload)
		}
	}
}

func TestResyncMessageIncludesSnapshotAndCurrentVersion(t *testing.T) {
	payload, err := json.Marshal(Message{
		Type:     TypeResyncRequired,
		DocID:    "doc",
		Snapshot: &Snapshot{Version: 0, Content: ""},
		Role:     permission.RoleRead,
	})
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"snapshot", "operations", "current_version", "role"} {
		if _, ok := decoded[field]; !ok {
			t.Fatalf("resync payload omits %s: %s", field, payload)
		}
	}
}
