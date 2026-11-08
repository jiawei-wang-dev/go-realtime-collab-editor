# Phase 10.1 Frontend Architecture

Date: 2026-11-08 (Australia/Melbourne)

## Scope and source of truth

This document is based on the current Go implementation, not only the project README. The reviewed boundaries are `internal/api`, `internal/authentication`, `internal/protocol`, `internal/room`, `internal/transport/quic`, `internal/client`, `internal/storage`, `internal/cluster`, and `cmd/server`.

The frontend must reuse the existing Room, OT, ownership, fencing, persistence, and session rules. It must not introduce a second collaboration engine.

## Current backend audit

### REST API

All authenticated requests use `Authorization: Bearer <access-token>`. Errors use the shared shape:

```json
{
  "type": "error",
  "code": "ERROR_CODE",
  "message": "safe client-facing message"
}
```

| Method | Path | Authentication | Request | Success | Relevant errors |
| --- | --- | --- | --- | --- | --- |
| `POST` | `/api/auth/register` | No | `{username,password}` | `201 User` | `400 BAD_MESSAGE`, `409 USERNAME_EXISTS`, `429 RATE_LIMITED`, `500 INTERNAL_ERROR` |
| `POST` | `/api/auth/login` | No | `{username,password}` | `200 TokenPair` | `400 BAD_MESSAGE`, `401 INVALID_CREDENTIALS`, `429 RATE_LIMITED`, `503 INTERNAL_ERROR` |
| `POST` | `/api/auth/refresh` | Refresh token in JSON | `{refresh_token,request_id?}` | `200 TokenPair` | `400 BAD_MESSAGE`, `401 AUTHENTICATION_REQUIRED`, `429 RATE_LIMITED` |
| `POST` | `/api/auth/logout` | Bearer | Empty | `204` | `401 AUTHENTICATION_REQUIRED` |
| `GET` | `/api/auth/me` | Bearer | — | `200 User` | `401 AUTHENTICATION_REQUIRED` |
| `POST` | `/api/documents` | Bearer | `{id,content}` | `201 Document` | `400 BAD_MESSAGE`, `401`, `409`, `500` |
| `GET` | `/api/documents/{docID}` | Bearer + read role | — | `200 Document` | `401`, `403 PERMISSION_DENIED`, `404`, `503 SERVICE_UNAVAILABLE` |
| `GET` | `/api/documents/{docID}/permissions/{userID}` | Bearer + owner role | — | `200 {doc_id,user_id,role}` | `401`, `403`, `404`, `503` |
| `PUT` | `/api/documents/{docID}/permissions/{userID}` | Bearer + owner role | `{role}` | `200 {doc_id,user_id,role}` | `400`, `401`, `403`, `503` |

`TokenPair` contains `access_token`, `refresh_token`, `token_type`, and `expires_in`. `User` contains `id`, `username`, `status`, and timestamps. `Document` contains `id`, `content`, and `version`.

The API currently has no document list, document deletion, permission list, or username lookup endpoint. Those capabilities are required for the requested Dashboard and sharing UI and must be implemented against real storage before those controls are enabled.

### Current real-time protocol

The QUIC server accepts one reliable ordered bidirectional stream. Messages are length-prefixed JSON on QUIC; the JSON payload is transport-independent.

1. Client sends `auth` with `token`.
2. Server validates JWT, Redis session, and user status, then returns `auth_ok`.
3. Client sends `join` with `doc_id`, or `resume` with `doc_id` and `last_version`.
4. Server returns `joined`, `sync`, `resync_required`, `owner_redirect`, or `error`.
5. Client sends `edit` with `request_id`, `doc_id`, `base_version`, and one Insert/Delete operation.
6. The Room commits through storage and broadcasts `edit_applied`.
7. Permission changes are delivered as `permission_changed`.

Supported error codes are `BAD_MESSAGE`, `PERMISSION_DENIED`, `VERSION_AHEAD`, `VERSION_CONFLICT`, `DUPLICATE_REQUEST`, `INVALID_OPERATION`, `RESYNC_REQUIRED`, `SERVICE_UNAVAILABLE`, and `INTERNAL_ERROR`.

The current protocol does not contain document-participant presence messages. Redis Presence tracks live sessions and nodes, not the users currently joined to a specific document.

## Browser transport decision

### Options considered

| Option | Compatibility | Operational cost | Decision |
| --- | --- | --- | --- |
| WebTransport Gateway | Browser WebTransport requires an HTTP/3 WebTransport session and is not the existing raw QUIC protocol | Adds HTTP/3/WebTransport handshake, certificate, and browser-availability work | Not selected for Phase 10 |
| WebSocket Gateway | Supported by current browsers; JSON messages map directly to `protocol.Message` | One small transport adapter in each application node | Selected |
| Separate proxy service | Can translate WebSocket to QUIC | Adds another deployable, another failure boundary, and duplicate connection lifecycle logic | Rejected |

### Selected architecture

