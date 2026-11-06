package cluster

import (
	"context"
	"fmt"

	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/collab"
	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/permission"
)

type NodeDirectory interface {
	Lookup(nodeID, incarnation string) (Node, bool)
}

type PermissionForwarder interface {
	ForwardPermission(ctx context.Context, owner Owner, authorization, documentID, userID string, role permission.Role) error
	ForwardDocument(ctx context.Context, owner Owner, authorization, documentID string) (collab.Document, error)
	ForwardGetPermission(ctx context.Context, owner Owner, authorization, documentID, userID string) (permission.Role, error)
}

type RemoteError struct {
	Status  int
	Code    string
	Message string
}

func (err *RemoteError) Error() string {
	return fmt.Sprintf("remote permission request failed (%s): %s", err.Code, err.Message)
}
