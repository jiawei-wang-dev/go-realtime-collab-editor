# Phase 11.1 Real-Browser Transport Benchmark

## Method

This is a controlled loopback development benchmark, not a production capacity claim. System Google Chrome 157 drove the real browser WebTransport and WebSocket APIs against the same long-running Go server, JWT authentication, Room/OT path, MySQL database and document message shape. Each client authenticated, joined a unique document, performed three warm-up edits and then 25 measured sequential edits. Transport order alternated each round.

Each concurrency level ran five rounds per transport. The dataset contains 11,000 valid RTT samples per transport: 125 at one client, 250 at two, 1,250 at ten, 3,125 at 25 and 6,250 at 50. There were no failed edits or timeouts. Edit propagation is measured at the observer connection; in this single-user-per-session benchmark it tracks acknowledgement RTT closely.

## Results

| Transport | Clients | Samples | RTT p50 | RTT p95 | RTT p99 | Connection p50 | Connection p95 | Throughput |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| WebTransport | 1 | 125 | 4.60 ms | 7.60 ms | 10.20 ms | 11.20 ms | 46.50 ms | 203.58 edits/s |
| WebSocket | 1 | 125 | 4.30 ms | 7.10 ms | 9.00 ms | 13.10 ms | 15.10 ms | 216.56 edits/s |
| WebTransport | 2 | 250 | 8.50 ms | 13.90 ms | 20.70 ms | 11.90 ms | 22.20 ms | 225.39 edits/s |
| WebSocket | 2 | 250 | 7.60 ms | 11.40 ms | 13.10 ms | 13.80 ms | 17.30 ms | 253.22 edits/s |
| WebTransport | 10 | 1,250 | 37.90 ms | 50.30 ms | 58.90 ms | 40.10 ms | 75.80 ms | 253.70 edits/s |
| WebSocket | 10 | 1,250 | 38.10 ms | 47.10 ms | 81.20 ms | 50.20 ms | 108.20 ms | 250.27 edits/s |
| WebTransport | 25 | 3,125 | 103.00 ms | 144.90 ms | 178.60 ms | 104.70 ms | 198.00 ms | 231.11 edits/s |
| WebSocket | 25 | 3,125 | 106.20 ms | 138.80 ms | 174.50 ms | 175.70 ms | 4,315.10 ms | 226.89 edits/s |
| WebTransport | 50 | 6,250 | 237.10 ms | 286.90 ms | 327.00 ms | 207.80 ms | 397.20 ms | 206.71 edits/s |
| WebSocket | 50 | 6,250 | 232.30 ms | 276.20 ms | 317.10 ms | 2,012.00 ms | 4,727.40 ms | 211.10 edits/s |

The peak resource observation during a separate 50-client round was 67.5% CPU and 50,960 KiB RSS for the Go server, and 123.5% aggregate CPU with 966,016 KiB aggregate RSS for the Chrome process tree. That run recorded 1,250 successful samples per transport; WebTransport throughput was 196.79 edits/s and WebSocket throughput was 192.27 edits/s. Browser JavaScript heap maxima in the five-round primary dataset were approximately 109.4 MB for WebTransport and 82.1 MB for WebSocket, but heap values include accumulated benchmark-page state and are not a per-connection allocation measurement.

## Interpretation

WebSocket was slightly faster at one and two clients and had slightly better 50-client steady-state RTT. WebTransport had substantially more predictable connection establishment at 25 and 50 concurrent clients in this run and a lower 10-client p99, while steady-state throughput was similar. The evidence does not justify a blanket claim that QUIC is faster.

The current browser WebTransport adapter uses one reliable bidirectional stream. It therefore does not yet exercise independent stream multiplexing, datagrams or connection migration. No safe isolated packet-loss shaper was available on this macOS host, and global host networking was deliberately left untouched. The 30 ms/0%, 80 ms/1% and 80 ms/3% matrices are **NOT VERIFIED**. No claims about QUIC loss recovery or network migration are made.

## Recommendation

Keep `auto` opt-in through an explicit HTTPS WebTransport endpoint and retain WebSocket as the compatibility path. WebTransport is operational and shows better high-concurrency connection-time consistency in this local test, but making it the unconditional browser default should wait for real WAN loss testing, native Firefox/Safari acceptance and a two-node browser failover run.
