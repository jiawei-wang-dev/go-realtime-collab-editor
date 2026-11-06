package transport

import "github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/protocol"

// Client is the transport-independent connection used by a Room. TrySend must
// be nonblocking, and Close must be safe to call more than once.
type Client interface {
	Identity() string
	TrySend(protocol.Message) bool
	Close(error)
}
