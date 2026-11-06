# Readability Refactoring Report

## Outcome

The Phase 1–9.0 codebase was reviewed package by package and refactored without changing its external APIs, QUIC protocol, persistence schema, distributed ownership model, security rules, or Terraform topology. The final review found no CRITICAL or HIGH issue.

The refactoring deliberately avoided a broad rewrite. It focused on making ownership, lifecycle, and state transitions visible where they matter most.

## Packages reviewed

The review covered every package under `cmd` and `internal`, including the server and CLI composition roots; API; authentication; client and client persistence; cluster, etcd, and internal RPC; collaboration and protocol; Room; storage, MySQL, migrations, and replay; QUIC transport; configuration; observability; and test utilities. SQL migrations, Docker assets, and the GCP Terraform configuration were also inspected.

## Files changed by this review

### Production code

- `cmd/server/main.go`
- `internal/authentication/authenticator.go`
- `internal/authentication/connection_registry.go`
- `internal/authentication/service.go`
- `internal/authentication/session.go`
- `internal/client/manager.go`
- `internal/client/refresh_persistence.go`
- `internal/client/tokens.go`
- `internal/cluster/node.go`
- `internal/cluster/membership.go`
- `internal/cluster/ownership.go`
- `internal/cluster/rendezvous.go`
- `internal/cluster/etcd/ownership.go`
- `internal/cluster/etcd/registry.go`
- `internal/collab/transform.go`
- `internal/protocol/message.go`
- `internal/room/channel_client.go`
- `internal/room/events.go`
- `internal/room/manager.go`
- `internal/room/room.go`
- `internal/storage/replay.go`
- `internal/storage/store.go`
- `internal/transport/client.go`

### Tests and reports

- `internal/api/cluster_permission_integration_test.go`
- `internal/cluster/etcd/registry_integration_test.go`
- `internal/testutil/etcdlock/lock.go`
- `docs/architecture/final-code-review.md`
- `docs/architecture/readability-refactoring-report.md`
- `docs/operations/final-quality-gate.md`

## Improvements by module

### Room and collaboration

Room event types and result types were moved from the event loop implementation into `events.go`. The channel-backed in-process client used by tests and adapters was moved into `channel_client.go`. This leaves `room.go` focused on authoritative document state, serialized event handling, edits, permissions, snapshots, and broadcasts.

The single-goroutine invariant, event ordering, commit-before-memory-update rule, slow-client behavior, snapshot threshold, history trimming, and self-fencing paths were preserved. OT transformation code was not redesigned.

### Authentication

Connection registry and presence responsibilities were separated from the authentication service into `connection_registry.go`. Registry names now describe the resource being protected, and lock-dependent helpers document their locking contract. Presence heartbeat execution has a named lifecycle function.

Redis Client ownership and Pub/Sub worker ownership remain distinct. Closing one Service does not close a shared Redis Client. Refresh rotation, encrypted replay, session revocation, operation leases, presence TTL, and fail-closed behavior were unchanged.

### Client

The long connection method was divided into domain steps: authenticate/start, mark connected, and follow an owner redirect. A redundant token-refresh temporary was removed. Names now expose the connection state transition instead of the mechanics around it.

Reconnect serialization, redirect-loop detection, seed fallback, token rotation, RESUME/SYNC/RESYNC_REQUIRED, stable `request_id`, and SQLite transaction ordering remain unchanged.

### Cluster and ownership

Comments describing membership snapshots, Rendezvous ranking, lease confirmation, ownership transitions, and self-fencing were normalized to concise English. No ownership abstraction was removed: Rendezvous ranking, etcd lease/transaction ownership, and MySQL fencing solve different correctness problems.

### Server composition

Eleven lifecycle arguments were replaced by one internal `serviceGroup` value. The composition root now shows which listeners and servers are owned and shut down together. Existing wrapper signatures needed by tests were retained.

### Tests

