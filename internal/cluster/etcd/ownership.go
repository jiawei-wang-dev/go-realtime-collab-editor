package etcd

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/cluster"
	clientv3 "go.etcd.io/etcd/client/v3"
)

const ownerPrefix = "/collab/v1/owners/"

type Ownership struct {
	registry *Registry

	mu         sync.Mutex
	owners     map[*cluster.OwnershipHandle]ownerRecord
	candidates chan string
	pending    map[string]struct{}
}

type ownerRecord struct {
	owner       cluster.Owner
	modRevision int64
	key         string
	cancelWatch context.CancelFunc
}

func NewOwnership(registry *Registry) (*Ownership, error) {
	if registry == nil {
		return nil, errors.New("etcd registry is required")
	}
	ownership := &Ownership{
		registry: registry, owners: make(map[*cluster.OwnershipHandle]ownerRecord),
		candidates: make(chan string, 64), pending: make(map[string]struct{}),
	}
	ctx, err := registry.runningContext()
	if err != nil {
		return nil, err
	}
	go ownership.monitorFailures(ctx)
	return ownership, nil
}

func (ownership *Ownership) FailoverCandidates() <-chan string { return ownership.candidates }

func (ownership *Ownership) Resolve(ctx context.Context, documentID string) (cluster.Owner, error) {
	if documentID == "" {
		return cluster.Owner{}, errors.New("document ID is required")
	}
	requestCtx, cancel := ownership.registry.client.requestContext(ctx)
	defer cancel()
	response, err := ownership.registry.client.client.Get(requestCtx, ownerKey(documentID))
	if err != nil {
		return cluster.Owner{}, fmt.Errorf("resolve document owner: %w", err)
	}
	if len(response.Kvs) == 0 {
		return cluster.Owner{}, cluster.ErrOwnerNotFound
	}
	var owner cluster.Owner
	if err := json.Unmarshal(response.Kvs[0].Value, &owner); err != nil {
		return cluster.Owner{}, fmt.Errorf("decode document owner: %w", err)
	}
	owner.DocumentID = documentID
	return owner, nil
}

func (ownership *Ownership) EnsureOwner(ctx context.Context, documentID string) (cluster.Owner, error) {
	owner, err := ownership.Resolve(ctx, documentID)
	if err == nil {
		return owner, nil
	}
	if !errors.Is(err, cluster.ErrOwnerNotFound) {
		return cluster.Owner{}, err
	}
	nodes, _ := ownership.registry.membership.Snapshot()
	candidates := cluster.RankNodes(documentID, nodes)
	if len(candidates) == 0 {
		return cluster.Owner{}, cluster.ErrOwnerNotFound
	}
	candidate := candidates[0]
	return cluster.Owner{
		DocumentID: documentID, NodeID: candidate.NodeID, Incarnation: candidate.Incarnation,
		State: cluster.OwnerAcquiring, QUICEndpoint: candidate.QUICEndpoint,
	}, nil
}

