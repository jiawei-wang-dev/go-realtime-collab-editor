# Phase 8.8–9.0 Final Execution Report

Execution date: 2026-11-06 (Australia/Melbourne)

Git branch remained `main` and HEAD remained `0c349f0c7b9babd62dabf8d7cd01c107d4e69621`. No add, commit, push, reset, checkout, clean, restore or stash operation was performed.

## Phase status

| Phase | Result | Evidence |
| --- | --- | --- |
| 8.7 | PASS (preserved, not rerun) | Existing workspace result supplied at recovery start |
| 8.8 | PASS | Observability gate and real dependency suite passed before Phase 8.9 |
| 8.9 | PASS | Real 1/3/5-node load tests, fault injection and full race gate |
| 8.10 | PASS | Security controls, production image, graceful shutdown and full quality gate |
| 9.0 | PASS (preparation only) | Terraform format, validate and offline plan succeeded; no apply |

## Phase 8.9 results

The load tool uses the real REST API, JWT and QUIC transport. Results are detailed in `docs/performance/phase-8.9-load-test.md`.

| Topology | Clients / docs / edits | Errors | Redirects | Throughput | p50 / p95 / p99 |
| --- | --- | ---: | ---: | ---: | --- |
| 1 node | 4 / 2 / 40 | 0 | 0 | 241.3 edits/s | 15.0 / 24.6 / 25.3 ms |
| 3 nodes | 9 / 6 / 135 | 0 | 9 | 491.5 edits/s | 15.5 / 29.9 / 41.3 ms |
| 5 nodes | 15 / 10 / 300 | 0 | 13 | 660.0 edits/s | 19.5 / 29.8 / 47.3 ms |

Database verification confirmed final versions, content, operation counts, distinct request IDs, owner node IDs and fencing tokens.

Real failure tests covered MySQL pause/unpause, fail-closed readiness and authentication, owner crash/takeover, etcd partition/self-fencing/recovery, concurrent ownership, Redis Pub/Sub reconnect, presence expiry, cross-node logout, QUIC revocation, snapshot/replay, SQLite pending recovery and reconnect-loop safety.

Two real defects were fixed:

1. A QUIC owner redirect could be lost because the server closed the connection immediately after writing the redirect frame. A successful redirect now closes the send stream and lets the client close the connection.
2. `Room.Done()` could close before manager reclamation completed. It now acts as a complete room-reclamation barrier.

## Phase 8.10 results

Implemented production hardening:

- Redis-backed fixed-window limits for login, refresh and internal RPC. Redis failure returns HTTP 503 instead of bypassing the control.
- Exact browser Origin allowlist; production permits HTTPS origins only.
- HTTP authentication timeout, one MiB header limit, existing read/write/idle timeouts, and a configurable total QUIC connection limit.
- QUIC server certificate current-validity and server-auth EKU checks.
- Redis TLS CA and server-name configuration for managed encrypted Redis.
- Production validation for Redis credentials and independent JWT/replay secrets.
- Multi-stage container build, minimal runtime, UID/GID 10001, embedded health-check binary, read-only secret mounting guidance and graceful SIGTERM behavior.
- Backup/restore, migration, certificate rotation, rollout and rollback runbook in `docs/operations/phase-8.10-production-readiness.md`.

Real image verification:

- Image build: PASS (standard upstream defaults; mirror/GOPROXY build arguments were used only to route around transient local network failures).
- Runtime user: `10001:10001`.
- Real MySQL and Redis startup: PASS.
- Docker health: `healthy`.
- REST TLS request: PASS (invalid bearer token correctly returned HTTP 401).
- SIGTERM graceful shutdown: PASS, exit code 0.

The final race run found one pre-existing etcd Registry race: `setStatus` wrote the local node while lease-loss logging read it without the registry mutex. The reporting path now takes an atomic snapshot under the mutex. The exact partition test passed five consecutive Race Detector runs after the fix.

## Phase 9.0 results

The selected first-production architecture is Compute Engine, because document ownership redirects require stable per-node UDP endpoints. GKE remains a documented future option after an owner-routing gateway or equivalent stable transport identity exists.

Prepared under `deploy/gcp/`:

- pinned Terraform and Google/random providers;
- custom VPC, subnet, Cloud NAT, private service access and restricted firewall rules;
- three app nodes with stable public QUIC and private internal-RPC identities;
- three-zone, private, mTLS etcd quorum with persistent disks;
- regional REST TCP load balancing;
- Cloud SQL MySQL 8.4 regional HA, private IP, backups, binary-log PITR and Cloud SQL Auth Proxy;
- Memorystore Redis Standard HA, AUTH, private IP and TLS;
- Secret Manager, least-privilege service accounts, shielded VMs and IAP-only SSH;
- baseline uptime, Cloud SQL and Redis alerts;
- deployment, rollback, disaster recovery and cost guidance.

Terraform validation used Terraform 1.17.1 with Google provider 8.10.0 and random provider 3.9.0:

- `terraform fmt -recursive -check`: PASS.
- `terraform init -backend=false`: PASS.
- `terraform validate`: PASS.
- offline `terraform plan -refresh=false`: PASS, `56 to add, 0 to change, 0 to destroy`.
- `terraform apply`: NOT RUN, by design.

The first offline plan caught and prevented an invalid MySQL-only PITR field. The corrected plan uses `binary_log_enabled`, which is the provider's MySQL PITR mechanism.

## Final quality gate

Executed with real isolated MySQL, Redis, etcd, mTLS and QUIC configuration:

```text
go test -count=1 ./...        PASS
go test -count=1 -race ./...  PASS
go vet ./...                  PASS
go build ./...                PASS
git diff --check              PASS
terraform fmt -check          PASS
terraform validate            PASS
offline terraform plan        PASS
```

## Remaining production boundaries

- No Google Cloud resource was created. Quotas, billing, DNS ownership, certificate SANs, IAM access and live regional behavior require a separately approved deployment window.
- Stable Room-owner nodes are intentionally not autoscaled in the first Compute Engine plan.
- The public TCP check proves listener reachability; private `/readyz` remains the dependency-aware rollout gate.
- Cross-region database, Redis and etcd disaster recovery is documented but not automated.
- The current single JWT signing key requires session invalidation during key rotation; multi-key verification is a future security enhancement.
- Cost figures are budgetary ranges and must be refreshed in the Google Cloud Pricing Calculator before approval.

## Safety statement

The real `.env`, user databases, unrelated Docker containers and existing Docker volumes were not modified. All implementation remains uncommitted for manual review.
