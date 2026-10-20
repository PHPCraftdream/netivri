# Data-path baseline

Measured by the `bench` CI job (`workflow_dispatch`, `tags=bench`,
`benchtime=2s`, `os=ubuntu`) on the commit `cb5108a`: GitHub-hosted
`ubuntu-latest` runner, 4 vCPU (AMD EPYC 9V45), Go from `go.mod`, loopback
only (an echo server and a fake SOCKS5 upstream, no external traffic). The
harness is described in [testing.md](../testing.md); the numbers are one run
on a shared runner and only good for orders of magnitude and for comparing
two runs of the same job.

## Connect path

One iteration = a new SOCKS5 client connection to netivri (handshake with
credentials, one echoed byte, close). "Direct" has no route; "routed" is one
account with a sticky group over two fake SOCKS5 upstreams, so it includes the
upstream hop (its own handshake and relay) and the account lookup.

| Benchmark | ns/op | p50 | p99 | B/op | allocs/op |
|---|---|---|---|---|---|
| `ConnectDirect` | 210 926 | 205 249 | 296 606 | 11 343 | 169 |
| `ConnectRouted` | 366 549 | 361 628 | 455 145 | 14 863 | 246 |
| `ConnectRoutedParallel` (4 workers) | 140 202 | | | 14 869 | 246 |
| `ParallelConnects1000` (1 000 parallel connects per op) | 155 107 482 | | | 15 242 137 | 248 387 |
| `ThroughputRouted` (32 MiB each way, 64 KiB writes) | 3 342 MB/s | | | | |

Reading: the routed connect costs about 0.16 ms and 77 allocations more than
the direct one; 1 000 parallel connects take about 155 ms on 4 vCPU; the data
path copies at several GB/s on loopback, so netivri does not limit
throughput.

## Decision paths

| Benchmark | ns/op | B/op | allocs/op |
|---|---|---|---|
| `StickyChooseHit` (binding found) | 87.8 | 0 | 0 |
| `StickyChooseHitParallel` (4 workers, wall ns per op) | 40.9 | 0 | 0 |
| `StickyChooseMiss/members-2` (new site) | 1 193 | 165 | 4 |
| `StickyChooseMiss/members-20` | 2 789 | 1 313 | 6 |
| `SiteKey/subdomain` | 148.8 | 96 | 2 |
| `SiteKey/multi-label-suffix` | 154.6 | 96 | 2 |
| `SiteKey/ip-literal` | 34.6 | 16 | 1 |
| `HealthRecordDial` | 40.5 | 0 | 0 |
| `WarmNodeProbe/without-pool` | 4 579 208 | 25 356 | 215 |
| `WarmNodeProbe/with-pool` | 3 515 441 | 26 320 | 227 |

## Statistics store

| Benchmark | ns/op | notes |
|---|---|---|
| `Record` | 24.6 | the producer floods a bounded queue; nearly all samples are counted as dropped, which is the designed non-blocking path |
| `RecordParallel` | 47.8 | same |
| `FlushBatch` | 2 614 954 | one batch of 256 samples to SQLite, nothing dropped |
| `NodesQuery` | 277 619 686 | one `Nodes` query over 100 000 recorded samples |

## What the baseline says

- The cost of netivri's own decisions per connection (site key, binding
  lookup, health bookkeeping, statistics recording) is about 0.3 microseconds
  in total and allocation-free on the hit path. Against 0.14-0.37 ms per
  loopback connect and tens or hundreds of milliseconds over a real network
  it is below any measurable effect. Lock-free rewrites of these paths would
  save nanoseconds and are not justified by this baseline (see
  [latency.md](../design/latency.md), "Gates and acceptance").
- Per-connect allocations (246) come mostly from Mihomo's connect path, not
  from netivri's decision code.
- Network time dominates: a probe of one real node measured 619 ms with a
  5-10 ms TCP connect to its first hop (see latency.md, "Evidence").
