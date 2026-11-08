# Phase 10.3 Frontend and Backend Integration Report

Date: 2026-11-08 (Australia/Melbourne)

## Integration architecture

The Go process now exposes `/api/realtime` as a WebSocket transport adapter. Authentication is the first message, after which JOIN, RESUME, EDIT, SYNC, RESYNC_REQUIRED, permission changes, presence, errors, and owner redirects use the shared transport-independent protocol. The adapter implements the existing bounded `transport.Client` contract and sends events to the existing `room.Manager`; it contains no OT or persistence implementation.

Browser login and refresh use dedicated endpoints. The access token is returned to memory, while the rotating refresh token is stored in an HttpOnly, SameSite cookie. Exact Origin validation and credentialed CORS are shared by REST and WebSocket entry points. Production enables the Secure cookie attribute.

## Compatible API additions

- Browser login, refresh, and logout endpoints.
- Visible-document listing and owner-only document deletion.
- Owner-only permission listing and exact authenticated username lookup.
- Cross-node owner forwarding for document deletion over the existing internal mTLS channel.
- Room-scoped `presence_changed` for clients that opt into presence.
- Optional operation `request_id` in SYNC payloads so a browser can resolve an edit committed before an acknowledgement was received.

Existing CLI REST routes, QUIC framing, protocol message fields, Room ordering, OT behavior, permission roles, ownership, and fencing semantics remain compatible.

## Recovery and consistency

Local edits are converted to Unicode code-point Insert/Delete operations, assigned stable request IDs, and persisted to IndexedDB before transmission. The UI maintains confirmed server content separately from optimistic pending operations. Reconnect uses exponential backoff with jitter, AUTH followed by RESUME, and then SYNC or RESYNC_REQUIRED. Pending edits retain their original request IDs and are replayed in order. Redirect endpoints are validated and bounded; failure never falls back to a local write.

## Test evidence

Base backend gates:

```text
go test -count=1 ./...        PASS
go test -count=1 -race ./...  PASS
go vet ./...                  PASS
go build ./...                PASS
```

Frontend gates:

```text
npm run lint       PASS
npm run typecheck  PASS
npm test           PASS (4 files, 7 tests)
npm run build      PASS
```

The Go integration suite covers browser authentication cookies, REST document operations, WebSocket authentication/join/edit/resume, request-ID recovery, two concurrent browser connections, stale-base OT ordering, dynamic permission downgrade, edit rejection, presence, and logout-driven connection closure.

An isolated real-dependency run used MySQL 8.4.13, Redis 7.4.5, TLS-enabled etcd 3.5.17, internal mTLS, and real QUIC. It passed MySQL transaction/fencing/replay, Redis session/presence/Pub/Sub/operation-lease, etcd membership/ownership/failover/partition, cross-node permission forwarding, QUIC failover, and crash-recovery tests. No existing Docker volume was mounted; task-owned containers, network, and certificates were removed after the run. Evidence: `/tmp/collab-phase10-real-248d59fb8a.log` (local execution artifact, not a repository dependency).

## Known limitations

- Browser participant presence contains user IDs; participant profile enrichment is not part of the realtime protocol.
- Selections and cursors are not synchronized.
- IndexedDB recovery is sequential and intentionally does not implement a second full client-side OT engine.
- WebSocket owner redirects require each trusted membership record to expose a browser-reachable HTTPS endpoint.
- A real-device mobile-browser matrix and long browser soak test were not run.
- Google Cloud deployment was not performed.
