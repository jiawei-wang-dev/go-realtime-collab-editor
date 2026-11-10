# Phase 11.2 QUIC Stream Design Audit

## Current design

Each browser WebTransport session accepts one client-created bidirectional stream. A four-byte length prefix frames JSON protocol messages. AUTH, JOIN/RESUME, EDIT, acknowledgements, errors, SYNC, redirects, presence, and cursor updates therefore share one reliable ordered stream. WebSocket uses the same transport-independent browser session core.

The shared core performs JWT and Redis session validation, document selection, owner resolution, redirect handling, Room access, permission checks, and revocation. All document mutations still enter the Room event queue, and MySQL Fencing Token checks remain below that boundary.

## Multi-stream assessment

A separate control stream and edit stream could isolate head-of-line blocking within the application stream, but it would remove the current global ordering between AUTH, JOIN, REDIRECT, RESUME, SYNC, EDIT, and their acknowledgements. Restoring those dependencies would require an explicit stream negotiation protocol, per-stream lifecycle limits, cross-stream sequencing, and more complex recovery behavior.

The existing real-browser dataset shows comparable WebSocket and WebTransport steady-state throughput and does not identify stream-level blocking as a bottleneck. The distributed failover run also completed over the single-stream design. There is therefore no measured justification for a protocol split in this phase.

Decision: **Multi-Stream NOT IMPLEMENTED / NOT YET BENEFICIAL**. The reliable single-stream protocol remains authoritative.

## Datagram assessment

`webtransport-go` 0.13.0 and the browser WebTransport API expose datagram operations. Cursor and selection state are conceptually suitable because newer positions supersede older positions, and the existing protocol already includes a monotonically increasing sequence number. However, a safe implementation would also need authenticated session binding, document and permission checks, version/TTL validation, maximum size limits, out-of-order rejection, redirect/failover behavior, and UI loss testing.

No controlled loss environment was available for an evidence-based reliable-stream versus datagram comparison. Moving cursor traffic now would add a second delivery path without demonstrated user-visible benefit.

Decision: **Datagram NOT IMPLEMENTED / EVIDENCE INSUFFICIENT**. Edits, authentication, acknowledgements, permissions, resume, snapshots, ownership, and fencing must always remain reliable.

## Connection migration

quic-go contains connection-ID and path-management support, but the browser WebTransport API does not expose QUIC connection IDs or a portable way to force NAT rebinding while proving that the original session survived. The successful failover behavior in this project is reconnect plus RESUME/SYNC, not proof of QUIC connection migration.

Decision: **Browser connection migration NOT VERIFIED**. It must not be advertised as supported until an observable path-change test proves that the original WebTransport session remains established without a new handshake or JOIN.
