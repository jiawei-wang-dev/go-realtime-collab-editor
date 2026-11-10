# Phase 11.0 Transport Comparison

## Scope

This is a controlled local development comparison, not a production capacity claim. Measurements were collected on 2026-11-10 on one macOS host over loopback. Each sample creates a real transport connection, authenticates, joins a memory-backed Room, applies one edit, observes the acknowledgement, revokes the session, and closes. WebTransport uses UDP + TLS 1.3 + HTTP/3 Extended CONNECT; WebSocket uses local TCP HTTP upgrade. Server setup and Go compilation are outside each sample.

Commands:

```bash
go test -count=100 -v -run '^TestRealWebTransportUsesSharedRoomProtocol$' ./internal/transport/webtransport
go test -count=100 -v -run '^TestGatewayUsesRoomProtocolAndSessionRegistry$' ./internal/websocket
```

## Observed end-to-end sample latency

| Transport | Successful samples | Mean | p50 | p95 | p99 |
| --- | ---: | ---: | ---: | ---: | ---: |
| WebTransport | 99/100 | 1.795 ms | 1.593 ms | 2.723 ms | 5.911 ms |
| WebSocket | 100/100 | 0.500 ms | 0.461 ms | 0.723 ms | 1.830 ms |

One WebTransport repetition timed out during session establishment at the five-second test deadline. It is excluded from latency quantiles but retained in the success count. A subsequent normal count-one integration run passed. The repeated-start result means this dataset does not support a claim that WebTransport is faster or fully stable under rapid listener churn; WebSocket was faster in this specific loopback workload.

An earlier 30-run process-level sample, which includes package harness overhead, recorded 4.50 seconds wall time and 228,556,800-byte maximum resident set size for WebTransport versus 1.33 seconds and 200,998,912 bytes for WebSocket. Those figures are process-level observations, not per-connection service memory.

## Incomplete measurements

The following requested measurements were not established by this local protocol test:

- real browser connection establishment across Chromium, Firefox, and native Safari;
- isolated steady-state message RTT versus edit propagation latency;
- sustained throughput and concurrent browser connection capacity;
- server CPU and memory under an identical long-running load;
- controlled packet-loss behavior and QUIC connection migration;
- reconnect time after an actual network interruption;
- large-document synchronization under both browser transports.

These require a trusted HTTPS/HTTP3 browser origin, network shaping, stable long-running servers, and repeatable browser workers. They must be run before the performance comparison can be marked complete. The present evidence validates protocol integration and provides a local baseline only.

## Conclusion

WebTransport adds the requested browser QUIC/HTTP3 path and future stream/datagram flexibility, but this measurement does not show a speed advantage. WebSocket remains a useful compatibility transport. Performance comparison status: **INCOMPLETE**.
