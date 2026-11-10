# Phase 11.2 QUIC Optimization Benchmark

## Evidence available

The existing real-Chrome benchmark contains 11,000 valid edit RTT samples for WebTransport single reliable stream and 11,000 for WebSocket across 1, 2, 10, 25, and 50 clients. Each level used five rounds and alternating transport order. See [Phase 11.1 Browser Transport Benchmark](phase-11.1-browser-transport-benchmark.md) for the full table and resource observations.

The new two-node acceptance adds functional evidence for WebTransport Owner Redirect, failover, fencing, recovery, cursor delivery, and continued editing. It is not a capacity benchmark.

## Conclusions

1. WebTransport was not faster at low concurrency in the loopback dataset; WebSocket was slightly faster at one and two clients.
2. WebTransport showed more consistent connection establishment at 25 and 50 clients in that run, while steady-state throughput remained similar.
3. Multi-Stream tail-latency improvement is **not measured** because Multi-Stream was not implemented without evidence of a stream bottleneck.
4. Datagram cursor improvement is **not measured** because controlled loss/reordering was unavailable.
5. Loss-condition advantage is **not measured**; the WAN fault matrix remains incomplete.
6. Browser QUIC connection migration is **not verified**; reconnect plus RESUME/SYNC must not be described as migration.
7. WebTransport remains an explicit preferred path when a valid HTTP/3 endpoint is configured, while WebSocket remains necessary for compatibility and operational fallback.

Performance benchmark status: **PARTIAL**. The loopback comparison is complete; network-fault, Multi-Stream, Datagram, and migration comparisons are incomplete.
