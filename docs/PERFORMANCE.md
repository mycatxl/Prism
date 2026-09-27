# Performance and capacity

This file records **measured** capacity figures for the in-process data structures
that bound Prism's node-pool scale. Everything here is reproducible with the
commands in §1; nothing is estimated or extrapolated from another deployment.

The plan (`docs/plan/13-testing-release-docs.md`) asked for a "100k node capacity
smoke". The measurement functions already existed in the tree; this file records
their results and the caveats that make them readable.

## 1. How to reproduce

```sh
make capacity        # runs every Test*Capacity* case with -v and prints the numbers
```

Equivalent to:

```sh
go test -tags "with_quic,with_grpc,with_utls,with_wireguard,with_gvisor,with_openvpn,with_openconnect,http2legacy" \
  -run Capacity -v -count=1 ./internal/platform/ ./internal/routing/ ./internal/topology/
```

The cases honour `testing.Short()`, so `go test -short` skips them and the normal
`make verify` stays fast.

## 2. What these figures are — and are not

**Are:** in-process microbenchmarks of the three data structures that scale with
the node count — the platform view (a per-platform routable node set), the routing
table (lease allocation and lookup), and the global node pool (bulk import and
iteration). They exercise real production code paths with real `node.NodeEntry`
values and a stub outbound builder.

**Are not:** end-to-end proxy throughput. No bytes are tunnelled, no sing-box
outbound is built, no socket is opened. A figure like "831k req/sec" describes
lease allocation and lookup, not how many client connections the proxy can carry —
that is bounded by the kernel, the upstream nodes and the sing-box runtime, none of
which these tests touch.

## 3. Measurement host

| | |
|---|---|
| CPU | AMD Ryzen 9 7900X (12 cores / 24 threads), 8 vCPU visible to WSL2 |
| RAM | 15.8 GiB total, ~14 GiB free |
| OS | WSL2 (Arch Linux) on Windows 11 |
| Go | go1.27.1 linux/amd64 |

**`GOMAXPROCS=2`.** The figures below were collected with `GOMAXPROCS=2` to keep the
development host's memory and CPU headroom intact. Throughput scales with the
worker count, so a machine left at the default `GOMAXPROCS` will report higher
numbers. Compare runs only when `GOMAXPROCS` matches.

**Memory readings are signed.** Each case takes a settled baseline
(`testutil.ReadMemStatsStable`, two `runtime.GC()` calls) before and after the
operation and reports two numbers:

- `Live` — net change in the live heap. **Negative is a legitimate result**: a
  platform rebuild replaces its view, so it frees more than it retains. Use this to
  answer "how much does this cost me at rest".
- `TotalAlloc` — cumulative bytes allocated during the operation, including garbage.
  Monotonic and GC-independent. Use this to answer "how much does this allocate".

Before this was fixed, the four capacity cases computed `m1.Alloc - m0.Alloc` on
`uint64`, which underflows whenever the second GC reclaims more than the operation
allocated and printed nonsense such as `Alloc=17592186044400.12MB`. See
`internal/testutil/memstats.go`.

## 4. Results (2026-09-27)

### 4.1 Platform view rebuild — `internal/platform`

`TestPlatformCapacity_FullRebuildLargePool`: builds a full platform view from a
node set, which is what a platform refresh does.

| Nodes | Time | Live | TotalAlloc | Throughput |
|---|---|---|---|---|
| 10,000 | 3.28 ms | −15.87 MB | 1.30 MB | 3,046,082 nodes/sec |
| 50,000 | 26.24 ms | −79.66 MB | 5.13 MB | 1,905,151 nodes/sec |
| 100,000 | 54.58 ms | −159.23 MB | 11.47 MB | 1,832,078 nodes/sec |

Rebuild cost is close to linear (~0.55 µs/node at 100k) and a full 100k rebuild
completes in ~55 ms, so a platform refresh is not a capacity concern.

