# Phase 11.1 Network Fault Benchmark

## Status

**NOT VERIFIED** on the current host.

## Finalization attempt (2026-11-11)

Docker Desktop's existing `desktop-linux` VM was selected as the only available local Linux virtualization environment. An AMD64 Debian probe ran successfully under emulation (`x86_64`), proving that an AMD64 Google Chrome container could in principle run on the ARM64 host. The planned topology used isolated, labelled client and server networks with `tc netem` applied only to their test interfaces; existing containers, networks, and volumes would not be touched.

The initial Selenium Google Chrome image extraction failed with `commit failed: input/output error` while the macOS Data volume was at 99–100% capacity. After an explicitly authorized cache cleanup, free space increased to approximately 25 GB. Docker Desktop was restarted without resetting its data; the existing project containers and all 154 volumes remained present, and the Chrome image then downloaded successfully at digest `sha256:3fa985d1a73d1a3ae43c758d546b505b9d73e31ecaffd7a77725f19d1a70edec`.

The Docker Linux VM is ARM64. A normal AMD64 Debian probe succeeded under emulation, but the real Google Chrome AMD64 executable terminated with exit 135 (`SIGBUS`) even for `--version` and a minimal headless page. Linux Google Chrome is not distributed for ARM64. Substituting an ARM64 Chromium build would violate the requirement for real Chrome, so the matrix was not run with a different browser. The task-created unusable Chrome image was removed by its exact tag; no unrelated image, container, network, or volume was removed.

Consequently, real Linux Chrome, TLS WebSocket, HTTP/3 WebTransport, and the four-condition `tc netem` matrix were not executed in this attempt. No partial measurements are presented as benchmark data.

The required matrix is baseline 0 ms/0%, normal 30 ms/0%, degraded 80 ms/1%, and poor 80 ms/3%, with WebSocket/TCP and WebTransport/UDP using the same fault boundary, workload, message size, concurrency, warm-up, and at least five alternating measured rounds.

The available host is Apple Silicon macOS with an ARM64 Docker Desktop VM. Applying `pf`, Network Link Conditioner, or another host-wide rule would violate the task's safety boundary. Running a browser inside the Docker Linux VM would provide a fair container-network boundary, but real Google Chrome cannot run reliably through the available AMD64 emulation. A TCP-only proxy would also make the comparison invalid because it cannot carry QUIC UDP.

No latency, loss, failure-rate, timeout, reconnect, or propagation figures are reported for this matrix. The existing loopback results remain in [the browser transport benchmark](phase-11.1-browser-transport-benchmark.md) and must not be interpreted as WAN-loss results.

## Reproducible next environment

Use a native x86_64 Ubuntu 24.04 VM (or an x86_64 host) with dedicated Linux network namespaces, a common bridge, and `tc netem` on the shared client-facing interface. Run Google Chrome inside that environment, verify applied qdisc counters and measured RTT/loss, alternate the transport order, retain failed samples, and remove only the namespace/qdisc created by the test.
