package storage

import (
	"context"
	"errors"
	"fmt"

	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/collab"
	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/permission"
)

var (
	ErrNotFound          = errors.New("storage: not found")
	ErrDocumentNotFound  = ErrNotFound // Kept for API compatibility.
	ErrAlreadyExists     = errors.New("storage: already exists")
	ErrVersionConflict   = errors.New("storage: version conflict")
	ErrPermissionDenied  = errors.New("storage: permission denied")
	ErrDuplicateRequest  = errors.New("storage: duplicate request")
	ErrFenced            = errors.New("storage: write authority is fenced")
	ErrOwnershipConflict = errors.New("storage: ownership conflict")
)

type WriteAuthority struct {
	NodeID       string
	FencingToken uint64
}

var LocalAuthority = WriteAuthority{NodeID: "local-single-node", FencingToken: 1}

func (a WriteAuthority) Valid() bool {
	return a.NodeID != "" && a.FencingToken > 0
}

func (a WriteAuthority) IsZero() bool {
	return a.NodeID == "" && a.FencingToken == 0
}

func ValidateWriteAuthority(authority WriteAuthority) error {
	if !authority.Valid() {
		return fmt.Errorf("write authority requires a non-empty node id and positive fencing token")
	}
	return nil
}

type DocumentState struct {
	Document    collab.Document
	Operations  []collab.AppliedOperation
	Permissions map[string]permission.Role
}

// RecoveryState contains a durable checkpoint and the ordered operations that
// follow it, allowing a Room to recover without trusting documents.content alone.
type RecoveryState struct {
	Document    collab.Document
	Snapshot    Snapshot
	Operations  []collab.AppliedOperation
	Permissions map[string]permission.Role
}

type EditCommit struct {
	Authority       WriteAuthority
	ExpectedVersion uint64
	Document        collab.Document
	Operation       collab.AppliedOperation
}

type PermissionCommit struct {
	Authority  WriteAuthority
	DocumentID string
	ActorID    string
	UserID     string
	Role       permission.Role
}

type SnapshotCommit struct {
	Authority WriteAuthority
	Snapshot  Snapshot
}

type Snapshot struct {
	DocumentID string
	Version    uint64
	Content    string
}

// Store is the persistence boundary shared by the API and collaboration Rooms.
// Implementations must make CreateDocument, CommitEdit, and SetPermission
// atomic. A successful return means the change is durable in that Store.
type Store interface {
	CreateDocument(ctx context.Context, id, content, ownerID string) (collab.Document, error)
	GetWriteAuthority(ctx context.Context, docID string) (WriteAuthority, error)
	ActivateOwnership(ctx context.Context, docID string, authority WriteAuthority) error
	LoadDocument(ctx context.Context, id string) (DocumentState, error)
	LoadRecoveryState(ctx context.Context, id string, authority WriteAuthority) (RecoveryState, error)
	GetDocument(ctx context.Context, id string) (collab.Document, error)
	GetPermission(ctx context.Context, docID, userID string) (permission.Role, error)
	GetPermissions(ctx context.Context, docID string) (map[string]permission.Role, error)
	SetPermission(ctx context.Context, commit PermissionCommit) error
	CommitEdit(ctx context.Context, commit EditCommit) error
	LoadOperations(ctx context.Context, docID string) ([]collab.AppliedOperation, error)
	LoadOperationsAfter(ctx context.Context, docID string, version uint64) ([]collab.AppliedOperation, error)
	GetAppliedOperation(ctx context.Context, docID, requestID string) (collab.AppliedOperation, error)
	GetLatestSnapshot(ctx context.Context, docID string) (Snapshot, error)
	EnsureBaselineSnapshot(ctx context.Context, docID string, authority WriteAuthority) (Snapshot, error)
	SaveSnapshot(ctx context.Context, commit SnapshotCommit) error
	Close() error
}