`TestPlatformCapacity_NotifyDirtyThroughput` — incremental single-node updates:
1,000 updates in 1.14 ms → **877,245 updates/sec**, 1.139 µs per update.

`TestPlatformCapacity_ViewRangeThroughput` — full iteration of a 100k view:
156 µs → **639,402,542 nodes/sec** (a pointer-chasing scan; the number is large
because the work per node is a map walk, not a computation).

### 4.2 Routing — `internal/routing`

`TestRouterCapacity_ConcurrentRouteRequests`: concurrent lease allocation and
lookup, the hot path of the proxy request handler.

| Concurrency | Requests | Time | Throughput | Avg latency | Live | TotalAlloc |
|---|---|---|---|---|---|---|
| 10 | 10,000 | 12.03 ms | 831,385 req/sec | 1.202 µs | 1.82 MB | 4.59 MB |
| 100 | 50,000 | 60.30 ms | 829,177 req/sec | 1.206 µs | 6.01 MB | 19.37 MB |
| 1000 | 100,000 | 126.76 ms | 788,872 req/sec | 1.267 µs | 13.22 MB | 41.75 MB |

Throughput is flat from 10 to 1000 concurrent workers (a 5% dip at 1000), and
per-request latency stays at ~1.2 µs, so the routing table is not a bottleneck and
does not degrade under contention.

`TestRouterCapacity_LeaseManagement` — bulk lifecycle:

- Create 50,000 leases: 95.4 ms (**524,193/sec**)
- Lookup 10,000 leases: 628 µs (**15,936,001/sec**)
- Delete 10,000 leases: 1.28 ms (**7,805,914/sec**)

`TestRouterCapacity_IPLoadTracking` — 20,000 leases across 1,000 IPs created in
40.0 ms; a full load snapshot of all 1,000 IPs captured in 71.8 µs.

### 4.3 Node pool — `internal/topology`

`TestPoolCapacity_BulkImport`: registering nodes in the global pool, which is what
a large subscription import does.

| Nodes | Time | Live | TotalAlloc | Throughput | Live per node |
|---|---|---|---|---|---|
| 10,000 | 103.8 ms | 17.28 MB | 45.13 MB | 96,337 nodes/sec | 1.77 KiB |
| 50,000 | 525.1 ms | 86.77 MB | 227.31 MB | 95,215 nodes/sec | 1.78 KiB |
| 100,000 | 1,097 ms | 173.69 MB | 455.14 MB | 91,119 nodes/sec | **1.78 KiB** |

Import is linear at ~1.78 KiB of retained heap per node, so **100k nodes cost about
174 MB** of live heap in the pool itself. Throughput degrades only 5% from 10k to
100k (96.3k → 91.1k nodes/sec), so a 100k import finishes in ~1.1 s.

This per-node figure is the number to use when sizing `PRISM_*_MAX_NODES`. It is
the pool's own footprint: the intel subsystem, the sing-box outbounds built for
healthy nodes, and the platform views all add their own cost on top.

