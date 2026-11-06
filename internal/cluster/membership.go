package cluster

import (
	"sort"
	"sync"
)

type MembershipEventType string

const (
	NodeJoined  MembershipEventType = "joined"
	NodeUpdated MembershipEventType = "updated"
	NodeLeft    MembershipEventType = "left"
)

type MembershipEvent struct {
	Type     MembershipEventType
	Node     Node
	Revision int64
}

// Membership is the local read-only view built from the etcd watch. Revisions
// prevent stale events from overwriting newer state.
type Membership struct {
	mu          sync.RWMutex
	nodes       map[string]Node
	revision    int64
	subscribers map[uint64]chan MembershipEvent
	nextID      uint64
}

func NewMembership() *Membership {
	return &Membership{
		nodes:       make(map[string]Node),
		subscribers: make(map[uint64]chan MembershipEvent),
	}
}

func (membership *Membership) Snapshot() ([]Node, int64) {
	membership.mu.RLock()
	defer membership.mu.RUnlock()

	nodes := make([]Node, 0, len(membership.nodes))
	for _, node := range membership.nodes {
		nodes = append(nodes, node)
	}
	sortNodes(nodes)
	return nodes, membership.revision
}

func (membership *Membership) Lookup(nodeID, incarnation string) (Node, bool) {
	membership.mu.RLock()
	defer membership.mu.RUnlock()
	node, exists := membership.nodes[nodeID+"/"+incarnation]
	return node, exists
}

func (membership *Membership) Replace(nodes []Node, revision int64) {
	membership.mu.Lock()
	defer membership.mu.Unlock()
	if revision < membership.revision {
		return
	}

	next := make(map[string]Node, len(nodes))
	for _, node := range nodes {
		next[node.Identity()] = node
	}
	for identity, previous := range membership.nodes {
		if _, exists := next[identity]; !exists {
			membership.publishLocked(MembershipEvent{Type: NodeLeft, Node: previous, Revision: revision})
		}
	}
	for identity, node := range next {
		previous, exists := membership.nodes[identity]
		eventType := NodeJoined
		if exists {
			if previous == node {
				continue
			}
			eventType = NodeUpdated
		}
		membership.publishLocked(MembershipEvent{Type: eventType, Node: node, Revision: revision})
	}
	membership.nodes = next
	membership.revision = revision
}

func (membership *Membership) Put(node Node, revision int64) {
	membership.mu.Lock()
	defer membership.mu.Unlock()
	if revision < membership.revision {
		return
	}
	previous, exists := membership.nodes[node.Identity()]
	membership.nodes[node.Identity()] = node
	membership.revision = revision
	if exists && previous == node {
		return
	}
	eventType := NodeJoined
	if exists {
		eventType = NodeUpdated
	}
	membership.publishLocked(MembershipEvent{Type: eventType, Node: node, Revision: revision})
}

func (membership *Membership) Delete(identity string, revision int64) {
	membership.mu.Lock()
	defer membership.mu.Unlock()
	if revision < membership.revision {
		return
	}
	node, exists := membership.nodes[identity]
	delete(membership.nodes, identity)
	membership.revision = revision
	if exists {
		membership.publishLocked(MembershipEvent{Type: NodeLeft, Node: node, Revision: revision})
	}
}

func (membership *Membership) Subscribe(buffer int) (<-chan MembershipEvent, func()) {
	if buffer < 1 {
		buffer = 1
	}
	membership.mu.Lock()
	id := membership.nextID
	membership.nextID++
	updates := make(chan MembershipEvent, buffer)
	membership.subscribers[id] = updates
	membership.mu.Unlock()

	var once sync.Once
	cancel := func() {
		once.Do(func() {
			membership.mu.Lock()
			delete(membership.subscribers, id)
			membership.mu.Unlock()
		})
	}
	return updates, cancel
}

func (membership *Membership) publishLocked(event MembershipEvent) {
	for _, subscriber := range membership.subscribers {
		select {
		case subscriber <- event:
		default:
			// The membership view remains the authoritative local snapshot; a slow
			// subscriber can reload it instead of blocking the watch.
		}
	}
}

func sortNodes(nodes []Node) {
	sort.Slice(nodes, func(i, j int) bool {
		if nodes[i].NodeID != nodes[j].NodeID {
			return nodes[i].NodeID < nodes[j].NodeID
		}
		return nodes[i].Incarnation < nodes[j].Incarnation
	})
}
