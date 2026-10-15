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
