# Phase 11.2 Browser Transport Compatibility

## Verified

- System Google Chrome 157: real WebTransport API, HTTP/3 connection, certificate-hash validation, two browser contexts, two-node Owner Redirect, owner failure, reconnect, RESUME/SYNC recovery, pending edit replay, and cursor recovery.
- WebSocket: retained as the compatibility implementation over the same browser session core.
- Security: WebTransport requires HTTPS, exact Origin allowlisting, and a verified server certificate or a short-lived development certificate hash. Setup failures are not silently downgraded because that would hide TLS or policy errors.

## Transport selection

`VITE_REALTIME_TRANSPORT=auto` selects WebTransport only when a WebTransport endpoint is configured and the API exists. Browsers without the API use WebSocket. Operators may force `webtransport` or `websocket` for testing. WebTransport URLs are direct HTTP/3 endpoints because the Vite TCP proxy cannot forward UDP.

## Not claimed

- Native Safari WebTransport acceptance remains unverified.
- Multi-Stream and WebTransport Datagram are not enabled.
- Browser QUIC connection migration is not verified.
- The controlled WAN delay/loss matrix is not verified.

The current browser session uses one reliable bidirectional stream. Cursor/selection messages remain reliable and sequence-numbered. Recovery after a network or owner failure is performed by reconnection and the existing RESUME/SYNC state machine.
