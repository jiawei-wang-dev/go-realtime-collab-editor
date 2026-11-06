package etcd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/cluster"
	"go.etcd.io/etcd/api/v3/mvccpb"
	"go.etcd.io/etcd/api/v3/v3rpc/rpctypes"
	clientv3 "go.etcd.io/etcd/client/v3"
)

const nodePrefix = "/collab/v1/nodes/"

var ErrLeaseLost = errors.New("etcd node lease lost")

type Registry struct {
	client     *Client
	node       cluster.Node
	leaseTTL   int64
	membership *cluster.Membership

	mu        sync.Mutex
	leaseID   clientv3.LeaseID
	cancel    context.CancelFunc
	done      chan struct{}
	lost      chan error
	started   bool
	closeOnce sync.Once
	ctx       context.Context
	observer  Observer
}

type Observer interface {
	LeaseRenewalFailed()
	OwnershipAttempt(result string)
}

func (registry *Registry) SetObserver(observer Observer) {
	registry.mu.Lock()
	registry.observer = observer
	registry.mu.Unlock()
}

func (registry *Registry) observerValue() Observer {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	return registry.observer
}

func NewRegistry(client *Client, node cluster.Node, leaseTTL time.Duration) (*Registry, error) {
	if client == nil || client.client == nil {
		return nil, errors.New("etcd client is required")
	}
	if err := node.Validate(); err != nil {
		return nil, fmt.Errorf("validate cluster node: %w", err)
	}
	if node.Status != cluster.NodeStarting {
		return nil, errors.New("new cluster node must have STARTING status")
	}
	seconds := int64(leaseTTL / time.Second)
	if seconds < 2 {
		return nil, errors.New("etcd lease TTL must be at least two seconds")
	}
	return &Registry{
		client:     client,
		node:       node,
		leaseTTL:   seconds,
		membership: cluster.NewMembership(),
		done:       make(chan struct{}),
		lost:       make(chan error, 1),
	}, nil
}

func (registry *Registry) Start(ctx context.Context) error {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	if registry.started {
		return errors.New("etcd registry already started")
	}
	requestCtx, cancel := registry.client.requestContext(ctx)
	lease, err := registry.client.client.Grant(requestCtx, registry.leaseTTL)
	cancel()
	if err != nil {
		return fmt.Errorf("grant node lease: %w", err)
	}
	registry.leaseID = lease.ID
	if _, err := registry.putNode(ctx, registry.node); err != nil {
		registry.revokeLeaseID(context.Background(), lease.ID)
		return err
	}
	nodes, revision, err := registry.loadMembership(ctx)
	if err != nil {
		registry.revokeLeaseID(context.Background(), lease.ID)
		return err
	}
	registry.membership.Replace(nodes, revision)

	runCtx, runCancel := context.WithCancel(ctx)
	keepAlive, err := registry.client.client.KeepAlive(runCtx, registry.leaseID)
	if err != nil {
		runCancel()
		registry.revokeLeaseID(context.Background(), lease.ID)
		return fmt.Errorf("start node lease keepalive: %w", err)
	}
	registry.cancel = runCancel
	registry.ctx = runCtx
	registry.started = true
	go registry.run(runCtx, keepAlive, revision+1)
	slog.Info("cluster node registered", "component", "cluster", "event", "node_registered", "node_id", registry.node.NodeID, "incarnation", registry.node.Incarnation)
	return nil
}

func (registry *Registry) ownershipIdentity() (cluster.Node, clientv3.LeaseID, context.Context, error) {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	if !registry.started || registry.ctx == nil || registry.ctx.Err() != nil || registry.node.Status != cluster.NodeReady {
		return cluster.Node{}, 0, nil, errors.New("local cluster node is not ready")
	}
	return registry.node, registry.leaseID, registry.ctx, nil
}

func (registry *Registry) runningContext() (context.Context, error) {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	if !registry.started || registry.ctx == nil || registry.ctx.Err() != nil {
		return nil, errors.New("etcd registry is not running")
	}
	return registry.ctx, nil
}

