# Phase 8.8 Observability

This service exposes structured logs, Prometheus metrics, health probes, and
OpenTelemetry traces without changing the collaboration protocol. The management
listener is separate from the public REST and QUIC listeners.

## Structured logging

Logs use JSON through `log/slog`. Every record has `timestamp`, `level`,
`service`, `node_id`, `incarnation`, `component`, and `event` when the event is
known. A valid tracing context also adds `trace_id`; request-scoped code may add
`request_id`.

Important events include node registration/readiness, ownership acquisition,
release and loss, self-fencing, owner redirects, Room recovery, snapshot
failure, session revocation, Redis Pub/Sub reconnection, presence updates, QUIC
reconnection, and Room closure.

The logging handler recursively redacts keys containing authorization,
password, secret, token, private key, DSN, refresh-token, or document-content
terms. Code must never log raw JWTs, refresh tokens, Redis/MySQL credentials,
private keys, Authorization headers, or full document content.

## Management listener and health

`ADMIN_ADDR` defaults to `127.0.0.1:9090`. It must contain an explicit loopback
or private address; wildcard and public bindings are rejected. In production,
place this listener on a protected management network and restrict it with the
platform firewall.

Endpoints:

- `GET /livez`: process liveness. Dependency outages do not make liveness fail.
- `GET /readyz`: traffic safety. MySQL, Redis, etcd and local Membership state
  are checked with bounded timeouts when configured.
- `GET /metrics`: Prometheus exposition endpoint.

The node is not ready during startup or shutdown. Loss of the etcd lease makes
Membership unready and triggers the existing fail-closed service lifecycle.

## Metrics

All labels have bounded values. User IDs, document IDs, session IDs and request
IDs are deliberately excluded.

| Area | Metric | Meaning |
| --- | --- | --- |
| Cluster | `collab_membership_nodes` | Nodes visible in Membership |
| Cluster | `collab_node_ready` | Local readiness state |
| Cluster | `collab_etcd_lease_renewal_failures_total` | Lease confirmation failures |
| Cluster | `collab_ownership_attempts_total{result}` | Ownership success/conflict/failure |
| Room | `collab_rooms_active` | Active Room goroutines |
| Room | `collab_room_clients_active` | Joined clients |
| Room | `collab_room_event_queue_depth` | Aggregate queued Room events |
| Room | `collab_room_slow_clients_total` | Slow-client disconnects |
| Room | `collab_snapshot_operations_total{result}` | Snapshot results |
| Room | `collab_recovery_duration_seconds` | Room recovery duration |
| QUIC | `collab_quic_connections_active` | Current connections |
| QUIC | `collab_quic_connections_total` | Accepted connections |
| QUIC | `collab_quic_events_total{event,result}` | Accept, redirect and protocol events |
| Redis | `collab_redis_operation_duration_seconds{operation}` | Redis latency |
| Redis | `collab_redis_events_total{event,result}` | Revocation, presence and Pub/Sub events |
| MySQL | `collab_mysql_operation_duration_seconds{operation}` | Transaction/query latency |
| MySQL | `collab_mysql_failures_total{type}` | Fencing, version and other failures |
| MySQL | `collab_mysql_pool_connections{state}` | Max/open/in-use/idle pool state |
| HTTP | `collab_http_request_duration_seconds{scope,method,status}` | Public/internal RPC latency |

Suggested alerts include sustained readiness failure, any lease renewal failure,
increasing fencing rejection, repeated recovery failure, snapshot failure,
slow-client spikes, Redis Pub/Sub reconnect loops, exhausted MySQL pool, and
elevated REST/internal RPC error latency.

## Tracing

W3C Trace Context and Baggage propagators are installed globally. REST and
internal mTLS RPC use `otelhttp`; internal RPC injects W3C headers. QUIC
connection handling, Room edits/recovery/snapshots, selected Redis operations,
and MySQL edit/permission/snapshot/recovery transactions create spans.

With no `OTEL_EXPORTER_OTLP_ENDPOINT`, tracing uses a no-op sampler and requires
no external service. To export over OTLP/HTTP, set an endpoint URL. Production
configuration rejects an insecure exporter; `OTEL_EXPORTER_OTLP_INSECURE=true`
is intended only for an isolated development network.

## Local use

Start the server normally, then inspect:

```bash
curl -fsS http://127.0.0.1:9090/livez
curl -fsS http://127.0.0.1:9090/readyz
curl -fsS http://127.0.0.1:9090/metrics
```

Minimal Prometheus scrape configuration:

```yaml
scrape_configs:
  - job_name: collab-editor
    static_configs:
      - targets: [127.0.0.1:9090]
```

Do not expose the management address on the public Internet.