Scenario-oriented tests and explicit synchronization were preserved. The cluster dependency fixture was reformatted so every required real-infrastructure input is individually visible. No assertion, failure condition, or integration requirement was weakened.

## Long functions split

- `internal/client/manager.go`: connection establishment was split into `authenticateAndStart`, `markConnected`, and `followRedirect`.
- `cmd/server/main.go`: service lifecycle parameters were grouped into `serviceGroup`, reducing the cognitive load of `serveServices` and `serveWithRegistry`.
- `internal/authentication/service.go`: connection registry and presence lifecycle code moved to a cohesive file and gained `runPresenceHeartbeat`.
- `internal/room/room.go`: event declarations and the channel-backed client moved to dedicated files.

High-risk transactional functions in MySQL storage were intentionally not split across opaque helpers. Keeping row locking, version/fencing checks, document updates, operation-log writes, and commit boundaries together makes transaction ordering easier to audit.

## Repetition and abstraction decisions

No production interface was removed merely to reduce line count. The Storage, Transport, Authenticator, ownership, membership, credential, and client-state boundaries all isolate real external systems or concurrency domains and remain justified.

The review removed a redundant client refresh variable and replaced the server's repeated lifecycle parameter plumbing with a cohesive value. It did not introduce generic helpers, factories, builders, or a global test framework.

Intentional repetition remains in Terraform node identities, certificate mappings, database transaction checks, and scenario tests because explicit code is safer to review than hidden generalized behavior in those areas.

## Naming and comments

Ambiguous registry variables and callback names were replaced with lifecycle-specific names such as `registry`, `closeConnection`, `firstConnection`, and `sessionIDs`. Extracted client functions are named after state transitions.

All Go comments in `cmd` and `internal` are now English. Comments explain locking, ownership, transaction ordering, replay, or shutdown rationale rather than restating individual statements.

## Compatibility and safety

The refactoring did not change:

- REST paths, fields, status codes, or error JSON;
- QUIC framing, ALPN, TLS verification, or protocol message types;
- JWT claims, Redis keys, refresh rotation, or session behavior;
- MySQL schema, migration history, transaction boundaries, or fencing rules;
- OT, versions, permissions, snapshots, replay, or idempotency;
- etcd membership, ownership keys, leases, Rendezvous ranking, or failover order;
- SQLite pending-edit semantics or automatic reconnect behavior;
- Terraform resource topology.

Real MySQL, Redis, etcd, mTLS, QUIC, Race Detector, and Terraform tests provide regression evidence. See `docs/operations/final-quality-gate.md`.

## Ten core files worth reading

1. `internal/room/room.go` — authoritative single-goroutine document workflow.
2. `internal/room/manager.go` — Room creation, recovery, ownership, and lifecycle coordination.
3. `internal/storage/store.go` — persistence and write-authority contracts.
4. `internal/storage/mysql/store.go` — transactional edits, permissions, snapshots, and fencing.
5. `internal/authentication/service.go` — registration, login, refresh, logout, and operation gates.
6. `internal/authentication/session.go` — Redis session, rotation, lease, presence, and Pub/Sub operations.
7. `internal/transport/quic/server.go` — authenticated QUIC connection and stream lifecycle.
8. `internal/client/manager.go` — reconnect, redirect, recovery, and durable pending edits.
9. `internal/cluster/etcd/ownership.go` — lease-backed ownership and fail-closed transitions.
10. `cmd/server/main.go` — complete dependency wiring and graceful shutdown order.

## Remaining issues

No CRITICAL or HIGH issue remains. The largest core files are still substantial, especially the MySQL store and client manager, but further splitting would risk hiding transaction or state-machine ordering behind thin wrappers. Those files should only be divided when a new stable domain boundary emerges.

The integration suite requires Docker and locally generated test certificates. CI should provision the same isolated dependencies and continue treating configuration-driven skips as a failed integration gate.
