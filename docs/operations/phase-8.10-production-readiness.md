# Phase 8.10 Production Readiness

## Security boundary

- Public REST traffic must terminate with TLS 1.2 or newer. QUIC always uses TLS 1.3 and the `collab-editor-v1` ALPN.
- Internal permission RPC uses mutual TLS. A caller certificate must identify a node incarnation that is present in etcd membership; request-body identity is never trusted.
- Production startup requires MySQL, JWT authentication, a Redis password, independent JWT and refresh-replay secrets, and explicit TLS files. Redis, MySQL and etcd failures are fail-closed.
- `CORS_ALLOWED_ORIGINS` is an exact HTTPS-origin allowlist. Non-browser requests without `Origin` remain supported. Client-supplied forwarding headers are not used for rate-limit identity unless a trusted proxy is added deliberately in a future change.
- Login, refresh, and internal RPC limits use Redis fixed windows. A Redis error returns HTTP 503 rather than bypassing the limit. QUIC has independent handshake, idle, operation, frame-size, send-queue and total-connection limits.
- Secrets, certificates, local databases and `.env` files are excluded from the image build context. The runtime image uses UID/GID 10001 and has no shell login account.

## Recommended production settings

Start from `.env.example`, inject values from a secret manager, and set at least:

```text
APP_ENV=production
STORAGE_DRIVER=mysql
AUTH_MODE=jwt
REST_TLS_CERT_FILE=/run/secrets/rest.crt
REST_TLS_KEY_FILE=/run/secrets/rest.key
QUIC_TLS_CERT_FILE=/run/secrets/quic.crt
QUIC_TLS_KEY_FILE=/run/secrets/quic.key
QUIC_CA_FILE=/run/secrets/quic-ca.crt
CORS_ALLOWED_ORIGINS=https://editor.example.com
```

Bind the management listener to loopback or a private management address. Expose TCP 8080 only through the public HTTPS load balancer, UDP 8443 for QUIC, TCP 9443 only inside the node network, and never expose MySQL, Redis or etcd publicly.

## Health and rollout

- `/livez` means the process is alive.
- `/readyz` requires startup completion and successful MySQL, Redis, etcd and membership checks when those components are configured.
- `/metrics` is management-plane only. Alert on readiness loss, ownership/fencing errors, Redis failures, QUIC rejection rate, room queue depth, HTTP 5xx, database pool saturation and recovery failures.
- During rollout, take a node out of service, wait for readiness removal and lease expiry or graceful ownership release, then stop it. Use a canary node before rolling the remaining nodes.
- Roll back the binary only when its schema compatibility is documented. Database migrations are forward-only; restore from a verified backup if an incompatible migration must be reversed.

## Backup and restore

### MySQL

MySQL is the durable authority for documents, permissions, operations, snapshots and fencing tokens. Use a consistent managed backup or `mysqldump --single-transaction --routines --triggers`. Encrypt backups, retain them under a documented policy, and test restoration into an isolated database regularly.

Restore procedure:

1. Stop writes and record the last healthy document versions.
2. Restore into an isolated MySQL instance and run migrations against the restored copy.
3. Verify document versions, operation-log continuity, snapshot versions, permissions and fencing tokens.
4. Point a canary node at the restored database and exercise snapshot plus replay.
5. Switch production only after verification. New owners must activate a fencing token higher than every restored token.

### etcd

Use `etcdctl snapshot save` against a healthy member and validate it with `etcdutl snapshot status`. Restore all members from the same snapshot into a new cluster, using the matching etcd release. etcd contains ephemeral membership and ownership coordination, not document content. After a complete etcd loss, start with no application nodes, restore or rebuild the cluster, then start application nodes so they acquire fresh leases and higher MySQL fencing tokens.

### Redis

Redis stores sessions, refresh rotation state, operation leases, revocation acceleration, presence and rate limits. Managed Redis backup is useful for session continuity, but correctness must not depend on Pub/Sub history. If Redis is rebuilt, invalidate all sessions and require login again; do not copy stale operation leases or presence keys into a new environment.

### Client SQLite

Client SQLite files contain pending edits and recovery state. They are client-owned and are not part of the server backup. A user should preserve the profile directory during client upgrades. Never merge SQLite files from two devices.

## Migration safety

1. Back up MySQL and verify free disk space.
2. Run migrations in staging with a production-sized copy.
3. Confirm that every migration is repeat-safe according to the migration runner and that older binaries tolerate the new schema during the rollout window.
4. Apply migrations once before scaling new binaries. Do not let an application instance silently fall back to memory storage.
5. Validate row counts, constraints and indexes, then run the smoke and replay tests.

## Certificate and secret rotation

- Issue node certificates with the expected DNS names and SPIFFE node/incarnation identity. Monitor expiry before the certificate enters the deployment window.
- For CA rotation, distribute a trust bundle containing old and new roots, roll leaf certificates, then remove the old root in a second rollout.
- Rotate Redis/MySQL credentials with overlapping accounts where the service permits it. Rotate JWT signing keys only with an explicit multi-key verification design; the current single-key configuration requires session invalidation during rotation.
- Never print secrets in logs or pass them as command-line flags visible to other processes.

## Container verification

Build and inspect locally:

```bash
docker build -t collab-editor:verify .
docker inspect collab-editor:verify
docker run --rm --entrypoint /usr/bin/id collab-editor:verify
```

The reported identity must be UID/GID 10001. Mount certificates read-only and inject environment variables at runtime. Send SIGTERM and allow the server shutdown timeout to drain HTTP, QUIC, rooms and ownership before force-killing the container.