func (registry *Registry) leaseIDValue() clientv3.LeaseID {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	return registry.leaseID
}

func (registry *Registry) MarkReady(ctx context.Context) error {
	return registry.setStatus(ctx, cluster.NodeReady)
}

func (registry *Registry) Ready() bool {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	return registry.started && registry.ctx != nil && registry.ctx.Err() == nil && registry.node.Status == cluster.NodeReady
}

func (registry *Registry) MarkDraining(ctx context.Context) error {
	return registry.setStatus(ctx, cluster.NodeDraining)
}

func (registry *Registry) setStatus(ctx context.Context, status cluster.NodeStatus) error {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	if !registry.started || registry.cancel == nil {
		return errors.New("etcd registry is not running")
	}
	node := registry.node
	node.Status = status
	revision, err := registry.putNode(ctx, node)
	if err != nil {
		return err
	}
	registry.node = node
	registry.membership.Put(node, revision)
	return nil
}

func (registry *Registry) Node() cluster.Node {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	return registry.node
}

func (registry *Registry) Membership() *cluster.Membership {
	return registry.membership
}

func (registry *Registry) LeaseLost() <-chan error {
	return registry.lost
}

func (registry *Registry) Close(ctx context.Context) error {
	var closeErr error
	registry.closeOnce.Do(func() {
		registry.mu.Lock()
		if registry.cancel != nil {
			registry.cancel()
		}
		started := registry.started
		registry.started = false
		registry.mu.Unlock()
		if started {
			closeErr = registry.revokeLease(ctx)
			<-registry.done
		}
	})
	return closeErr
}

func (registry *Registry) run(ctx context.Context, keepAlive <-chan *clientv3.LeaseKeepAliveResponse, revision int64) {
	defer close(registry.done)
	watchDone := make(chan struct{})
	go func() {
		defer close(watchDone)
		registry.watchMembership(ctx, revision)
	}()

	confirmationTimer := time.NewTimer(leaseConfirmationWindow(registry.leaseTTL))
	defer confirmationTimer.Stop()
	for {
		select {
		case <-ctx.Done():
			<-watchDone
			return
		case response, open := <-keepAlive:
			if !open || response == nil || response.TTL <= 0 {
				registry.reportLeaseLost()
				registry.cancelRun()
				<-watchDone
				return
			}
			resetTimer(confirmationTimer, leaseConfirmationWindow(response.TTL))
		case <-confirmationTimer.C:
			// A KeepAlive TTL is the last evidence that the lease remains valid. Fail
			// closed when the monotonic confirmation window expires instead of waiting
			// for the network library to close the channel.
			registry.reportLeaseLost()
			registry.cancelRun()
			<-watchDone
			return
		}
	}
}

func leaseConfirmationWindow(ttlSeconds int64) time.Duration {
	ttl := time.Duration(ttlSeconds) * time.Second
	margin := ttl / 3
	if margin < 250*time.Millisecond {
		margin = 250 * time.Millisecond
	}
	window := ttl - margin
	if window < 250*time.Millisecond {
		return 250 * time.Millisecond
	}
	return window
}

func resetTimer(timer *time.Timer, duration time.Duration) {
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
	timer.Reset(duration)
}

