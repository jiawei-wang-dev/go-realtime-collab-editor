package protocol

import (
	"encoding/json"

	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/collab"
	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/permission"
)

const (
	TypeJoin              = "join"
	TypeAuth              = "auth"
	TypeAuthOK            = "auth_ok"
	TypeJoined            = "joined"
	TypeEdit              = "edit"
	TypeEditApplied       = "edit_applied"
	TypePermissionChanged = "permission_changed"
	TypeResume            = "resume"
	TypeSync              = "sync"
	TypeResyncRequired    = "resync_required"
	TypeError             = "error"
	TypeOwnerRedirect     = "owner_redirect"
)

const (
	CodeBadMessage         = "BAD_MESSAGE"
	CodePermissionDenied   = "PERMISSION_DENIED"
	CodeVersionAhead       = "VERSION_AHEAD"
	CodeVersionConflict    = "VERSION_CONFLICT"
	CodeDuplicateRequest   = "DUPLICATE_REQUEST"
	CodeInvalidOperation   = "INVALID_OPERATION"
	CodeResyncRequired     = "RESYNC_REQUIRED"
	CodeServiceUnavailable = "SERVICE_UNAVAILABLE"
	CodeInternalError      = "INTERNAL_ERROR"
)

type Snapshot struct {
	Version uint64 `json:"version"`
	Content string `json:"content"`
}

type VersionedOperation struct {
	Version   uint64           `json:"version"`
	Operation collab.Operation `json:"operation"`
}

type Message struct {
	Type           string               `json:"type"`
	RequestID      string               `json:"request_id,omitempty"`
	DocID          string               `json:"doc_id,omitempty"`
	BaseVersion    uint64               `json:"base_version,omitempty"`
	LastVersion    uint64               `json:"last_version,omitempty"`
	Version        uint64               `json:"version,omitempty"`
	FromVersion    uint64               `json:"from_version,omitempty"`
	ToVersion      uint64               `json:"to_version,omitempty"`
	CurrentVersion uint64               `json:"current_version,omitempty"`
	Content        string               `json:"content,omitempty"`
	Role           permission.Role      `json:"role,omitempty"`
	Operation      *collab.Operation    `json:"operation,omitempty"`
	Operations     []VersionedOperation `json:"operations,omitempty"`
	Snapshot       *Snapshot            `json:"snapshot,omitempty"`
	Code           string               `json:"code,omitempty"`
	Message        string               `json:"message,omitempty"`
	Token          string               `json:"token,omitempty"`
	Endpoint       string               `json:"endpoint,omitempty"`
}

// MarshalJSON preserves zero versions and empty content in the initial joined
// snapshot while keeping other messages compact.
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
	if m.Type == TypeSync {
		return json.Marshal(struct {
			Type        string               `json:"type"`
			DocID       string               `json:"doc_id"`
			FromVersion uint64               `json:"from_version"`
			ToVersion   uint64               `json:"to_version"`
			Operations  []VersionedOperation `json:"operations"`
		}{m.Type, m.DocID, m.FromVersion, m.ToVersion, nonNilOperations(m.Operations)})
	}
	if m.Type == TypeResyncRequired {
		return json.Marshal(struct {
			Type           string               `json:"type"`
			DocID          string               `json:"doc_id"`
			Snapshot       *Snapshot            `json:"snapshot"`
			Operations     []VersionedOperation `json:"operations"`
			CurrentVersion uint64               `json:"current_version"`
		}{m.Type, m.DocID, m.Snapshot, nonNilOperations(m.Operations), m.CurrentVersion})
	}

	type messageAlias Message
	return json.Marshal(messageAlias(m))
}

func nonNilOperations(operations []VersionedOperation) []VersionedOperation {
	if operations == nil {
		return []VersionedOperation{}
	}
	return operations
}

func Error(code, message string) Message {
	return Message{Type: TypeError, Code: code, Message: message}
}