```text
Browser / Monaco
    |
    | HTTPS REST + WSS JSON
    v
Go application node
    ├── REST API
    ├── WebSocket Gateway (transport adapter only)
    └── existing Room Manager
            |
            ├── versioned OT
            ├── Ownership / Fencing
            └── MySQL transactions
```

The gateway runs in the existing Go process and shares the existing `RoomManager`, `Authenticator`, `ConnectionRegistry`, `OwnerResolver`, and observability hooks. Its connection type implements `transport.Client`, including the bounded non-blocking send queue. It does not perform OT, permission mutation, persistence, or ownership decisions itself.

WebSocket messages use the same JSON fields and message types as QUIC. WebSocket framing replaces only the QUIC length prefix.

### Owner redirect

The QUIC redirect remains unchanged for CLI clients. For a WebSocket connection, the gateway resolves the Owner's trusted `Node` through the etcd-backed `NodeDirectory`, converts its registered HTTPS endpoint to a `wss://.../api/realtime` endpoint, and returns the existing `owner_redirect` message type.

The browser accepts only `wss` redirects in production and only allows `ws` for loopback development. It rejects credentials, fragments, invalid hosts, redirect loops, and excessive redirects. Failure never falls back to a local write. The owner node still acquires the Room through etcd and writes with its MySQL Fencing Token.

## Required compatible backend extensions

These are additive changes. Existing CLI REST and QUIC behavior remains compatible.

### Browser authentication

Add browser-specific auth endpoints while retaining the current CLI endpoints:

| Method | Path | Behavior |
| --- | --- | --- |
| `POST` | `/api/browser/auth/login` | Authenticate credentials, return access-token metadata, set rotated refresh token in an HttpOnly cookie |
| `POST` | `/api/browser/auth/refresh` | Read refresh cookie, rotate with stable `request_id`, return a new access token, replace cookie |
| `POST` | `/api/browser/auth/logout` | Revoke the bearer session and expire the refresh cookie |

The access token lives only in JavaScript memory. The refresh token is never written to `localStorage`, `sessionStorage`, Zustand persistence, logs, or IndexedDB. Production cookies use `Secure`, `HttpOnly`, `SameSite=Strict`, and `Path=/`. Exact Origin validation and credentialed CORS protect browser endpoints. Local HTTP development uses an explicitly named non-`__Host-` cookie without weakening production defaults.

### Dashboard and sharing

Add the following authenticated APIs and matching storage methods:

- `GET /api/documents`: list documents visible to the current user, including role and update time.
- `DELETE /api/documents/{docID}`: owner-only deletion through the Room event queue and a fenced MySQL transaction.
- `GET /api/documents/{docID}/permissions`: owner-only permission list.
- `GET /api/users/lookup?username=...`: exact authenticated lookup for sharing; no broad user enumeration.

Deletion must validate the active `WriteAuthority`, close joined clients, and never fall back to a non-owner node. Foreign-key cascades may remove permissions, operations, and snapshots only inside the same transaction.

### Document presence

Add the transport-independent `presence_changed` message with a bounded list of joined user IDs. The authoritative list comes from the single active Room, so it follows Ownership automatically. Join, Resume, Leave, slow-client removal, logout-driven closure, and Room shutdown update the list. Redis session Presence remains responsible for cross-node session liveness and revocation; it is not repurposed as Room Ownership.

## Frontend architecture

```text
frontend/src/
  api/             Axios client, endpoint types, normalized API errors
  components/      Shared UI primitives and shell components
  editor/          Monaco adapter, operation conversion, realtime session
  hooks/           Page-level orchestration hooks
  pages/           Auth, Dashboard, Editor, Settings
  services/        Browser auth, WebSocket transport, IndexedDB pending store
  stores/          Auth, document, and collaboration Zustand stores
  types/           REST and realtime protocol types
  utils/           Unicode positions, request IDs, validation
```

### Pages

- **Login/Register:** credentials, validation, loading, normalized errors, and session bootstrap.
- **Dashboard:** user summary, real document list, create/open/delete, search, recent updates, and permission entry point.
- **Collaborative Editor:** document navigation, Monaco, connection/sync/version status, joined users, and sharing dialog.
- **Settings:** account/session information and logout. Unsupported profile mutation is shown as unavailable rather than simulated.

### State boundaries

- `authStore`: in-memory access token, expiry, current user, bootstrap/refresh/logout state.
- `documentStore`: REST document summaries, selection, filters, and CRUD state.
- `collaborationStore`: authoritative server content/version, role, connection state, participants, and pending metadata.
- IndexedDB: pending operations with stable request IDs only. It never stores credentials.

Server state remains authoritative. Components do not call Axios or WebSocket directly; page hooks coordinate typed services and stores.

## Monaco and OT integration

The backend operation position is a Unicode code-point index, while Monaco exposes UTF-16 offsets. The editor adapter must convert offsets explicitly and test surrogate pairs.

The browser keeps an authoritative server document separately from Monaco's optimistic display. A local change is reduced to an Insert, Delete, or Delete+Insert sequence, assigned stable request IDs, written to IndexedDB, and then sent. Applied operations are consumed strictly by version. The backend remains responsible for stale-version OT.

