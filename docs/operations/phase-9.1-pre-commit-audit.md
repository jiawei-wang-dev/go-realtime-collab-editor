# Phase 9.1 Pre-Commit Audit

## 1. Scope

This audit covers the complete Phase 1–9.0 workspace: 26 Go packages, production and test code, SQL migrations, Docker assets, Google Cloud Terraform, configuration examples, and project documentation. It also revalidates real MySQL, Redis, etcd, mutual-TLS, QUIC, and SQLite behavior.

The audit started and ended on branch `main` at HEAD `0c349f0c7b9babd62dabf8d7cd01c107d4e69621`. No Git index, commit, branch, or remote operation was performed.

## 2. Git workspace

Before the audit the workspace contained 15 tracked modified files, 2 tracked deletions, and 127 untracked files. The two deleted WebSocket files are intentional: QUIC is the only formal realtime transport. The untracked tree is the accumulated Phase 1–9.0 implementation, not disposable output.

After adding this report, the commit candidate contains 147 existing files:

| Category | Files |
| --- | ---: |
| Go source | 65 |
| Go tests | 46 |
| Database migrations | 3 |
| Docker | 3 |
| Terraform, templates, lock file, and deployment README | 13 |
| Documentation, including root README | 13 |
| Configuration (`.gitignore`, `.env.example`, `go.mod`, `go.sum`) | 4 |
| Scripts outside Terraform templates | 0 |
| Temporary files | 0 |
| Sensitive files in commit candidate | 0 |
| Total | 147 |

Every untracked file was checked for type and size. All are text source, test, configuration, migration, Terraform, Docker, or documentation files. No duplicate-content groups, symlinks, build outputs, profiles, logs, archives, or debug dumps were found. A five-line test-only file that contained only a regex used by `store_test.go` was merged into that test file and removed.

Ignored local items are `.env`, `.DS_Store`, Terraform provider binaries, and cache files. They are not part of the commit candidate. No ignored Go, SQL, Terraform, Markdown, Docker, or module source was found. `.gitignore` and `.dockerignore` now also exclude PEM, CRT, CER, P12, PFX, and private-key files outside the conventional `certs/` directory.

The tracked diff was reviewed together with all untracked content. It represents the Phase 1–9.0 migration from the original WebSocket/memory implementation to the documented QUIC, MySQL, Redis, etcd, recovery, observability, production, and GCP design.

## 3. Architecture and code review

The audit confirmed these boundaries and invariants:

- Room imports the transport interface, not the QUIC implementation. A Room goroutine remains the only in-memory writer for one document.
- QUIC is the only formal realtime server. No WebSocket package, dependency, route, or protocol implementation remains.
- REST owns authentication, document creation/read, permission operations, CORS, rate limiting, and cross-node forwarding; it does not implement OT.
- Rendezvous hashing ranks preferred owners but does not grant ownership. etcd transactions and leases grant ownership; MySQL fencing is the final durable-write authority.
- `CommitEdit`, `SetPermission`, recovery/baseline snapshot, and `SaveSnapshot` validate `NodeID` and fencing token in MySQL transactions. Durable commit precedes Room mutation and broadcast.
- Snapshot plus ordered operation replay validates version continuity and reconstructed content.
- Redis sessions, refresh rotation/replay, operation leases, presence, Pub/Sub revocation, and periodic reconciliation retain fail-closed behavior.
- Client reconnect, authenticated owner redirect, RESUME/SYNC/RESYNC_REQUIRED, stable `request_id`, and SQLite pending-edit transactions remain intact.

Lifecycle inspection covered every production goroutine and channel close. Room entry readiness/stopped channels use `sync.Once`; QUIC client/server close paths are idempotent; authentication Pub/Sub subscriptions are actively closed before waiting; Manager reconnect is serialized; etcd lease loss triggers self-fencing; server shutdown stops listeners, QUIC, Rooms, registry, stores, and observability components in dependency order.

No new business feature, API, protocol field, database schema, migration, ownership rule, or Terraform deployment topology was introduced by this audit.

## 4. Sensitive information review

Gitleaks was not installed. An equivalent repository and Git-history scan was performed without printing candidate secret values:

- no private-key header;
- no AWS, GitHub, Slack, Google API, OpenAI-style, or JWT token pattern;
- no tracked `.env`, certificate, private key, Terraform state, or plan;
- no sensitive filename in Git history;
- no access/refresh token, password, DSN, or private key written by application logging;
- no secret payload in README or committed Terraform variables.

The real local `.env` was confirmed ignored and its values were not read or reported. Test credentials are fixed test-only strings and production configuration rejects weak/missing secrets. Structured logging recursively redacts keys containing authorization, password, secret, token, private key, DSN, refresh, or document content.

The audit found and fixed one HIGH Terraform issue: MySQL and Redis credentials were interpolated into VM startup metadata. Startup metadata now contains only Secret Manager IDs; the VM fetches the values at boot. Redis AUTH has its own Secret Manager secret. Project-wide `secretmanager.secretAccessor` grants were replaced by eight per-secret IAM bindings. The generated MySQL password is 32 alphanumeric characters so it cannot break the DSN or shell boundary.

