package cluster

import (
	"context"
	"errors"
	"sync"

	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/storage"
)

type OwnerState string

const (
	OwnerAcquiring  OwnerState = "ACQUIRING"
	OwnerRecovering OwnerState = "RECOVERING"
	OwnerActive     OwnerState = "ACTIVE"
	OwnerDraining   OwnerState = "DRAINING"
)

var (
	ErrNotPreferredNode = errors.New("cluster: local node is not the preferred owner candidate")
	ErrOwnerExists      = errors.New("cluster: document already has an owner")
	ErrOwnershipLost    = errors.New("cluster: document ownership was lost")
	ErrOwnerNotFound    = errors.New("cluster: document owner not found")
)

type Owner struct {
	DocumentID   string     `json:"-"`
	NodeID       string     `json:"node_id"`
	Incarnation  string     `json:"incarnation"`
	State        OwnerState `json:"state"`
	QUICEndpoint string     `json:"quic_endpoint"`
}

// OwnershipHandle binds a Room lifecycle to an etcd owner key lifecycle.
type OwnershipHandle struct {
	DocumentID   string
	NodeID       string
	Incarnation  string
	FencingToken uint64
	Context      context.Context

	mu     sync.Mutex
	cancel context.CancelCauseFunc
}

func NewOwnershipHandle(parent context.Context, documentID string, node Node, token uint64) *OwnershipHandle {
	ctx, cancel := context.WithCancelCause(parent)
	return &OwnershipHandle{
		DocumentID:   documentID,
		NodeID:       node.NodeID,
		Incarnation:  node.Incarnation,
		FencingToken: token,
		Context:      ctx,
		cancel:       cancel,
	}
}

func (handle *OwnershipHandle) Authority() storage.WriteAuthority {
	return storage.WriteAuthority{NodeID: handle.NodeID, FencingToken: handle.FencingToken}
}

func (handle *OwnershipHandle) Cancel(cause error) {
	handle.mu.Lock()
	defer handle.mu.Unlock()
	if handle.cancel != nil {
		handle.cancel(cause)
		handle.cancel = nil
	}
}

type OwnershipCoordinator interface {
	Acquire(ctx context.Context, documentID string) (*OwnershipHandle, error)
	BeginRecovery(ctx context.Context, handle *OwnershipHandle) error
	Activate(ctx context.Context, handle *OwnershipHandle) error
	BeginDraining(ctx context.Context, handle *OwnershipHandle) error
	Release(ctx context.Context, handle *OwnershipHandle) error
}

type OwnerResolver interface {
	Resolve(ctx context.Context, documentID string) (Owner, error)
	EnsureOwner(ctx context.Context, documentID string) (Owner, error)
}

type OwnershipService interface {
	OwnershipCoordinator
	OwnerResolver
}

// FailoverSource reports documents for which the local node became the preferred
// candidate. The event only triggers an attempt; etcd and MySQL fencing still
// decide ownership.
type FailoverSource interface {
	FailoverCandidates() <-chan string
}
