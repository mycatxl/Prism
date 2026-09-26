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

## 5. What is still not measured

- **End-to-end proxy throughput and concurrent connection capacity.** Needs real
  sing-box outbounds, real upstream nodes and a load generator. Not covered here.
- **Steady-state memory under a live workload.** These cases measure the
  structures; a running Prism also holds intel.db (SQLite page cache), request
  logs, and one sing-box outbound per healthy node.
- **Behaviour past 100k nodes.** 100k is the largest case in the tree. The plan's
  target is 100k, so this is sufficient for the stated goal, but the linear
  extrapolation above 100k is not measured.
- **Import throughput from a real subscription file.** §4.3 registers
  pre-constructed entries; parsing a real subscription is measured separately in
  `internal/subscription` tests, not here.