## 5. Security mechanisms

- JWT accepts only HS256 and validates issuer, audience, expiration, token type, session ID, subject, and JTI. The secret must be at least 32 bytes.
- Refresh replay uses AES-256-GCM with a random nonce and request/session/hash additional authenticated data.
- Redis Session validation and operation leases fail closed. Logout waits for distributed in-flight operations and remote connections are revoked by Pub/Sub plus reconciliation.
- QUIC requires TLS 1.3, fixed ALPN, server certificate verification, message-size limits, deadlines, and connection limits. `InsecureSkipVerify` is rejected.
- Internal RPC uses mutual TLS, SPIFFE-style NodeID plus Incarnation SAN identity, and live etcd membership validation.
- REST has exact-origin CORS, login/refresh/internal rate limits, header/read/write/idle timeouts, and bounded headers.
- Docker runs as UID/GID 10001 with a healthcheck and exec-form entrypoint. The Go server handles SIGINT/SIGTERM gracefully.

## 6. Dependencies and vulnerabilities

`go mod tidy` and `go mod verify` pass. All versions are fixed in `go.mod`/`go.sum`; no floating dependency is used.

The first `govulncheck` run found 47 reachable vulnerabilities in the Go 1.24.10 standard library and seven modules. Minimal security upgrades were applied to QUIC, JWT, Redis, OpenTelemetry, gRPC, `x/net`, and compression dependencies. The fixes require Go 1.25/1.26, so retaining a Go 1.22 build baseline was not safe. The module language version is Go 1.26, while the module toolchain and production builder are pinned to the patched Go 1.26.10 release.

The final Go 1.26.10 `govulncheck` result is **0 reachable vulnerabilities**. It reports two vulnerable imported packages and six vulnerable modules with no reachable symbol from this program; these should continue to be monitored.

## 7. License review

The installed `go-licenses` version does not understand the Go 1.26 toolchain layout, so a module-level scan inspected the actual license file of all 152 modules in the graph:

| License | Modules |
| --- | ---: |
| Apache-2.0 | 53 |
| BSD family | 49 |
| MIT | 48 |
| ISC | 1 |
| MPL-2.0 | 1 |
| GPL / AGPL | 0 |
| Unknown | 0 |

The MPL-2.0 module is `github.com/go-sql-driver/mysql`; MPL applies at file level and does not impose a GPL/AGPL distribution requirement on this project. No dependency license blocks normal commercial use or binary redistribution, subject to preserving required notices. The repository itself has no LICENSE file; this is acceptable for the current private take-home repository but must be decided before public distribution.

## 8. Docker review

Static review passes:

- multi-stage build;
- builder pinned to `golang:1.26.10-bookworm`;
- Debian slim runtime;
- reproducible module download before source copy;
- stripped, static Linux binaries;
- non-root UID/GID 10001;
- only server, healthcheck, CA bundle, and migrations copied;
- TCP 8080 and UDP 8443 exposed;
- healthcheck and exec-form entrypoint;
- secret, state, database, log, archive, Git, Terraform cache, and certificate exclusions in `.dockerignore`;
- `docker compose -p collab-audit config --quiet` passes.

The real image build was attempted three times. Docker Hub's anonymous-token endpoint timed out over IPv6 before either base image was downloaded, so the build never reached a project layer. Result: **NOT VERIFIED due to external registry connectivity**, not a source/build failure. `go build ./...` and Linux-oriented CGO-free package compilation remain covered by the Go gate. Trivy was not installed, so no image CVE scan was claimed.

## 9. Terraform review

Providers are locked to Google 8.10.0 and Random 3.9.0; Terraform is constrained to `>= 1.8.0, < 2.0.0`. The validated plan contains 75 creates, 0 changes, and 0 destroys:

- 8 API enablements: Compute, Artifact Registry, Logging, Monitoring, Redis, Secret Manager, Service Networking, and Cloud SQL;
- 5 network foundations: custom VPC, subnet, router, Cloud NAT, private-service range/connection (the range and connection are separate resources, producing six Terraform addresses with router/NAT);
- 10 addresses: three private etcd, three private app, three public QUIC app, and one public REST load-balancer address;
- 3 persistent etcd disks and 6 Shielded VMs across three zones;
- 3 zonal instance groups plus regional TCP backend, health check, forwarding rule, and REST address;
- 5 firewall rules: public TCP 443, public UDP 8443, Google health-check ranges, subnet-only etcd/internal RPC, and IAP-only SSH;
- 1 regional HA private Cloud SQL instance, database, user, backups, PITR/binlog, encrypted-only transport, and deletion protection;
- 1 Standard HA private Memorystore instance with AUTH, TLS, and `noeviction`;
- 3 Secret Manager secrets and versions: MySQL password, Redis AUTH, and Redis CA;
- 2 service accounts, 6 narrow project roles, and 8 per-secret access bindings;
- 3 monitoring alert policies, one uptime check, and an optional email channel.

