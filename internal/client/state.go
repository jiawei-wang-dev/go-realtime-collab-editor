package client

import "time"

type State string

const (
	StateDisconnected    State = "DISCONNECTED"
	StateConnecting      State = "CONNECTING"
	StateAuthenticating  State = "AUTHENTICATING"
	StateSyncing         State = "SYNCING"
	StateConnected       State = "CONNECTED"
	StateReconnecting    State = "RECONNECTING"
	StateRefreshingToken State = "REFRESHING_TOKEN"
	StateClosed          State = "CLOSED"
)

type PendingState string

const (
	PendingQueued PendingState = "QUEUED"
	PendingSent   PendingState = "SENT"
	PendingFailed PendingState = "FAILED"
)

type PendingStatus struct {
	RequestID   string       `json:"request_id"`
	DocumentID  string       `json:"doc_id"`
	BaseVersion uint64       `json:"base_version"`
	Operation   string       `json:"operation"`
	State       PendingState `json:"state"`
	RetryCount  int          `json:"retry_count"`
	Error       string       `json:"error,omitempty"`
}

type Status struct {
	State                State           `json:"state"`
	DocumentID           string          `json:"document_id,omitempty"`
	ServerVersion        uint64          `json:"server_version"`
	Authenticated        bool            `json:"authenticated"`
	Reconnecting         bool            `json:"reconnecting"`
	ReconnectAttempts    int             `json:"reconnect_attempts"`
	LastDisconnectReason string          `json:"last_disconnect_reason,omitempty"`
	Pending              []PendingStatus `json:"pending_operations"`
	UpdatedAt            time.Time       `json:"updated_at"`
	OwnerEndpoint        string          `json:"owner_endpoint,omitempty"`
}