Phase 10 serializes local pending operations per document. While operations are awaiting acknowledgement, incoming broadcasts update the authoritative server copy and transform the queued local Insert/Delete operations with the same pairwise OT rules used by the server. This narrow client-side rebase keeps the optimistic Monaco view stable without creating a second authority: the Room still assigns versions, performs stale-version OT, and commits the final operation. Multi-cursor changes are represented as a deterministic replacement range.

No periodic full-document overwrite is used.

## Reconnect and recovery

```text
DISCONNECTED
  -> CONNECTING
  -> AUTHENTICATING
  -> RESUMING (last acknowledged version)
  -> SYNC or RESYNC_REQUIRED
  -> replay persisted pending operations with original request_id
  -> CONNECTED
```

- Only one reconnect supervisor runs per editor session.
- Backoff is exponential with jitter and a cap.
- An ACK removes the matching IndexedDB pending record; the server's idempotent `request_id` and SYNC metadata resolve crashes between acknowledgement and local deletion.
- Unknown outcomes resend the same request ID.
- `VERSION_AHEAD`, `RESYNC_REQUIRED`, redirects, and ownership loss return to recovery rather than rewriting server state.
- `permission_changed` immediately updates editor read-only state.
- Session revocation closes the server connection; failed refresh returns the UI to login without restoring pending writes under another identity.

## Security design

- Production REST uses HTTPS and realtime transport uses WSS; TLS verification is never disabled.
- WebSocket Origin is checked against the same exact allowlist as REST.
- Authentication is the first application message and must complete within a deadline.
- Message size, connection count, send queue, read/write deadlines, and rate limits are bounded.
- Access tokens remain in memory; refresh tokens remain HttpOnly cookies.
- Redirect endpoints come from etcd membership and are revalidated by the browser.
- Permission, ownership, and fencing checks remain in Room and MySQL transactions.
- Client-facing errors never expose SQL, Redis, etcd, JWT, or certificate internals.
- Monaco content is treated as text, never injected as HTML.

## Frontend dependency baseline

Exact versions will be pinned and validated together on Node 24.19.0 / npm 11.17.0:

| Package | Version |
| --- | --- |
| React / React DOM | `19.3.1` |
| TypeScript | `6.0.3` |
| Vite | `8.3.6` |
| `@vitejs/plugin-react` | `6.2.1` |
| Tailwind CSS / `@tailwindcss/vite` | `4.3.5` |
| Monaco Editor | `0.57.0` |
| `@monaco-editor/react` | `4.7.0` |
| Zustand | `5.1.0` |
| Axios | `1.21.0` |
| React Router DOM | `7.18.5` |
| Vitest | `5.1.0` |
| ESLint | `10.14.0` |

The Go WebSocket adapter will pin `github.com/coder/websocket v1.8.15`. No dependency uses a floating `latest` range.

## Frontend/backend flows

### Login

1. Browser posts credentials to the browser login endpoint with credentials enabled.
2. Server creates the existing Redis session, sets the HttpOnly refresh cookie, and returns an access token.
3. Frontend stores the access token in memory and calls `/api/auth/me`.
4. On reload, the frontend calls browser refresh before loading protected routes.

### Open and edit a document

1. Dashboard obtains the accessible document list through REST.
2. Editor opens WSS and sends `auth`.
3. After `auth_ok`, it sends `join` or `resume`.
4. The gateway redirects or joins the existing Room.
5. Monaco changes are persisted as pending operations and sent as `edit`.
6. `edit_applied` advances authoritative content/version and acknowledges matching pending records.

### Permission change

1. Owner resolves the target user by exact username.
2. REST request reaches any node.
3. A non-owner node forwards through the existing mTLS path.
4. The Owner Room and fenced MySQL transaction validate and persist the change.
5. The target connection receives `permission_changed` and becomes read-only immediately when downgraded.

## Phase gates

### Phase 10.1

- Backend and protocol audit complete.
- Browser transport selected without duplicating OT or Room logic.
- Required additive backend gaps explicitly identified.
- No unresolved architecture blocker.

### Phase 10.2

- Build the pinned React/Vite application and all four pages.
- Use real implemented REST endpoints only; unsupported controls remain unavailable until their backend extension exists.
- Validate routing, responsive layouts, Monaco loading, empty/loading/error states, lint, typecheck, unit tests, and production build.

### Phase 10.3

- Implement browser auth extensions, real document APIs, WebSocket Gateway, presence, IndexedDB pending recovery, owner redirect, and end-to-end collaboration.
- Validate two-browser concurrent edits, reconnect, lost ACK idempotency, owner failover/fencing, and session revocation with real dependencies.

## Phase 10.1 result

**PASS.** WebSocket Gateway is the smallest compatible browser transport. It preserves the existing protocol payloads and all authoritative backend correctness boundaries. The identified REST, cookie-auth, redirect, and document-presence additions are additive and do not require rewriting OT, Room management, Ownership, or Fencing.