func (registry *Registry) watchMembership(ctx context.Context, revision int64) {
	for ctx.Err() == nil {
		watch := registry.client.client.Watch(ctx, nodePrefix, clientv3.WithPrefix(), clientv3.WithRev(revision), clientv3.WithPrevKV())
		reload := false
		for response := range watch {
			if response.Canceled {
				if response.CompactRevision > 0 || errors.Is(response.Err(), rpctypes.ErrCompacted) {
					reload = true
				}
				break
			}
			for _, event := range response.Events {
				registry.applyWatchEvent(event, response.Header.Revision)
			}
			revision = response.Header.Revision + 1
		}
		if ctx.Err() != nil {
			return
		}
		if reload || revision > 0 {
			nodes, loadedRevision, err := registry.loadMembership(ctx)
			if err == nil {
				registry.membership.Replace(nodes, loadedRevision)
				revision = loadedRevision + 1
				continue
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func (registry *Registry) applyWatchEvent(event *clientv3.Event, revision int64) {
	switch event.Type {
	case mvccpb.PUT:
		node, err := decodeNode(event.Kv.Value)
		if err == nil {
			registry.membership.Put(node, revision)
		}
	case mvccpb.DELETE:
		identity, ok := identityFromKey(string(event.Kv.Key))
		if ok {
			registry.membership.Delete(identity, revision)
		}
	}
}

func (registry *Registry) loadMembership(ctx context.Context) ([]cluster.Node, int64, error) {
	requestCtx, cancel := registry.client.requestContext(ctx)
	defer cancel()
	response, err := registry.client.client.Get(requestCtx, nodePrefix, clientv3.WithPrefix())
	if err != nil {
		return nil, 0, fmt.Errorf("load cluster membership: %w", err)
	}
	nodes := make([]cluster.Node, 0, len(response.Kvs))
	for _, keyValue := range response.Kvs {
		node, err := decodeNode(keyValue.Value)
		if err != nil {
			continue
		}
		nodes = append(nodes, node)
	}
	return nodes, response.Header.Revision, nil
}

func (registry *Registry) putNode(ctx context.Context, node cluster.Node) (int64, error) {
	payload, err := json.Marshal(node)
	if err != nil {
		return 0, fmt.Errorf("encode cluster node: %w", err)
	}
	requestCtx, cancel := registry.client.requestContext(ctx)
	defer cancel()
	response, err := registry.client.client.Put(requestCtx, nodeKey(node), string(payload), clientv3.WithLease(registry.leaseID))
	if err != nil {
		return 0, fmt.Errorf("register cluster node: %w", err)
	}
	return response.Header.Revision, nil
}

func (registry *Registry) revokeLease(ctx context.Context) error {
	registry.mu.Lock()
	leaseID := registry.leaseID
	registry.mu.Unlock()
	return registry.revokeLeaseID(ctx, leaseID)
}

func (registry *Registry) revokeLeaseID(ctx context.Context, leaseID clientv3.LeaseID) error {
	if leaseID == 0 {
		return nil
	}
	requestCtx, cancel := registry.client.requestContext(ctx)
	defer cancel()
	_, err := registry.client.client.Revoke(requestCtx, leaseID)
	if err != nil && !errors.Is(err, rpctypes.ErrLeaseNotFound) {
		return fmt.Errorf("revoke node lease: %w", err)
	}
	return nil
}

func (registry *Registry) cancelRun() {
	registry.mu.Lock()
	cancel := registry.cancel
	registry.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (registry *Registry) reportLeaseLost() {
	registry.mu.Lock()
	observer := registry.observer
	node := registry.node
	registry.mu.Unlock()
	if observer != nil {
		observer.LeaseRenewalFailed()
	}
	slog.Error("cluster lease lost", "component", "cluster", "event", "lease_lost", "node_id", node.NodeID, "incarnation", node.Incarnation)
	select {
	case registry.lost <- ErrLeaseLost:
	default:
	}
}

func nodeKey(node cluster.Node) string {
	return nodePrefix + node.NodeID + "/" + node.Incarnation
}

func identityFromKey(key string) (string, bool) {
	if !strings.HasPrefix(key, nodePrefix) {
		return "", false
	}
	parts := strings.Split(strings.TrimPrefix(key, nodePrefix), "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", false
	}
	return parts[0] + "/" + parts[1], true
}

func decodeNode(payload []byte) (cluster.Node, error) {
	var node cluster.Node
	if err := json.Unmarshal(payload, &node); err != nil {
		return cluster.Node{}, err
	}
	if err := node.Validate(); err != nil {
		return cluster.Node{}, err
	}
	return node, nil
}
