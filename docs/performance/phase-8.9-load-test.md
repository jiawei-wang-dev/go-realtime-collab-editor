# Phase 8.9 Multi-node Load and Failure Test

Date: 2026-11-06 (Australia/Melbourne)

## Scope and environment

The run used isolated task-owned resources only:

- macOS 26.5, Apple M4, 10 logical CPUs, 16 GiB RAM
- Go 1.24.10 at the time of the Phase 8.9 run (the Phase 9.1 security audit later raised the project baseline to Go 1.26)
- MySQL 8.4.13
- Redis 7.4.5
- etcd 3.5.17 with client-certificate TLS
- real QUIC/TLS 1.3 and internal mTLS
- SQLite client state in temporary profiles

No existing Docker volume was mounted. Results are development-machine burst
measurements, not production capacity claims.

## Load tool

`cmd/loadtest` creates isolated users and documents through REST, assigns writer
permissions, connects real QUIC clients, follows authenticated Owner Redirects,
and issues concurrent stale-base inserts. It reports operations, failures,
redirects, RPS, P50/P95/P99 latency, client heap, and goroutine count.

Required settings are `LOAD_NODES`, `LOAD_API_URL`, and `LOAD_CA_FILE`. Controlled
load settings are `LOAD_CLIENTS`, `LOAD_DOCUMENTS`, `LOAD_OPERATIONS`,
`LOAD_DURATION`, and `LOAD_RAMP_UP`.

## Measured results

| Topology | Clients | Documents | Applied edits | Errors | Redirects | RPS | P50 | P95 | P99 | Client heap | Client goroutines |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| 1 node | 4 | 2 | 40 | 0 | 0 | 241.3 | 15.0 ms | 24.6 ms | 25.3 ms | 1.91 MB | 21 |
| 3 nodes | 9 | 6 | 135 | 0 | 9 | 491.5 | 15.5 ms | 29.9 ms | 41.3 ms | 2.19 MB | 46 |
| 5 nodes | 15 | 10 | 300 | 0 | 13 | 660.0 | 19.5 ms | 29.8 ms | 47.3 ms | 2.55 MB | 76 |

The five server processes used approximately 28-33 MiB RSS each after the run.
The sampled idle CPU value was 0%; this run did not collect a high-frequency CPU
profile, so no peak CPU claim is made.

## Data consistency

MySQL was queried after each run, rather than relying only on client responses.
For every completed document:

- `documents.version` equaled the number of applied operations.
- document content length equaled the number of one-byte inserts.
- operation versions were contiguous from 1 through the document version.
- operation count equaled distinct `request_id` count.
- each document had one persisted `owner_node_id` and fencing token.
- documents were distributed across real Owner nodes in the 3/5-node runs.

The five-node run produced documents at versions 20 and 40 as expected from the
client-to-document distribution. No duplicate operation or missing version was
observed.

## Fault injection matrix

All rows used real dependencies where the component is named.

| Scenario | Result | Validation |
| --- | --- | --- |
| Owner process/lease loss | PASS | New Owner recovered Room; client reconnected and resumed |
| Owner-etcd partition | PASS | Old Room self-fenced; restarted node revalidated ownership |
| MySQL pause/unpause | PASS | Readiness 503; authentication failed closed in 5 s; recovery restored original content/version |
| Redis unavailable | PASS | Session store failed closed |
| Redis Pub/Sub disconnect | PASS | Subscriber reconnected; reconciliation found revocation; Presence expired |
| etcd unavailable | PASS | Registry startup failed closed |
| Node re-registration | PASS | Membership and lease expiry tests removed old process identity |
| Owner fails during recovery | PASS | Third node acquired a higher token and completed recovery |
| Concurrent ownership attempts | PASS | etcd transaction admitted one winner |
| Old Owner after takeover | PASS | MySQL fencing rejected edit, permission and snapshot writes |
| Cross-node logout | PASS | Remote QUIC connection closed; revoked session could not edit |
| Snapshot failure | PASS | History retained, retry succeeded, replay recovered state |
| Client crash/Pending recovery | PASS | SQLite retained original `request_id`; no duplicate edit |
| Concurrent reconnect wakeups | PASS | One reconnect loop; Pending sequence preserved under Race Detector |

Observed automatic failover/reconnect scenarios completed in roughly 2-5
seconds with the test lease/backoff settings. This is configuration-specific and
is not an SLA.

## Issue found under load

The first real three-node run found that the server wrote `owner_redirect` and
immediately closed the whole QUIC connection. Under load, the connection close
could race delivery of the ordered Stream frame. The server now closes the
sending side of the Stream after a successful redirect write and lets the client
close the connection. Redirect and client tests passed repeatedly with the Race
Detector after the fix.

MySQL pause testing also found that REST authentication had no independent query
deadline. A five-second authentication boundary now returns a deterministic
fail-closed response instead of leaving a Handler waiting on a half-open database
connection.

## Stability and limits

- QUIC connections returned to zero after clients exited.
- Room counts matched active ownership and idle cleanup behavior.
- Redis Operation Leases and Presence keys expired/released in integration tests.
- etcd leases removed stopped process identities.
- Race tests found no data race in Room, QUIC, Client, Ownership, Session, or
  Pending recovery paths.

This was a conservative burst test, not a soak test. It does not establish peak
production capacity, long-duration memory stability, cross-region latency, or
cloud-network packet-loss tolerance. Production sizing requires sustained tests
on the target instance type with representative document sizes and edit mixes.
