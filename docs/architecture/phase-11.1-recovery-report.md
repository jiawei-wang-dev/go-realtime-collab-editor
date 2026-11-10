# Phase 11.1 Recovery Report

Date: 2026-11-11

## Scope and environment

The recovery run used system Google Chrome 157.0.8103.38, two independent browser contexts, two Go nodes, and isolated MySQL 8.4.13, Redis 7.4.5, and etcd 3.5.17 containers. WebTransport used real HTTP/3 over UDP with a short-lived P-256 certificate pinned through the standard `serverCertificateHashes` API. Test credentials, certificates, containers, and networks were unique to the run and removed afterward.

## Verified distributed path

The browser connected to a non-owner seed, received `owner_redirect`, and established a WebTransport session with the authoritative owner. Two users then joined the same document, observed remote cursor state, and completed concurrent edits through the shared Room/OT path.

The active owner was terminated with `SIGKILL`. The clients entered reconnecting state, an edit remained pending, the surviving node acquired a higher etcd ownership revision, activated the higher MySQL Fencing Token, recovered the Room, and drained the pending edit without changing its request identity. Both users continued editing after recovery. Restarting the old node with a new incarnation restored cluster membership but did not transfer ownership away from the surviving owner.

## Corrections made during recovery

- Browser endpoints now use the same IPv4 address family as the UDP listener; `localhost` had resolved to `::1` while the listener was bound to `127.0.0.1`.
- The browser-pinned certificate is generated with OpenSSL 3 using P-256 and a validity period below the WebTransport certificate-hash limit.
- Ownership takeover waits for a changed node identity and a strictly higher Fencing Token instead of merely observing the old `ACTIVE` record.
- Restarted test nodes use the repository root as their working directory so migrations remain available.
- Structured logs record accepted WebTransport sessions and owner redirects without logging credentials.

## Result

| Gate | Result |
|---|---|
| Real Chrome / HTTP/3 / WebTransport | PASS |
| Two-node collaboration | PASS |
| Owner Redirect | PASS |
| Owner failover and Room recovery | PASS |
| Fencing Token increase | PASS |
| Pending edit recovery | PASS |
| Cursor recovery | PASS |
| Old-node restart safety | PASS |
| Isolated resource cleanup | PASS |
| Controlled WAN delay/loss matrix | NOT VERIFIED |

Phase 11.1 remains **INCOMPLETE** because the required fair TCP/UDP network-fault matrix could not be applied safely on this macOS host without changing host-wide network policy. The distributed functional acceptance itself passed.
