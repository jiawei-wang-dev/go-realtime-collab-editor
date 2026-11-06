package clientstorage

import (
	"time"

	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/collab"
)

type Document struct {
	DocID         string
	ServerVersion uint64
	Content       string
	UpdatedAt     time.Time
}

type PendingEdit struct {
	ID          int64
	DocID       string
	RequestID   string
	BaseVersion uint64
	Operation   collab.Operation
	Status      string
	RetryCount  int
	Error       string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

type RefreshAttempt struct {
	RequestID           string
	CredentialRequestID string
	Status              string
	CreatedAt           time.Time
	UpdatedAt           time.Time
}

type Recovery struct {
	ActiveDocument string
	Document       *Document
	Pending        []PendingEdit
	RefreshAttempt *RefreshAttempt
}
