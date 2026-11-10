# Phase 11.0 Transport Architecture Audit

## Scope and baseline

This audit was completed before the Phase 11.0 transport implementation. The repository baseline uses Go 1.26.10, `quic-go` v0.49.1, a custom native QUIC protocol, and a browser WebSocket gateway. GCP and Cloudflare deployment work is frozen for this phase; no cloud resource is part of the WebTransport implementation or validation.

WebTransport is not wire-compatible with the existing native QUIC listener. Native QUIC negotiates the custom ALPN `collab-editor-v1` and immediately opens one application stream containing 4-byte-length-prefixed JSON. Browser WebTransport first establishes HTTP/3, performs an Extended CONNECT session handshake, and then exposes WebTransport streams. The adapters can share framing and application messages, but not the QUIC handshake or listener implementation.

## Current transport paths

### Native QUIC

1. `internal/transport/quic.Server` listens on UDP, enforces TLS 1.3, and negotiates `collab-editor-v1`.
2. The client opens one bidirectional QUIC stream.
3. `AUTH` is the first framed JSON message. The server validates the JWT and Redis-backed session through `authentication.Authenticator`.
4. `JOIN` or `RESUME` is required next. In cluster mode the adapter resolves etcd Ownership and either opens the local Room or returns `owner_redirect` with the Owner's native QUIC endpoint.
5. `quictransport.Client` implements the transport-independent `transport.Client` interface. The Room only sees `Identity`, nonblocking `TrySend`, and idempotent `Close`.
6. Every subsequent application message validates or acquires a lease for the authenticated Principal before reaching the Room event queue.
7. Session revocation closes the connection through `authentication.ConnectionRegistry`.

Native QUIC currently handles edit, resume, sync, resync, permission and redirect messages. It intentionally does not opt into the optional browser Presence/Cursor capability interfaces.

### Browser WebSocket

1. REST exposes `GET /api/realtime`; the API exact-Origin middleware executes before the WebSocket upgrade.
2. `internal/websocket.Gateway` accepts the upgrade, applies a 1 MiB read limit, and requires an `AUTH` JSON message within the authentication timeout.
3. It registers the authenticated session in the same `ConnectionRegistry` used by native QUIC.
4. For every `JOIN`, `RESUME`, `EDIT`, and `CURSOR_UPDATE`, it validates the Principal or acquires an operation lease.
5. It resolves Ownership before opening a Room. A remote owner returns `owner_redirect` using the node's browser HTTPS endpoint.
6. Its client implements `transport.Client`, `PresenceClient`, and `CursorClient`; Room broadcasts therefore remain transport-independent.
7. A bounded send channel and nonblocking `TrySend` preserve the Room's slow-client behavior.

### Shared application semantics

Both adapters use `internal/protocol.Message` and the same message names: `auth`, `auth_ok`, `join`, `joined`, `edit`, `edit_applied`, `permission_changed`, `resume`, `sync`, `resync_required`, `owner_redirect`, and `error`. Browser-only Presence and Cursor messages are extensions of the same struct and are processed by the same Room.

Neither adapter owns OT, permissions, persistence, snapshots, fencing, or document lifecycle. Those remain in Room, Storage and Ownership. This boundary is suitable for adding WebTransport without another collaboration engine.

## Authentication, authorization and revocation

- JWT parsing establishes a `Principal` containing server-derived user and session IDs.
- Redis is consulted by `ValidatePrincipal` / `AcquirePrincipal`; logout and revoked sessions fail closed.
- `AcquirePrincipal` pins one application operation while logout waits for in-flight work.
- `ConnectionRegistry` associates live connections with a session. Local and Redis-driven cross-node revocation invoke the transport's idempotent close callback.
- Document read/write permissions remain Room/Storage concerns. A transport adapter must never infer authorization from client fields.
- MySQL Fencing Token checks remain inside write transactions and are unaffected by the browser transport.

## Redirect and recovery

- Native clients follow `owner_redirect` to `Node.QUICEndpoint`.
- Browser clients follow the same message to `Node.BrowserEndpoint`, reject insecure or malformed endpoints, cap redirects, and detect loops.
- The frontend keeps confirmed state and original Pending `request_id` values in IndexedDB. Reconnection sends `RESUME`; the server returns incremental `SYNC` or `RESYNC_REQUIRED` with Snapshot + Replay.
- Transport replacement must be below this recovery state machine. Implementing a second WebTransport-specific OT/Pending state machine would risk duplicate edits.

## WebTransport server selection

The selected implementation is `github.com/quic-go/webtransport-go` v0.13.0. It is maintained alongside `quic-go`, implements the current draft-16 protocol, supports the HTTP/3 settings required by current Safari, and requires Go 1.26—matching this repository. It requires `quic-go` v0.62.0, so the existing v0.49.1 dependency must move to v0.62.0 and the native adapter must be updated only for upstream API changes. Native QUIC protocol behavior and ALPN remain unchanged.

An older WebTransport release was rejected:

