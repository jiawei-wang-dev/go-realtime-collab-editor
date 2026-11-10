# Phase 11.0 Browser Compatibility

## Automated environment result

On 2026-11-10 the repository's installed Playwright engines were launched independently and evaluated with `typeof WebTransport`:

| Engine | Result |
| --- | --- |
| Playwright Chromium | `undefined` |
| Playwright Firefox | `undefined` |
| Playwright WebKit | `undefined` |

Therefore the real browser WebTransport path was **not** accepted in these packaged engines. The frontend selection tests prove that these browsers take the WebSocket fallback, but this is not equivalent to a successful WebTransport connection.

The installed system Google Chrome 157.0.8103.38 was also launched headlessly on an HTTPS page; it reported `typeof WebTransport === "function"` and `isSecureContext === true`. This proves API availability only. A real session against the project was not completed because the local project test origin does not currently have a browser-trusted certificate; disabling certificate validation would not be valid acceptance evidence.

The Go integration test establishes a real WebTransport session over UDP/TLS/HTTP3 and validates the complete application handshake. It proves server interoperability with `webtransport-go`, not browser interoperability.

## Required manual/real-engine validation

- Current desktop Chromium with WebTransport enabled and a browser-trusted TLS certificate.
- Current Firefox with WebTransport enabled under its released defaults.
- Native Safari 26.4 or later. Playwright WebKit must not be reported as native Safari.
- A network that permits UDP to the HTTP/3 port.
- Two real browser sessions covering edit, cursor, reconnect, Resume/Sync, permission downgrade, revocation and Owner Redirect.

Until those checks pass, browser compatibility status for WebTransport is **NOT VERIFIED**. WebSocket fallback remains verified by the existing browser client and backend suites.