The only `0.0.0.0/0` rules expose the intended public REST 443/TCP and QUIC 8443/UDP ports. Cloud SQL and Redis have no public address. etcd has no external interface. SSH is limited to the Google IAP range. Internal ports accept only the application subnet and application protocols still require TLS/mTLS.

Executed with Terraform 1.17.1:

- `terraform fmt -recursive -check`: PASS
- `terraform init -backend=false`: PASS
- `terraform validate`: PASS
- `terraform plan -refresh=false`: PASS (`75 add, 0 change, 0 destroy`)

The plan was generated in a temporary backend-free copy with placeholder inputs. No Apply, Destroy, state mutation, or paid resource creation occurred. The production GCS backend must be configured with encryption, uniform access, versioning, retention, and narrow IAM because Terraform state contains generated secret values.

## 10. Real infrastructure and quality gate

An isolated environment was created only for this audit: MySQL 8.4.13, Redis 7.4.5 with DB 0–15, etcd 3.5.17 with mutual TLS, internal RPC CAs and node identities, real QUIC/TLS, and temporary SQLite stores. It used random localhost ports and credentials, a dedicated Docker network, ephemeral container filesystems, and no Docker volumes.

The following were executed without configuration-driven Skip:

- authentication, Session, refresh rotation/replay, rate limits;
- cross-node logout, operation lease, presence, Pub/Sub interruption and reconciliation;
- MySQL transactions, idempotency, rollback, permission changes, fencing, snapshots, and replay;
- etcd test lock, membership, compaction recovery, ownership, network partition, failover, and graceful release;
- mTLS permission forwarding with JWT and Redis revalidation;
- QUIC certificate verification, framing, message size, redirect, logout, RESUME/SYNC/RESYNC_REQUIRED;
- client reconnect and MySQL/Redis/QUIC/SQLite crash recovery;
- fresh and existing database migration behavior.

Final commands under Go 1.26.10:

| Gate | Result |
| --- | --- |
| `gofmt -l .` | PASS, no output |
| `go test -count=1 ./...` | PASS |
| `go test -count=1 -race ./...` | PASS |
| `go vet ./...` | PASS |
| `go build ./...` | PASS |
| `go mod verify` | PASS |
| `git diff --check` | PASS |
| `govulncheck ./...` | PASS, 0 reachable vulnerabilities |

The first Race run exposed a test-isolation flaw: a migration test compared whole-database row counts while other packages legitimately inserted unique fixtures into the shared isolated MySQL instance. The test now asserts that migration never decreases any table count and still verifies the seeded legacy document, permissions, operation, snapshot, migration versions, users table, and fencing columns exactly. The targeted Race test and full Race suite then passed.

## 11. Autonomous fixes

1. Upgraded the Go baseline and vulnerable dependencies; updated README and production builder version.
2. Removed secret values from VM metadata and narrowed Secret Manager IAM to individual secrets.
3. Added a dedicated Redis AUTH secret and safe runtime retrieval.
4. Fixed cross-package MySQL migration test isolation without weakening production assertions.
5. Merged a meaningless test-helper file into its owning test file.
6. Expanded Git and Docker ignore coverage for certificate and private-key formats.
7. Updated deployment documentation to match the secured secret flow.

## 12. Remaining risks

- LOW: Docker image build and image CVE scan must be rerun when Docker Hub connectivity is available. Static Docker review and Go build pass.
- LOW: Six module vulnerabilities are present only in unreachable code according to `govulncheck`; monitor future code-path and dependency changes.
- LOW: The private repository has no project LICENSE. Choose one before public distribution.
- Operational: production TLS certificates, GCS backend controls, Secret Manager payloads, budgets, and rollback rehearsal require deployment-time human review.

No CRITICAL, HIGH, or unresolved MEDIUM issue remains.

## 13. Final status

```text
PHASE 9.1 PRE-COMMIT AUDIT

GIT REVIEW: PASS
SECRET SCAN: PASS
DEPENDENCY REVIEW: PASS
LICENSE REVIEW: PASS
GO CODE REVIEW: PASS
DOCKER REVIEW: PASS (STATIC), NOT VERIFIED (REGISTRY-BLOCKED BUILD)
TERRAFORM REVIEW: PASS
SECURITY REVIEW: PASS
CONCURRENCY REVIEW: PASS
REAL INFRASTRUCTURE: PASS
RACE DETECTOR: PASS
GO VET: PASS
GO BUILD: PASS
FULL TEST SUITE: PASS
TERRAFORM VALIDATION: PASS

CRITICAL: 0
HIGH: 0
MEDIUM: 0
LOW: 3

WORKSPACE PRESERVED: YES
COMMIT: NO
PUSH: NO
TERRAFORM APPLY: NO

FINAL RESULT: READY FOR MANUAL COMMIT
```

The Docker registry limitation is documented and does not indicate a source failure. Before publishing a production image, rerun `docker build --pull` and an image scanner in a network environment that can reach Docker Hub.