- v0.8.0 targets `quic-go` v0.43.0 and an older protocol draft.
- v0.9.0 targets `quic-go` v0.53.0 but predates the Safari HTTP/3 settings interoperability work.
- v0.11.1 includes the Safari work but uses draft-15 and `quic-go` v0.60.0.
- v0.13.0 matches the repository's Go version and the current WebTransport draft while avoiding an unsupported dependency mix.

The implementation will use one reliable bidirectional WebTransport stream and the existing 4-byte length-prefixed JSON format. Datagrams are intentionally deferred: edit, authentication, resume and redirect require reliable ordering, while moving Cursor/Presence alone to datagrams would require explicit loss, sequence and expiry behavior plus separate browser compatibility validation. Reliable streams preserve current semantics for this phase.

Sources consulted during selection:

- [`webtransport-go` releases](https://github.com/quic-go/webtransport-go/releases)
- [`quic-go` releases](https://github.com/quic-go/quic-go/releases)
- [W3C WebTransport Candidate Recommendation](https://www.w3.org/TR/webtransport/)
- [MDN WebTransport API](https://developer.mozilla.org/en-US/docs/Web/API/WebTransport)
- [Safari 26.4 WebTransport announcement](https://webkit.org/blog/17862/webkit-features-for-safari-26-4/)

## Listener and endpoint design

Native QUIC remains on its existing UDP address and custom ALPN. WebTransport requires a separate HTTP/3 listener. TCP and UDP can share a numeric port, so WebTransport can bind UDP on the same public port used by REST HTTPS/WebSocket over TCP:

```text
TCP :8080  REST HTTPS + WebSocket fallback
UDP :8080  HTTP/3 + WebTransport
UDP :8443  existing native QUIC
```

This avoids changing `Node.BrowserEndpoint`: the same `https://host:port` base can produce `/api/webtransport` over HTTP/3 and `/api/realtime` over WSS. No new owner redirect field is required.

The WebTransport listener requires a publicly trusted certificate in real browser deployments. Local integration can use a short-lived certificate hash only for controlled browser tests; production must not disable certificate verification.

## Adapter design

The backend WebTransport adapter will:

1. expose only `CONNECT /api/webtransport` over HTTP/3;
2. validate the Origin against the exact configured browser allowlist before Upgrade;
3. accept exactly one reliable bidirectional application stream;
4. use the same framed `protocol.Message` semantics;
5. authenticate before Room selection;
6. reuse Owner resolution, Principal operation leases, Room events, bounded sending, Cursor/Presence capability, session registry and graceful leave;
7. return browser Owner Redirect endpoints, never internal mTLS or native QUIC endpoints;
8. close all sessions and workers during server shutdown.

The browser will receive a small transport interface (`connect`, `send`, `close`, message/close callbacks). The existing `RealtimeSession` remains the single authority for reconnect, redirect, token refresh, `RESUME`, `SYNC`, Pending replay, Cursor, and request IDs. WebTransport is tried first when an HTTPS WebTransport seed is configured and the API is supported; an absent configuration or API selects WebSocket. Browsers don't expose a portable error taxonomy that reliably separates blocked UDP from TLS and Origin policy failures, so an establishment failure in a configured, WebTransport-capable browser remains visible rather than silently weakening the transport. Operators can explicitly select WebSocket. A protocol `PERMISSION_DENIED`, invalid token, Origin failure, or other application/security error must never trigger a downgrade.

## Browser compatibility baseline

As of this audit, MDN marks WebTransport as Baseline 2026 and secure-context only. Chromium supports it broadly, Firefox supports it in current releases, and Safari added it in 26.4. Actual acceptance still requires separate real-engine testing:

- Playwright Chromium can validate the primary path.
- Playwright Firefox can validate Firefox, subject to its packaged version and WebTransport build flags.
- Playwright WebKit is not proof of native Safari; Safari 26.4+ must be recorded separately as manual or real-device verification.
- Older browsers and environments where UDP/HTTP/3 is blocked must retain WebSocket fallback.

## Performance test constraints

WebTransport must not be described as faster without measurements under an identical workload, certificate setup, document size, connection count and host. The comparison must record establishment, message RTT, edit propagation, p50/p95/p99, throughput, CPU, memory, reconnect and packet-loss behavior. Local loopback data is a controlled development measurement, not a production capacity claim. Browser WebTransport over one reliable stream can still experience ordering within that stream; its main architectural gain here is a QUIC/HTTP3 browser path and future independent streams/datagrams, not an assumed latency win.

## Risks and gates

1. `quic-go` v0.62 API changes can break native QUIC; its complete integration suite and TLS/ALPN assertions are mandatory.
2. UDP reachability and trusted TLS are independent of TCP HTTPS; a healthy REST endpoint does not prove WebTransport.
3. Browser fallback must classify establishment/unsupported failures separately from authenticated protocol failures.
4. Origin checks must run before session Upgrade and must not use a wildcard with credentials.
5. One browser session must map to one Room client and one session-registry entry; duplicate stream acceptance must be rejected.
6. The server and frontend need observable transport labels so tests and operators can prove which path was used.
7. Datagrams remain out of scope until their loss semantics are explicitly designed and measured.
