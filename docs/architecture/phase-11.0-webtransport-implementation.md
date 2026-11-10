# Phase 11.0 WebTransport Implementation

## Implemented boundary

The browser adapters now terminate at `internal/transport/browser`, a shared reliable-message session that owns authentication, Redis-backed Principal validation, operation leases, session revocation, Owner Redirect, Room selection, Resume/Sync, edits, cursor updates, bounded sending, and graceful leave. WebSocket and WebTransport contain only handshake and message I/O code. Room, OT, permissions, ownership, fencing, snapshots, and persistence are unchanged.

The WebTransport adapter listens on a dedicated UDP socket, negotiates HTTP/3 with TLS 1.3, exposes only `CONNECT /api/webtransport`, applies an exact Origin allowlist before upgrade, and accepts one reliable bidirectional stream. Application JSON uses the same four-byte big-endian length prefix as native QUIC, with a 1 MiB default limit. Datagrams are not used because the existing protocol assumes reliable ordered delivery.

Native QUIC keeps its `collab-editor-v1` ALPN and UDP endpoint. The required `quic-go` v0.62 API migration changes Go types and listener ownership only; it does not change the native wire protocol.

## Browser selection and recovery

`RealtimeSession` remains the only browser collaboration state machine. A new transport adapter supplies connect, message, send, and close behavior. Pending IndexedDB records, stable `request_id`, reconnect backoff, token refresh, `RESUME`, `SYNC`, `RESYNC_REQUIRED`, redirect limits, cursor throttling, and OT rebasing remain in the original class.

`VITE_REALTIME_TRANSPORT=auto` prefers WebTransport when an explicit HTTPS WebTransport seed is configured and the API exists. An absent seed or API selects WebSocket, preserving existing deployments. A WebTransport-capable browser does not silently downgrade after a TLS, Origin, or policy failure because browser errors cannot reliably distinguish a blocked network from a security failure. `webtransport` and `websocket` force a path for operations and compatibility testing. The selected transport is shown in the editor status bar.

## Configuration

- `WEBTRANSPORT_ENABLED=false` preserves existing deployments by default.
- `WEBTRANSPORT_ADDR=:8080` uses UDP and can share the REST numeric port used over TCP.
- WebTransport certificate/key settings may reuse the REST TLS files.
- Authentication, operation, write, stream and idle timeouts are independently bounded.
- `VITE_WEBTRANSPORT_URLS` contains HTTPS seed URLs; browser Owner Redirect continues to use trusted `NODE_BROWSER_ENDPOINT` values.

## Security review

- TLS 1.3 and HTTP/3 ALPN are mandatory.
- Empty, wildcard-like, or nonmatching Origins are rejected.
- Authentication completes before Room selection.
- Every application message validates or leases the authenticated Principal.
- Client-supplied identity fields are not trusted.
- Redis revocation closes WebTransport through the existing connection registry.
- MySQL fencing remains the final authority for writes.
- Internal mTLS and native QUIC endpoints are never exposed in browser redirects.
- WebSocket remains available but security and authorization failures never trigger automatic downgrade.

## Verified locally

The real Go WebTransport integration uses UDP, TLS, HTTP/3 Extended CONNECT, one bidirectional stream, AUTH, JOIN, Presence, EDIT and session revocation. Existing WebSocket and native QUIC integration suites continue through the shared Room core. Frontend unit tests cover URL validation, unsupported-browser fallback, forced transport selection, and no downgrade after a WebTransport security/setup error.

Real Chromium, Firefox, and native Safari WebTransport acceptance still requires a browser-trusted certificate and UDP-enabled test origin. Playwright WebKit is not treated as native Safari evidence.
