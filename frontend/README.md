# Browser Client

React, TypeScript, Monaco Editor, Zustand, and IndexedDB client for the collaborative editor. Browser realtime traffic uses the Go server's WebSocket gateway and therefore shares the existing Room, OT, permission, ownership, fencing, and persistence path with QUIC clients.

## Run locally

Start the backend and its MySQL/Redis dependencies first. The Vite server proxies `/api` HTTP and WebSocket traffic to `http://127.0.0.1:8080`.

```bash
npm ci
npm run dev
```

Open `http://127.0.0.1:5173`.

Optional production overrides:

- `VITE_API_URL`: public REST origin.
- `VITE_REALTIME_URL`: one complete `wss://.../api/realtime` seed URL.
- `VITE_REALTIME_URLS`: comma-separated seed URLs. The client rotates seeds
  after a failed connection so a surviving node can resolve the new Owner.

In cluster mode, each node publishes `NODE_BROWSER_ENDPOINT` as its public
HTTPS endpoint. This is intentionally separate from `NODE_HTTP_ENDPOINT`,
which is reserved for internal mTLS RPC and is never exposed to browsers.

The server must list the browser origin in `CORS_ALLOWED_ORIGINS`. Production requires HTTPS/WSS and a Secure refresh cookie.

## Quality checks

```bash
npm run lint
npm run typecheck
npm test
npm run build
```

Access tokens stay in JavaScript memory. Rotating refresh tokens use an HttpOnly, SameSite cookie. IndexedDB contains only pending document operations with stable request IDs; it never contains credentials.

See [Phase 10.1 Frontend Architecture](../docs/frontend/phase-10.1-frontend-architecture.md) and [Phase 10.3 Integration Report](../docs/frontend/phase-10.3-integration-report.md) for protocol and recovery details.
