# Phase 11.1 Real Browser Acceptance

## Environment

Acceptance ran on 2026-11-11 on macOS 26.5 with system Google Chrome 157.0.8103.38. The browser was launched through Playwright's `channel=chrome`/system executable path, not the bundled Chromium. Browser contexts used `ignoreHTTPSErrors=false`; no certificate-error or SPKI-ignore command-line flag was supplied.

The test environment was isolated from repository configuration and existing services:

- MySQL 8.4.13, Redis 7.4.5 and etcd 3.5.17 ran in dedicated, volume-free containers.
- Vite served HTTPS on loopback using an mkcert leaf certificate whose SAN covered `localhost`, `127.0.0.1` and `::1`.
- REST used the same locally trusted certificate over TCP.
- WebTransport listened on the same numeric port over UDP and negotiated HTTP/3/TLS 1.3.
- All leaf keys and generated credentials lived in a mode-`0600` temporary directory outside the repository.

Chrome's certificate verifier accepts a locally installed root for TCP TLS, but intentionally does not add local roots as positive trust anchors for QUIC. Therefore REST and Vite used normal platform PKI validation while local WebTransport used the standard `serverCertificateHashes` SHA-256 certificate pin. The pin was generated at process start from a short-lived, P-256 WebTransport leaf certificate. This is WebTransport certificate validation, not disabled validation or a browser bypass. Production remains expected to use a publicly trusted certificate and omit the development pin.

## Verified data path

```text
System Chrome 157
  -> WebTransport API (secure context)
  -> HTTP/3 + TLS 1.3 over UDP
  -> Go WebTransport listener
  -> shared browser session
  -> Room / OT / permission logic
  -> MySQL transaction and operation log
```

The real-browser test completed AUTH, JOIN, EDIT, edit acknowledgement, cursor/selection presence, offline reconnect, RESUME/SYNC, IndexedDB pending replay with the original `request_id`, refresh bootstrap, live permission downgrade and Redis-backed session revocation. Two independent Chrome contexts edited the same document and converged after same-position, different-position and rapid concurrent edits. The status bar explicitly asserted `webtransport`; a second full run explicitly asserted `websocket`.

Origin enforcement was tested from `https://127.0.0.1` against an allowlist containing only `https://localhost`; the WebTransport upgrade was rejected. An invalid JWT also failed closed and the session was terminated. Chrome may observe that terminal authentication rejection as a closed/reset stream rather than a readable JSON error because closing the HTTP/3 session races the final stream delivery; it never joins a Room or falls back to WebSocket.

## Distributed recovery update

Real etcd/MySQL tests passed for ownership takeover, concurrent acquisition with one winner, failed recovery never becoming active and MySQL fencing of the old owner. Real Redis native-QUIC tests passed for cross-node logout and remote connection revocation. These results preserve the existing distributed ownership guarantees, but they are not presented as a two-node browser test.

The follow-up recovery run added a real two-node browser test. Two system Chrome contexts connected through different application seeds, followed an Owner Redirect, collaborated over WebTransport, survived an owner `SIGKILL`, recovered a pending edit after a higher Fencing Token was activated, and continued editing. The terminated node then rejoined with a new incarnation without reclaiming the active ownership. See [Phase 11.1 Recovery Report](phase-11.1-recovery-report.md).

## Not verified in this run

- Native Firefox and native Safari WebTransport interoperability.
- Packet-loss, latency and network-switch behavior; safe host-independent shaping was unavailable.
- WebTransport datagrams, multiple application streams and connection migration. The current implementation intentionally uses one reliable bidirectional stream per document, so these are not implemented features.

Phase 11.1 single-node and distributed browser acceptance are complete. The overall Phase 11.1 network matrix remains incomplete until controlled latency and loss are exercised without mocks.