`TestPoolCapacity_LiveNodeOutboundCreation` — creating the outbound handle per
live node: 1,000 in 65 µs, 10,000 in 361 µs (the stub builder is used, so this
measures the pool's bookkeeping, not real sing-box construction).

`TestPoolCapacity_ConcurrentAccess` — iterating 50,000 nodes: 809 µs →
**61,768,430 nodes/sec**.

## 5. End-to-end proxy throughput (Docker, measured)

Section 4 measures in-process data structures. This section measures the **whole
request path** with a real sing-box outbound, which section 4 deliberately does
not touch.

### 5.1 Topology

Everything runs in Docker containers on one bridge network, so no request leaves
the host and the numbers are not bounded by an external network:

```
load container --HTTP proxy--> Prism container :2260
                                   |
                                   | sing-box "http" outbound (real runtime)
                                   v
                              node container :18081  (HTTP forward proxy)
                                   |
                                   v
                              target container :18080 (HTTP server)
```

Prism's egress probe points at a local Cloudflare-trace-shaped endpoint
(`PRISM_EGRESS_TRACE_URL`), so the node passes its probe and becomes routable
without outbound internet -- the same technique `scripts/smoke.sh` uses.

### 5.2 Setup

```sh
# 1. Build the image from the current HEAD.
docker build -t prism:verify .

# 2. A helper container provides the target (18080), the egress trace (19080)
#    and the node proxy (18081). See "Harness" below.

# 3. Prism, with the probe pointed at the local trace endpoint.
docker run -d --name prism-compose --network bridge \
  -e PRISM_ADMIN_TOKEN=<token> -e PRISM_PROXY_TOKEN=<token> \
  -e PRISM_LISTEN_ADDRESS=0.0.0.0 -e PRISM_PORT=2260 \
  -e PRISM_EGRESS_TRACE_URL=http://<helper-ip>:19080/cdn-cgi/trace \
  -p 2260:2260 \
  -v prism_cache:/var/cache/prism -v prism_state:/var/lib/prism -v prism_log:/var/log/prism \
  prism:verify

# 4. Register the node (the parser requires a literal IP for the bare host:port form).
curl -X POST .../api/v1/subscriptions \
  -d '{"name":"docker-e2e","source_type":"local","content":"<helper-ip>:18081"}'
curl -X POST .../api/v1/subscriptions/<id>/actions/refresh

# 5. Load, from a container on the same network (the WSL2 host cannot reach
#    container IPs directly, so the generator must run inside the network).
docker exec prism-load /loadgen -url http://<helper-ip>:18080/ \
  -proxy http://Default:<proxy-token>@<prism-ip>:2260 -c 32 -n 4000
```

### 5.3 Results (2026-09-27)

4,000 requests per run, 32 concurrent workers, one node. Every run below is
**0 failed requests**.

| Path | Throughput | p50 | p90 | p99 |
|---|---|---|---|---|
| A. Baseline: direct to target (no proxy) | 103,230 req/sec | 180 us | 684 us | 1.47 ms |
| B. Baseline: direct to the node proxy (no Prism) | 58,204 req/sec | 406 us | 1.19 ms | 2.11 ms |
| C. **Through Prism, HTTP forward proxy** | **39,010 req/sec** | 736 us | 1.31 ms | 2.10 ms |
| D. **Through Prism, SOCKS5** | **26,196 req/sec** | 593 us | 1.00 ms | 25.66 ms |
| E. **Through Prism, reverse-proxy path** | **30,987 req/sec** | 937 us | 1.63 ms | 2.70 ms |

**Reproducibility** (three consecutive runs of the same configuration):

| Round | A (direct) | B (node) | C (through Prism) |
|---|---|---|---|
| 1 | 106,365 | 55,266 | 35,666 |
| 2 | 120,766 | 60,484 | 35,381 |
| 3 | 100,772 | 59,503 | 36,654 |

Baselines move +-10% between rounds while path C stays within +-2%, so the Prism
figure is the stable one.

**Reading these numbers.** Path B is the ceiling: Prism cannot be faster than
talking to the node directly, because every request still traverses it. Prism
costs about **33% of that ceiling** at 32 workers (39.0k vs 58.2k); the remaining
budget goes to lease allocation, the sing-box outbound hop and the extra proxy
leg.

At 128 concurrent workers the same path reports 18,010 req/sec (p99 41 ms) against
a node-direct baseline of 41,344 req/sec, still with 0 failures -- so throughput
degrades under contention rather than dropping requests.

### 5.4 Harness

The load generator and the three helper endpoints are **not part of the
repository**; they are throwaway harnesses. Reproducing this section means
rebuilding them:

- **`loadgen`** -- a dependency-free Go HTTP load generator (concurrency, warmup,
  p50/p90/p99, status-code histogram), standard library only.
- **`helpers`** -- one Go binary serving three listeners: the HTTP target, the
  Cloudflare-trace-shaped egress endpoint, and an HTTP forward proxy playing the
  node role (absolute-form forwarding plus CONNECT).

Two harness gotchas that cost real time and are worth recording:

1. **A single-threaded target server becomes the bottleneck.** The first attempt
   used Python's `HTTPServer` (single-threaded); it serialised requests and
   reported a fake 788 req/sec with a 62 ms p50. Use a threading/async server, and
   always measure the direct baseline first: if the baseline is not fast, the
   measurement is about the target, not about Prism.
2. **The WSL2 host cannot reach container IPs.** The host is on `172.31.x` and the
   Docker bridge on `172.17.x`; a TCP connection to a container IP completes but no
   reply arrives (100% packet loss on ICMP). The load generator must therefore run
   *inside* the bridge network, not on the host.

### 5.5 TLS front-end overhead (2026-09-27)

Sections 5.1-5.3 measure the proxy data path with no TLS in front of it. This
subsection measures what TLS termination costs on the **control plane**, so the
numbers isolate the front end rather than node latency. Same Prism container (271
nodes), two nginx blocks in front of it, one Python HTTP/1.1 keep-alive client.

The body is `GET /api/v1/nodes?limit=1000`: **295,157 bytes**, chunked, over 300
keep-alive requests per entry point. Handshakes are measured separately, with a
fresh connection each time (median of 20).

| entry point | req/sec | MB/s | vs plain | handshake |
|---|---|---|---|---|
| plain HTTP, direct `127.0.0.1:9226` | 437 | 128.9 | -- | 0.02 ms |
| TLS, HTTP-level `proxy_pass` (nginx) | 344 | 101.6 | 78.9% | 1.25 ms |
| TLS, `stream` termination (nginx) | 363 | 107.3 | 83.2% | 1.18 ms |

Reading these honestly:

- The 17-21% gap is **TLS record processing over a large response**, not handshake
  cost. A fresh TLS connection costs **~1.2 ms** (median; observed range
  1.07-2.48 ms), which is small next to the node latency of a real proxied request.
- The `stream` block is slightly faster than the HTTP-level block because it does no
  HTTP parsing at all.
- Client, Prism and nginx all ran on the **same host**, so they competed for CPU.
  Treat the ratios as indicative, not as a capacity plan.
- The payload is unchanged by the front end: the exported `/sub/{token}` body is
  byte-identical between plain HTTP and the TLS edge (`cmp` clean), and the
  application surface answers the same status codes through both blocks.

One harness gotcha, recorded because it produced a wrong answer first: **Prism
answers with `Transfer-Encoding: chunked`, and this endpoint sends no
`Content-Length`.** A client that keys on `Content-Length` reads 0 bytes per
response; the first version of this benchmark did exactly that and reported a
plausible-looking table that was pure noise. Parse chunk sizes.

### 5.6 Multi-node load: P2C spread, per-protocol cost, steady-state memory (2026-09-27)

Sections 5.1-5.3 use a single loopback node. This subsection uses the **real
subscription**: 271 parsed nodes, **172 routable**, 155 with an observed egress
address. Traffic goes through the reverse-proxy path to `https://1.1.1.1/cdn-cgi/trace`
so every request leaves through a real exit node.

**P2C spread.** The request log records `node_hash` and `egress_ip` per request,
so the spread is read back from it rather than inferred. Over 5,000 logged
requests:

| metric | value |
|---|---|
| distinct nodes used | **162** (of 172 routable, 94%) |
| distinct egress IPs used | **158** |
| busiest node's share | **1.5%** |
| top-5 share | 7.0% |
| top-20 share | 24.8% |

No node carries a hotspot, and the selection reaches almost the whole routable
set — consistent with P2C picking between two candidates per request rather than
latching onto one fast node.

**Steady-state memory under load.** Two runs against the same instance, sampling
the container's cgroup v2 counters every 3-10 s. Peak load: 32 concurrent,
7,146 requests in 61.8 s (**116 req/sec**), of which 7,118 were `200` (99.6%);
22 `502` and 6 `504` were real upstream failures on a live node inventory.
Latency p50 213 ms / p90 306 ms / p99 1371 ms.

| measure | value |
|---|---|
| total cgroup memory, steady | **236-270 MB** |
| of which file-backed (SQLite page cache) | 92-155 MB |
| anonymous heap | **84-170 MB**, moving with the GC cycle |

The second run (16 concurrent, 180 s, 12,923 requests) is the one that answers
"does it converge": **anonymous memory did not move for 91 seconds** — 162.1 MB
flat — then stepped up by 7.7 MB in one sample and stayed flat again for the
remaining 50 s. That is Go's heap growth stepping to a new GC target, not a leak:
a leak grows monotonically, this is flat-then-step.

**Per-protocol first-byte cost.** Same target for every protocol
(`1.1.1.1:443`), grouped by the node's protocol, using
`first_byte_duration_ms` from the request log:

| protocol | n | median ms | p90 ms |
|---|---|---|---|
| trojan | 115 | 105 | 207 |
| vmess | 79 | 110 | 336 |
| hysteria2 | 873 | 196 | 269 |
| http | 3063 | 210 | 295 |
| socks | 93 | 212 | 721 |
| anytls | 29 | 215 | 226 |
| tuic | 39 | 229 | 237 |
| shadowsocks | 117 | 240 | 277 |
| vless | 501 | 244 | 338 |
| hysteria | 91 | 247 | 274 |

Plaintext protocols (`http`/`socks`) median **210 ms** against TLS-based
protocols median **230 ms** — a **+9.5%** difference, and the fastest entries are
TLS-based (trojan, vmess). Read this with its limits in mind: it is *not* a pure
handshake measurement (the target's own latency and each node's distance are
uncontrolled and vary by protocol), and QUIC is represented by few nodes
(hysteria2 873 requests, tuic 39, hysteria 91). The honest conclusion is narrow:
**a TLS or QUIC node hop is not categorically slower to first byte than a
plaintext one on this inventory.**

One environment note worth recording, because it looked alarming and was not:
96 of 271 nodes carried a `last_probe_error`, but only **one** was caused by the
host's fake-IP DNS (the container resolves public names into `198.18.0.0/15`,
which exists only inside a transparent-proxy TUN). The other 95 are the node
inventory failing on its own terms — `AUTH` 31, reality verification 22, `EOF` 18,
TLS certificate 14, `TIMEOUT` 2, HTTP 4xx 5. The latency probe resolves hostnames
**through the node**, so local DNS does not normally interfere; that is why 175
nodes probed successfully under a fake-IP resolver.

## 6. What is still not measured

Sections 5.5 and 5.6 close the three items that used to head this list (TLS in
front of Prism, P2C across a large routable set, steady-state memory under load).
What remains:

- **A pure node-side handshake cost.** Section 5.6 compares first-byte times by
  protocol across a live, uncontrolled inventory; it does not isolate the dial
  and handshake from the target and the node's distance. Separating them needs a
  controlled target per protocol.
- **Long-run stability.** Section 5.6 shows the heap is flat over 91 s of load,
  which rules out a fast leak but says nothing about behaviour over days. The
  scheduled cleanups (intel.db retention, request-log rotation, `VACUUM`) are
  exercised by unit tests, not by a long soak.
- **Behaviour past 100k nodes.** 100k is the largest case in the tree. The plan's
  target is 100k, so this is sufficient for the stated goal, but the linear
  extrapolation above 100k is not measured.
- **Import throughput from a real subscription file.** Section 4.3 registers
  pre-constructed entries; parsing a real subscription is measured separately in
  `internal/subscription` tests, not here.
