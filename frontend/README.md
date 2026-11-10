# Browser Client

React, TypeScript, Monaco Editor, Zustand, and IndexedDB client for the collaborative editor. Browser realtime traffic prefers WebTransport over HTTP/3 and uses WebSocket when the browser has no WebTransport API. Both adapters share the Go server's authentication, routing, Room, OT, permission, ownership, fencing, and persistence path with native QUIC clients.

## Run locally

Start the backend and its MySQL/Redis dependencies first. The Vite server proxies `/api` HTTP and WebSocket traffic to `http://127.0.0.1:8080`.

```bash
npm ci
npm run dev
```

Open `http://127.0.0.1:5173`.

For `.go` documents, owners and writers see a **Run** button. It sends the live
Monaco buffer (including edits not yet persisted by the collaboration path) to
the authenticated execution API and renders stdout, stderr, exit code, elapsed
time, timeout, and truncation state in the output panel. Viewers cannot run
code. The backend and the separate `cmd/runner` process must be configured as
described in [Go Execution Runner](../docs/operations/go-execution-runner.md).

Optional production overrides:

- `VITE_API_URL`: public REST origin.
- `VITE_REALTIME_TRANSPORT`: `auto` (default), `webtransport`, or `websocket`.
- `VITE_WEBTRANSPORT_URLS`: comma-separated `https://.../api/webtransport` seed URLs.
- `VITE_REALTIME_URL`: one complete `wss://.../api/realtime` fallback seed URL.
- `VITE_REALTIME_URLS`: comma-separated seed URLs. The client rotates seeds
  after a failed connection so a surviving node can resolve the new Owner.

In cluster mode, each node publishes `NODE_BROWSER_ENDPOINT` as its public
HTTPS endpoint. This is intentionally separate from `NODE_HTTP_ENDPOINT`,
which is reserved for internal mTLS RPC and is never exposed to browsers.

The server must list the browser origin in `CORS_ALLOWED_ORIGINS`. WebTransport requires HTTP/3/UDP reachability and a browser-trusted certificate; Vite's TCP proxy cannot proxy it. Production requires HTTPS/WSS and a Secure refresh cookie.

## Quality checks

```bash
npm run lint
npm run typecheck
npm test
npm run build
npx playwright test e2e/execution.spec.ts
```

Access tokens stay in JavaScript memory. Rotating refresh tokens use an HttpOnly, SameSite cookie. IndexedDB contains only pending document operations with stable request IDs; it never contains credentials.

See [Phase 10.1 Frontend Architecture](../docs/frontend/phase-10.1-frontend-architecture.md) and [Phase 10.3 Integration Report](../docs/frontend/phase-10.3-integration-report.md) for protocol and recovery details.
