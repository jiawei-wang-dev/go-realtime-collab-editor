# Phase 10.4 Frontend Review

Date: 2026-11-08 (Australia/Melbourne)

## Scope

The review covered all files under `frontend/src`, the Vite/TypeScript/ESLint configuration, browser authentication and REST handlers, the WebSocket gateway, Room event handling, authentication/session revocation, transport contracts, IndexedDB recovery, and the existing Phase 10 design and integration reports.

## Page review

| Area | Result | Notes |
| --- | --- | --- |
| Login/Register | PASS with refinements | Validation, loading, safe errors, and HttpOnly refresh flow exist. Multi-tab refresh serialization and logout propagation needed strengthening. |
| Dashboard | PASS | Real list/create/delete APIs, search, roles, loading, error, and empty states are present. Native delete confirmation is intentionally retained. |
| Editor | PASS with refinements | Monaco and realtime state are integrated. Offline editing was incorrectly disabled and connection states needed stronger visual differentiation. |
| Settings | PASS | Shows only supported account/session data and performs real logout; unsupported profile mutation is not simulated. |
| Permission management | PASS | Exact username lookup and owner-only role updates use real APIs. Existing permission rows expose IDs when username enrichment is unavailable. |

## Findings and resolution priority

### Critical recovery and isolation findings

1. **Pending edits were keyed by document only.** A different user in the same browser profile could load another user's pending operation for a shared document. The IndexedDB schema is being upgraded to scope pending and recovery state by both user ID and document ID.
2. **ACK persistence was not atomic.** Removing a pending record after an acknowledgement was independent from saving the confirmed document version. A crash in that window could replay an already committed request and stop on `DUPLICATE_REQUEST`. The recovery store is being extended so confirmed content/version, transformed pending operations, and acknowledged-request deletion commit in one IndexedDB transaction.
3. **Offline editing was disabled by the UI and realtime session.** The editor became read-only whenever the socket disconnected. The role, rather than connection state, must decide editability after the first successful join; edits are then persisted before replay.

### Authentication and lifecycle findings

4. Concurrent tabs could rotate the same refresh cookie simultaneously with different request IDs. Browser refresh is being serialized with the Web Locks API where available.
5. Logout did not notify other tabs, and failed refresh cleared the module token without moving the Zustand state to anonymous. BroadcastChannel logout propagation and a session-expired event now clear auth, document, and collaboration state.
6. The realtime reconnect timer is harmless after unmount because every callback checks `stopped`; WebSocket closure and Room leave are already explicit. No duplicate reconnect supervisor was found.
7. Monaco controlled updates are filtered so remote content changes are not emitted as new local edits.

### UI and accessibility findings

8. Connected and error states were visible, but connecting/authenticating/syncing/reconnecting were too similar. Dedicated colors, pulsing indicators, explicit pending count, and a sync pill are being added.
9. The top-right avatar had no action. It now links to account settings with an accessible label.
10. Mobile layout intentionally removes IDE side panels instead of compressing them. Share, account, editor, and status actions remain reachable; actual viewport screenshots are required for acceptance.

## Security review

- No access or refresh token is written to localStorage, sessionStorage, IndexedDB, or application logs.
- Refresh credentials use an HttpOnly, SameSite cookie; production enables `Secure`.
- Monaco content is rendered as text and no `dangerouslySetInnerHTML` use was found.
- REST/WebSocket Origin enforcement is exact; permission and fencing checks remain server-side.
- Owner redirects reject credentials, invalid schemes, loops, excessive redirects, and insecure non-loopback HTTP endpoints.
- An npm audit identified vulnerable transitive `dompurify` and `minimatch` versions. Fixed compatible overrides are pinned and must produce a zero-vulnerability audit before acceptance.

## Gate status

`Phase 10.4.1 Frontend Review`: **PASS**. The issues above are concrete, bounded Phase 10.4 fixes and do not require changes to OT, database schema, protocol compatibility, or distributed ownership guarantees.
