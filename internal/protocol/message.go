package protocol

import (
	"encoding/json"
	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/collab"
	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/permission"
)

const (
	TypeJoin              = "join"
	TypeJoined            = "joined"
	TypeEdit              = "edit"
	TypeEditApplied       = "edit_applied"
	TypePermissionChanged = "permission_changed"
	TypeError             = "error"
)

const (
	CodeBadMessage       = "BAD_MESSAGE"
	CodePermissionDenied = "PERMISSION_DENIED"
	CodeVersionAhead     = "VERSION_AHEAD"
	CodeInvalidOperation = "INVALID_OPERATION"
	CodeInternalError    = "INTERNAL_ERROR"
)

type Message struct {
	Type        string            `json:"type"`
	RequestID   string            `json:"request_id,omitempty"`
	DocID       string            `json:"doc_id,omitempty"`
	BaseVersion uint64            `json:"base_version,omitempty"`
	Version     uint64            `json:"version,omitempty"`
	Content     string            `json:"content,omitempty"`
	Role        permission.Role   `json:"role,omitempty"`
	Operation   *collab.Operation `json:"operation,omitempty"`
	Code        string            `json:"code,omitempty"`
	Message     string            `json:"message,omitempty"`
}

// MarshalJSON keeps zero-valued version and empty content visible in the
// initial joined snapshot while retaining compact messages for other types.
func (m Message) MarshalJSON() ([]byte, error) {
	if m.Type == TypeJoined {
		return json.Marshal(struct {
			Type    string          `json:"type"`
			DocID   string          `json:"doc_id"`
			Version uint64          `json:"version"`
			Content string          `json:"content"`
			Role    permission.Role `json:"role"`
		}{
			Type:    m.Type,
			DocID:   m.DocID,
			Version: m.Version,
			Content: m.Content,
			Role:    m.Role,
		})
	}

	type messageAlias Message
	return json.Marshal(messageAlias(m))
}

func Error(code, message string) Message {
	return Message{Type: TypeError, Code: code, Message: message}
}
