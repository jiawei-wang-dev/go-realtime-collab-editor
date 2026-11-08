# Phase 10.5 Cursor and Selection Acceptance

Status: **PASS**
Date: 2026-11-09

## Delivered behavior

- `cursor_update`, `cursor_changed`, and `cursor_removed` are authenticated WebSocket messages.
- Cursor and selection offsets use Unicode code points, matching OT operations.
- The authoritative Room goroutine validates, transforms, sequences, broadcasts, and removes cursor state.
- Cursor traffic does not update the document version, operation log, snapshot, or MySQL content.
- Stored cursor positions follow accepted insert/delete operations; stale client positions are transformed through retained history.
- The browser throttles updates to 20 per second, sends a 10-second heartbeat, and republishes after reconnect.
- Monaco decorations show stable per-user colors, selections, carets, and server-derived usernames without changing editor text.
- Decorations are disposed on document changes, navigation, session revocation, and component unmount; inactive remote state expires locally after 30 seconds.

## Security and failure handling

- Identity and username are derived from the authenticated principal, never from cursor message fields.
- Room read permission is checked for each update.
- WebSocket read limits remain in force; cursor messages also have a 30-message-per-second server limit.
- Invalid coordinates and versions are rejected by the Room. Because cursor presence is ephemeral, a cursor that becomes stale while an edit acknowledgement crosses in flight is discarded without terminating a valid document session.
- Cursor state always follows the current Owner Room and is not forwarded around ownership or fencing checks.

## Automated and real-environment results

| Gate | Result |
|---|---|
| Go tests | PASS |
| Go race detector | PASS |
| `go vet` / `go build` | PASS |
| Frontend lint / typecheck / 11 unit tests / build | PASS |
| Real two-browser collaboration | PASS |
| Real two-node cursor + Owner failover | PASS |
| `git diff --check` | PASS |

The isolated single-node run ended with document version 11, 11 operation-log rows, and 11 distinct request IDs. Cursor and selection traffic did not add rows. Log: `/tmp/collab-phase104-single-d889b639a4.log`.

The isolated two-node run used real MySQL, Redis, etcd, mTLS, Owner Redirect, and fencing. After Owner termination, the replacement Owner reached fencing token 10; the document ended at version 4 with four operation-log rows and four distinct request IDs. Log: `/tmp/collab-phase104-cluster-0d3ae13b9c.log`.

## Known boundary

Cursor presence is intentionally ephemeral and is rebuilt from active clients after Owner failover. It is not persisted or replayed. The current permission model has `owner`, `write`, and `read` roles but no permission-delete endpoint; a read-only user may still observe collaboration, as required by the existing authorization model.
