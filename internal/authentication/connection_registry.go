package authentication

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// ConnectionRegistry tracks live connections by session and coordinates their
// Redis presence heartbeat. Closing a session closes every local connection.
type ConnectionRegistry struct {
	mu          sync.Mutex
	connections map[string]map[any]func()
	presence    map[string]*presenceHeartbeat
	ctx         context.Context
	store       DistributedSessionStore
	nodeID      string
	presenceTTL time.Duration
	observer    Observer
}

type presenceHeartbeat struct {
	cancel context.CancelFunc
	done   chan struct{}
}

func NewConnectionRegistry() *ConnectionRegistry {
	return newCoordinatedConnectionRegistry(context.Background(), nil, "", 0)
}

func newCoordinatedConnectionRegistry(ctx context.Context, store DistributedSessionStore, nodeID string, presenceTTL time.Duration, observers ...Observer) *ConnectionRegistry {
	var observer Observer
	if len(observers) > 0 {
		observer = observers[0]
	}
	return &ConnectionRegistry{
		connections: map[string]map[any]func(){},
		presence:    map[string]*presenceHeartbeat{},
		ctx:         ctx,
		store:       store,
		nodeID:      nodeID,
		presenceTTL: presenceTTL,
		observer:    observer,
	}
}

func (registry *ConnectionRegistry) Register(sessionID string, key any, closeConnection func()) func() {
	registry.mu.Lock()
	firstConnection := len(registry.connections[sessionID]) == 0
	if registry.connections[sessionID] == nil {
		registry.connections[sessionID] = map[any]func(){}
	}
	registry.connections[sessionID][key] = closeConnection
	if firstConnection {
		registry.startPresenceLocked(sessionID)
	}
	registry.mu.Unlock()

	return func() {
		registry.mu.Lock()
		delete(registry.connections[sessionID], key)
		var heartbeat *presenceHeartbeat
		if len(registry.connections[sessionID]) == 0 {
			delete(registry.connections, sessionID)
			heartbeat = registry.stopPresenceLocked(sessionID)
		}
		registry.mu.Unlock()
		waitForPresence(heartbeat)
	}
}

func (registry *ConnectionRegistry) CloseSession(sessionID string) {
	registry.mu.Lock()
	connections := registry.connections[sessionID]
	delete(registry.connections, sessionID)
	heartbeat := registry.stopPresenceLocked(sessionID)
	registry.mu.Unlock()

	for _, closeConnection := range connections {
		closeConnection()
	}
	waitForPresence(heartbeat)
}

func (registry *ConnectionRegistry) SessionIDs() []string {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	sessionIDs := make([]string, 0, len(registry.connections))
	for sessionID := range registry.connections {
		sessionIDs = append(sessionIDs, sessionID)
	}
	return sessionIDs
}

func (registry *ConnectionRegistry) Close() {
	for _, sessionID := range registry.SessionIDs() {
		registry.CloseSession(sessionID)
	}
}

// startPresenceLocked starts one heartbeat for the first connection in a
// session. The caller must hold registry.mu.
func (registry *ConnectionRegistry) startPresenceLocked(sessionID string) {
	if registry.store == nil || registry.nodeID == "" || registry.presenceTTL <= 0 || registry.presence[sessionID] != nil {
		return
	}
	ctx, cancel := context.WithCancel(registry.ctx)
	heartbeat := &presenceHeartbeat{cancel: cancel, done: make(chan struct{})}
	registry.presence[sessionID] = heartbeat
	go registry.runPresenceHeartbeat(ctx, sessionID, heartbeat)
}

func (registry *ConnectionRegistry) runPresenceHeartbeat(ctx context.Context, sessionID string, heartbeat *presenceHeartbeat) {
	defer close(heartbeat.done)
	interval := registry.presenceTTL / 3
	if interval < time.Second {
		interval = time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		touchCtx, cancelTouch := context.WithTimeout(ctx, interval)
		started := time.Now()
		err := registry.store.TouchPresence(touchCtx, sessionID, registry.nodeID, registry.presenceTTL)
		if registry.observer != nil {
			registry.observer.RedisOperation("presence_touch", time.Since(started), err)
		}
		if err == nil {
			slog.Debug("presence updated", "component", "authentication", "event", "presence_updated")
		}
		cancelTouch()

		select {
		case <-ctx.Done():
			deleteCtx, cancelDelete := context.WithTimeout(context.Background(), 2*time.Second)
			_ = registry.store.DeletePresence(deleteCtx, sessionID, registry.nodeID)
			cancelDelete()
			return
		case <-ticker.C:
		}
	}
}

// stopPresenceLocked detaches and cancels the session heartbeat. The caller
// must hold registry.mu and wait only after releasing it.
func (registry *ConnectionRegistry) stopPresenceLocked(sessionID string) *presenceHeartbeat {
	heartbeat := registry.presence[sessionID]
	delete(registry.presence, sessionID)
	if heartbeat != nil {
		heartbeat.cancel()
	}
	return heartbeat
}

func waitForPresence(heartbeat *presenceHeartbeat) {
	if heartbeat != nil {
		<-heartbeat.done
	}
}