func (ownership *Ownership) Acquire(ctx context.Context, documentID string) (*cluster.OwnershipHandle, error) {
	if documentID == "" {
		return nil, errors.New("document ID is required")
	}
	localNode, leaseID, registryCtx, err := ownership.registry.ownershipIdentity()
	if err != nil {
		return nil, err
	}
	nodes, _ := ownership.registry.membership.Snapshot()
	candidates := cluster.RankNodes(documentID, nodes)
	if len(candidates) == 0 || candidates[0].Identity() != localNode.Identity() {
		return nil, cluster.ErrNotPreferredNode
	}

	key := ownerKey(documentID)
	owner := cluster.Owner{
		DocumentID:   documentID,
		NodeID:       localNode.NodeID,
		Incarnation:  localNode.Incarnation,
		State:        cluster.OwnerAcquiring,
		QUICEndpoint: localNode.QUICEndpoint,
	}
	payload, err := json.Marshal(owner)
	if err != nil {
		return nil, err
	}
	nodePayload, err := json.Marshal(localNode)
	if err != nil {
		return nil, err
	}
	requestCtx, cancel := ownership.registry.client.requestContext(ctx)
	response, err := ownership.registry.client.client.Txn(requestCtx).
		If(
			clientv3.Compare(clientv3.Version(key), "=", 0),
			clientv3.Compare(clientv3.Value(nodeKey(localNode)), "=", string(nodePayload)),
		).
		Then(clientv3.OpPut(key, string(payload), clientv3.WithLease(leaseID))).
		Commit()
	cancel()
	if err != nil {
		ownership.observeAttempt("failure")
		return nil, fmt.Errorf("acquire document ownership: %w", err)
	}
	if !response.Succeeded {
		ownership.observeAttempt("conflict")
		return nil, cluster.ErrOwnerExists
	}

	requestCtx, cancel = ownership.registry.client.requestContext(ctx)
	getResponse, err := ownership.registry.client.client.Get(requestCtx, key)
	cancel()
	if err != nil {
		return nil, ownership.cleanupFailedAcquire(ctx, key, owner, fmt.Errorf("read acquired ownership: %w", err))
	}
	if len(getResponse.Kvs) != 1 || getResponse.Kvs[0].CreateRevision <= 0 {
		return nil, ownership.cleanupFailedAcquire(ctx, key, owner, cluster.ErrOwnershipLost)
	}
	keyValue := getResponse.Kvs[0]
	handle := cluster.NewOwnershipHandle(registryCtx, documentID, localNode, uint64(keyValue.CreateRevision))
	watchCtx, cancelWatch := context.WithCancel(registryCtx)
	record := ownerRecord{owner: owner, modRevision: keyValue.ModRevision, key: key, cancelWatch: cancelWatch}
	ownership.mu.Lock()
	ownership.owners[handle] = record
	ownership.mu.Unlock()
	go ownership.watch(watchCtx, handle, key, keyValue.ModRevision+1)
	ownership.observeAttempt("success")
	slog.Info("document ownership acquired", "component", "ownership", "event", "ownership_acquired", "doc_id", documentID, "fencing_token", handle.FencingToken)
	return handle, nil
}

func (ownership *Ownership) BeginRecovery(ctx context.Context, handle *cluster.OwnershipHandle) error {
	return ownership.updateState(ctx, handle, cluster.OwnerRecovering)
}

func (ownership *Ownership) Activate(ctx context.Context, handle *cluster.OwnershipHandle) error {
	return ownership.updateState(ctx, handle, cluster.OwnerActive)
}

func (ownership *Ownership) BeginDraining(ctx context.Context, handle *cluster.OwnershipHandle) error {
	return ownership.updateState(ctx, handle, cluster.OwnerDraining)
}

func (ownership *Ownership) Release(ctx context.Context, handle *cluster.OwnershipHandle) error {
	record, ok := ownership.record(handle)
	if !ok {
		return cluster.ErrOwnershipLost
	}
	payload, err := json.Marshal(record.owner)
	if err != nil {
		return err
	}
	requestCtx, cancel := ownership.registry.client.requestContext(ctx)
	response, err := ownership.registry.client.client.Txn(requestCtx).
		If(
			clientv3.Compare(clientv3.ModRevision(record.key), "=", record.modRevision),
			clientv3.Compare(clientv3.Value(record.key), "=", string(payload)),
		).
		Then(clientv3.OpDelete(record.key)).
		Commit()
	cancel()
	if err != nil {
		return fmt.Errorf("release document ownership: %w", err)
	}
	if !response.Succeeded {
		handle.Cancel(cluster.ErrOwnershipLost)
		return cluster.ErrOwnershipLost
	}
	ownership.forget(handle, nil)
	slog.Info("document ownership released", "component", "ownership", "event", "ownership_released", "doc_id", handle.DocumentID, "fencing_token", handle.FencingToken)
	return nil
}

func (ownership *Ownership) updateState(ctx context.Context, handle *cluster.OwnershipHandle, state cluster.OwnerState) error {
	record, ok := ownership.record(handle)
	if !ok || handle.Context.Err() != nil {
		return cluster.ErrOwnershipLost
	}
	previousPayload, err := json.Marshal(record.owner)
	if err != nil {
		return err
	}
	next := record.owner
	next.State = state
	nextPayload, err := json.Marshal(next)
	if err != nil {
		return err
	}
	requestCtx, cancel := ownership.registry.client.requestContext(ctx)
	response, err := ownership.registry.client.client.Txn(requestCtx).
		If(
			clientv3.Compare(clientv3.ModRevision(record.key), "=", record.modRevision),
			clientv3.Compare(clientv3.Value(record.key), "=", string(previousPayload)),
		).
		Then(clientv3.OpPut(record.key, string(nextPayload), clientv3.WithLease(ownership.registry.leaseIDValue()))).
		Commit()
	cancel()
	if err != nil {
		return fmt.Errorf("update document ownership state: %w", err)
	}
	if !response.Succeeded {
		ownership.forget(handle, cluster.ErrOwnershipLost)
		return cluster.ErrOwnershipLost
	}
	record.owner = next
	record.modRevision = response.Header.Revision
	ownership.mu.Lock()
	if _, exists := ownership.owners[handle]; exists {
		ownership.owners[handle] = record
	}
	ownership.mu.Unlock()
	return nil
}

