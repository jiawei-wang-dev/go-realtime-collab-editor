# Phase 11.2 QUIC Optimization Report

## Outcome

Phase 11.2 completed the requested architecture audit but did not change the wire protocol. The project retains:

- native QUIC for the recoverable CLI;
- WebTransport over HTTP/3 for supported browsers with an explicitly configured endpoint;
- WebSocket as the compatibility transport;
- one reliable bidirectional WebTransport stream per browser collaboration session;
- the shared browser session, Room, OT, permission, ownership, fencing, and persistence path.

## Why no speculative optimization was merged

The current 22,000-sample browser dataset does not show a universal latency advantage for WebTransport, and no measurement identifies single-stream head-of-line blocking as the limiting factor. Multi-stream would add cross-stream ordering hazards around redirect and recovery. Datagram could be appropriate for cursor state but could not be evaluated under controlled loss on this host. Browser-level connection migration could not be distinguished from the existing reconnect state machine.

The lowest-risk optimization was therefore operational: add deterministic two-node WebTransport acceptance, precise owner-redirect logging, correct IPv4 endpoint publication, and explicit takeover synchronization. These changes improve diagnosability and repeatability without weakening ordering or consistency.

## Remaining work before protocol changes

1. Run the TCP and UDP transports through the same isolated Linux traffic-control path for at least five alternating rounds per condition.
2. Instrument per-message queue delay and stream write blocking.
3. Prototype datagram cursor delivery behind an explicit experimental flag and compare loss, reordering, CPU, UI flicker, and tail latency.
4. Implement multi-stream only if the instrumentation shows a material tail-latency problem attributable to the single stream.
5. Test connection migration in an environment where NAT rebinding and handshake/connection-ID telemetry are observable.

Status: **audit complete; Multi-Stream and Datagram not implemented; browser migration not verified**.
