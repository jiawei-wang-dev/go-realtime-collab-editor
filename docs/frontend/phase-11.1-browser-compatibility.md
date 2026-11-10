# Phase 11.1 Browser Compatibility

## Verified

| Browser / mode | Secure context | WebTransport | WebSocket fallback | Result |
| --- | --- | --- | --- | --- |
| System Google Chrome 157.0.8103.38 | Yes | Real HTTP/3/UDP session | Real TLS WebSocket session | PASS |
| Chrome, two isolated browser contexts | Yes | Edit, cursor, recovery, permission and revocation | Same complete scenario | PASS |
| Chrome, two application nodes | Loopback secure context | Owner Redirect, owner termination, fencing and pending recovery | Not repeated in this distributed run | PASS |

Chrome was launched from `/Applications/Google Chrome.app` with normal certificate enforcement. The test did not use Playwright's bundled Chromium, `ignoreHTTPSErrors`, `--ignore-certificate-errors`, or an SPKI-ignore launch flag. `navigator.userAgent`, `window.isSecureContext` and `typeof WebTransport` were captured before the transport tests.

The WebSocket fallback was selected through `auto` with no explicit WebTransport seed and asserted in the UI status bar. When WebTransport was explicitly configured, the status bar asserted `webtransport`. Unit tests also cover a missing WebTransport API, forced transport selection, malformed certificate hashes and the rule that a TLS/policy setup failure must not silently downgrade.

## TLS notes

Vite and REST use an mkcert leaf trusted by the macOS Login Keychain. Chrome QUIC intentionally ignores locally added roots as positive trust anchors; the local WebTransport endpoint therefore uses the standards-based `serverCertificateHashes` SHA-256 option with a 13-day P-256 certificate. Production deployments should use public PKI and omit the development hash.

## Not verified

- Native Firefox WebTransport.
- Native Safari WebTransport; Playwright WebKit is not equivalent evidence.
- Android or iOS browsers.
- Controlled WAN latency and packet loss across equivalent TCP and UDP fault boundaries.
- UDP-blocked enterprise networks beyond the explicit WebSocket selection path.

The supported compatibility posture remains: WebTransport when an operator configures a verified HTTPS endpoint and the browser API exists; WebSocket when the API/endpoint is intentionally unavailable; fail closed for certificate, Origin, authentication and authorization failures.