func (ownership *Ownership) watch(ctx context.Context, handle *cluster.OwnershipHandle, key string, revision int64) {
	for ctx.Err() == nil {
		watch := ownership.registry.client.client.Watch(ctx, key, clientv3.WithRev(revision))
		for response := range watch {
			if response.Canceled {
				break
			}
			for _, event := range response.Events {
				if event.Type == clientv3.EventTypeDelete || !ownerEventMatches(event, handle) {
					ownership.forget(handle, cluster.ErrOwnershipLost)
					return
				}
				revision = event.Kv.ModRevision + 1
			}
		}
		if ctx.Err() != nil {
			return
		}
		currentRevision, valid, err := ownership.verifyHandle(ctx, key, handle)
		if err == nil && !valid {
			ownership.forget(handle, cluster.ErrOwnershipLost)
			return
		}
		if err == nil {
			revision = currentRevision + 1
		}
		// A broken watch does not prove lease loss. Reload the key and rebuild the
		// watch; if etcd remains unavailable, the Registry confirmation timer will
		// eventually cancel ctx and self-fence this node.
		select {
		case <-ctx.Done():
			return
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func ownerEventMatches(event *clientv3.Event, handle *cluster.OwnershipHandle) bool {
	var current cluster.Owner
	return json.Unmarshal(event.Kv.Value, &current) == nil &&
		current.NodeID == handle.NodeID && current.Incarnation == handle.Incarnation &&
		uint64(event.Kv.CreateRevision) == handle.FencingToken
}

func (ownership *Ownership) verifyHandle(ctx context.Context, key string, handle *cluster.OwnershipHandle) (int64, bool, error) {
	requestCtx, cancel := ownership.registry.client.requestContext(ctx)
	defer cancel()
	response, err := ownership.registry.client.client.Get(requestCtx, key)
	if err != nil {
		return 0, false, err
	}
	if len(response.Kvs) != 1 {
		return response.Header.Revision, false, nil
	}
	keyValue := response.Kvs[0]
	var current cluster.Owner
	valid := json.Unmarshal(keyValue.Value, &current) == nil &&
		current.NodeID == handle.NodeID && current.Incarnation == handle.Incarnation &&
		uint64(keyValue.CreateRevision) == handle.FencingToken
	return response.Header.Revision, valid, nil
}

func (ownership *Ownership) monitorFailures(ctx context.Context) {
	defer close(ownership.candidates)
	updates, unsubscribe := ownership.registry.membership.Subscribe(64)
	defer unsubscribe()
	known, revision := ownership.loadOwners(ctx)
	if known == nil {
		known = make(map[string]cluster.Owner)
	}
	reconcile := time.NewTicker(time.Second)
	defer reconcile.Stop()
	for ctx.Err() == nil {
		watch := ownership.registry.client.client.Watch(ctx, ownerPrefix, clientv3.WithPrefix(), clientv3.WithPrevKV(), clientv3.WithRev(revision+1))
		watchEnded := false
		for !watchEnded {
			select {
			case <-ctx.Done():
				return
			case <-updates:
				ownership.reconsiderPending(ctx)
			case <-reconcile.C:
				ownership.reconsiderPending(ctx)
			case response, open := <-watch:
				if !open || response.Canceled {
					watchEnded = true
					continue
				}
				revision = response.Header.Revision
				for _, event := range response.Events {
					documentID, ok := documentIDFromOwnerKey(string(event.Kv.Key))
					if !ok {
						continue
					}
					if event.Type == clientv3.EventTypeDelete {
						previous := known[documentID]
						delete(known, documentID)
						if ownerNeedsFailover(previous) {
							ownership.queueCandidate(ctx, documentID)
						}
						continue
					}
					var owner cluster.Owner
					if json.Unmarshal(event.Kv.Value, &owner) == nil {
						owner.DocumentID = documentID
						known[documentID] = owner
						if owner.State == cluster.OwnerActive {
							ownership.removePending(documentID)
						}
					}
				}
			}
		}
		if ctx.Err() != nil {
			return
		}
		current, loadedRevision := ownership.loadOwners(ctx)
		if loadedRevision > 0 {
			for documentID, previous := range known {
				if _, exists := current[documentID]; !exists && ownerNeedsFailover(previous) {
					ownership.queueCandidate(ctx, documentID)
				}
			}
			known, revision = current, loadedRevision
		}
	}
}

func ownerNeedsFailover(owner cluster.Owner) bool {
	return owner.State == cluster.OwnerActive || owner.State == cluster.OwnerRecovering
}

func (ownership *Ownership) loadOwners(ctx context.Context) (map[string]cluster.Owner, int64) {
	requestCtx, cancel := ownership.registry.client.requestContext(ctx)
	defer cancel()
	response, err := ownership.registry.client.client.Get(requestCtx, ownerPrefix, clientv3.WithPrefix())
	if err != nil {
		return nil, 0
	}
	owners := make(map[string]cluster.Owner, len(response.Kvs))
	for _, keyValue := range response.Kvs {
		documentID, ok := documentIDFromOwnerKey(string(keyValue.Key))
		if !ok {
			continue
		}
		var owner cluster.Owner
		if json.Unmarshal(keyValue.Value, &owner) == nil {
			owner.DocumentID = documentID
			owners[documentID] = owner
		}
	}
	return owners, response.Header.Revision
}

func (ownership *Ownership) queueCandidate(ctx context.Context, documentID string) {
	ownership.mu.Lock()
	ownership.pending[documentID] = struct{}{}
	ownership.mu.Unlock()
	ownership.considerCandidate(ctx, documentID)
}

func (ownership *Ownership) reconsiderPending(ctx context.Context) {
	ownership.mu.Lock()
	documents := make([]string, 0, len(ownership.pending))
	for documentID := range ownership.pending {
		documents = append(documents, documentID)
	}
	ownership.mu.Unlock()
	for _, documentID := range documents {
		ownership.considerCandidate(ctx, documentID)
	}
}

func (ownership *Ownership) considerCandidate(ctx context.Context, documentID string) {
	if owner, err := ownership.Resolve(ctx, documentID); err == nil || !errors.Is(err, cluster.ErrOwnerNotFound) {
		if err == nil && owner.State == cluster.OwnerActive {
			ownership.removePending(documentID)
		}
		return
	}
	local := ownership.registry.Node()
	nodes, _ := ownership.registry.membership.Snapshot()
	ranked := cluster.RankNodes(documentID, nodes)
	if len(ranked) == 0 || ranked[0].Identity() != local.Identity() {
		return
	}
	select {
	case ownership.candidates <- documentID:
	default:
	}
}

func (ownership *Ownership) removePending(documentID string) {
	ownership.mu.Lock()
	delete(ownership.pending, documentID)
	ownership.mu.Unlock()
}

func documentIDFromOwnerKey(key string) (string, bool) {
	if !strings.HasPrefix(key, ownerPrefix) {
		return "", false
	}
	decoded, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(key, ownerPrefix))
	return string(decoded), err == nil && len(decoded) > 0
}

func (ownership *Ownership) record(handle *cluster.OwnershipHandle) (ownerRecord, bool) {
	ownership.mu.Lock()
	defer ownership.mu.Unlock()
	record, ok := ownership.owners[handle]
	return record, ok
}

func (ownership *Ownership) forget(handle *cluster.OwnershipHandle, cause error) {
	ownership.mu.Lock()
	record, exists := ownership.owners[handle]
	delete(ownership.owners, handle)
	ownership.mu.Unlock()
	if exists {
		record.cancelWatch()
	}
	if cause != nil {
		slog.Warn("document ownership lost", "component", "ownership", "event", "ownership_lost", "doc_id", handle.DocumentID, "fencing_token", handle.FencingToken, "error", cause)
		handle.Cancel(cause)
	}
}

func (ownership *Ownership) observeAttempt(result string) {
	if observer := ownership.registry.observerValue(); observer != nil {
		observer.OwnershipAttempt(result)
	}
}

func (ownership *Ownership) cleanupFailedAcquire(ctx context.Context, key string, owner cluster.Owner, cause error) error {
	payload, _ := json.Marshal(owner)
	requestCtx, cancel := ownership.registry.client.requestContext(ctx)
	defer cancel()
	_, _ = ownership.registry.client.client.Txn(requestCtx).
		If(clientv3.Compare(clientv3.Value(key), "=", string(payload))).
		Then(clientv3.OpDelete(key)).
		Commit()
	return cause
}

func ownerKey(documentID string) string {
	return ownerPrefix + base64.RawURLEncoding.EncodeToString([]byte(documentID))
}
