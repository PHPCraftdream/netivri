# Connection latency: evidence, contracts and gates

Status: contract for the implementation. Related: [node statistics](node-stats.md),
[warm-up](warmup.md), [accounts](accounts.md), [testing](../testing.md).

## Goals

1. Cut the time a new proxied connection needs before its first byte, and
   remove the multi-second stalls on a small share of connections.
2. Keep the per-connection cost of netivri itself flat when accounts,
   connections and nodes grow (no locks shared by all connections, no work
   proportional to the number of accounts).
3. Decide every change by a number (CI benchmark or `netivri diag dial`),
   not by assumption.

Non-goals: choosing exit countries or nodes for the user (policy), stream
multiplexing (needs server support and does not combine with `xtls-rprx-vision`),
active bandwidth tests or any synthetic load on the user's links, pre-opening
connections to nodes that carry no traffic, new configuration modes unless a
measurement requires one.

## Evidence

Observed, read-only, on one user machine (the aggregate numbers only):

- The installed build predates the sticky group and the parallel probes.
- The account route has one node: VLESS over TCP with Reality and
  `xtls-rprx-vision`. The warm-up probe (`http://www.gstatic.com/generate_204`,
  a fresh connection every time) measured 619 ms; 11 of 187 probes (5.9 %)
  failed at the 5 s probe timeout.
- A plain TCP connect to the first hop of every node of the subscription took
  5-10 ms (the first sample of each node 18-62 ms). Of 22 nodes, 19 are
  VLESS/TCP/Reality/Vision and 3 are VLESS/gRPC/Reality.
- The pre-dial pool reports 0 hits: it exists for the default node only.

Inferred, not measured: of the 619 ms about 600 ms are spent after the TCP
connect. The split between the TLS/Reality handshake, the VLESS request, the
exit's own connect to the target and the first response is unknown. The 5.9 %
failures may hang in the transport handshake (fixable by a hedged dial) or
after the VLESS request (not fixable by hedging). Whether the user's
complaint is connect latency, tail stalls or throughput is also unknown.

## Mihomo v1.19.32 facts

Verified in the module sources:

- `adapter/outbound/vless.go`: `DialContext` = `dialContext` (TCP via
  `v.dialer`, or the gun/xhttp client) then `StreamConnContext`:
  `streamTLSConn` (TLS or Reality) then `streamConnContext` (optional
  encryption handshake, then `client.StreamConn`).
- `transport/vless/conn.go`: the VLESS request header is written lazily with
  the first `Write` (`sent`, `NeedHandshake`); the Vision wrapper
  (`vision.NewConn`) is created in `newConn` and type-asserts the TLS
  connection, so a pooled transport must be the unwrapped TLS connection.
- `adapter/outbound/trojan.go` writes its header inside `StreamConnContext`
  (eager). A cancelled attempt can leave an empty connection at the target.
- `tunnel/tunnel.go` dials under one `C.DefaultTCPTimeout` (5 s) context and
  retries (up to 10 times, with slow-down) only after an error. A hanging
  handshake therefore costs the whole 5 s.
- `component/dialer`: `tcp-concurrent` is off (serial dial over resolved
  addresses); DNS is disabled in netivri's config, so the system resolver
  (with its own cache) is used.
- gRPC nodes use one HTTP/2 transport per node: new connections are new
  streams, no new handshake. Mihomo already does this.
- netivri emits one `IN-USER,<login>,<group>` rule per account
  (`internal/core/accounts.go`): rule matching is O(accounts) per connection.

## Where the time goes and what each change can save

Time to first byte of a new connection = DNS (node host) + TCP connect +
TLS/Reality handshake + VLESS request, exit connect and response.

| Change | Saves | Cannot save |
|---|---|---|
| TCP pool (exists, default node only) | one TCP handshake (5-10 ms here) | anything after TCP |
| Transport pool (TCP + TLS/Reality done) | DNS + TCP + TLS/Reality | the exit's connect and response |
| Hedged dial | stalls in the transport handshake | stalls after the VLESS request |
| Lock-free / O(1) paths | microseconds per connection, matters at scale | network time |
| gRPC nodes (Mihomo) | all handshakes after the first | n/a |

The transport pool is worth building only if the TLS/Reality phase is a
substantial share of the time to first byte; hedging only if slow dials are
really handshake stalls. Both are decided by `diag dial` and by the hedge
counters, see Gates.

## Measure first

1. **Baseline**: the CI benchmarks (`-tags bench`, `workflow_dispatch` with
   `benchtime`) on a clean state: netivri overhead per connect
   (`ConnectRouted` - `ConnectDirect`), parallel connect percentiles, the
   sticky and statistics micro-benchmarks. Recorded in `docs/perf/baseline.md`.
2. **Phase timings**: a patched vendored VLESS adapter reports the TCP and
   TLS durations of every dial; `netivri diag dial` reports them per node.
3. **Field**: the user runs `netivri diag dial` and `netivri stats` from a CI
   build. Aggregates go to `docs/perf/field-<date>.md` without node names or
   addresses.

## Vendoring Mihomo

The user allowed vendoring libraries that need changes.

- Location `third_party/mihomo`, `replace github.com/metacubex/mihomo =>
  ./third_party/mihomo` in `go.mod`; the `require` line keeps the upstream
  version.
- The import commit is pristine (a recursive diff against the module cache is
  empty). Every patch is a separate commit. New code goes into new files
  named `netivri_*.go` in the same package; edits to upstream files are
  minimal.
- `third_party/README.md` states the upstream URL and version, lists every
  patch with its reason, and describes the update procedure: import the new
  pristine tag, re-apply the patch commits, resolve conflicts, rerun CI.
  Each modified upstream file carries a short modification notice (GPL-3.0
  section 5(a)). The vendored directory itself stays free of our files.
- CI: `gofmt -l` skips `third_party`; Mihomo's own tests are not run; the
  license check and `scripts/collect-licenses.sh` keep shipping Mihomo's
  `LICENSE`. `.gitattributes` marks `third_party/**` as vendored and keeps
  its line endings untouched.
- Nothing else is vendored without a reason of the same kind.

## VLESS seam

In the vendored package `adapter/outbound`, for `*Vless` with the default
(TCP) network and TLS enabled:

- `PrepareTransport(ctx) (net.Conn, error)`: dial TCP and complete
  TLS/Reality; no VLESS request, no encryption handshake. Returns an
  error for gRPC, xhttp, ws, h2 and http networks. The returned connection
  is the unwrapped TLS connection (Vision keeps working).
- `SetTransportSource(TransportSource)` with
  `Take(ctx) (net.Conn, bool)`: `DialContext` asks the source first (TCP
  path only); on a hit it continues with `streamConnContext`, otherwise it
  dials as before. UDP and XUDP keep dialing normally.
- `SetPhaseObserver(PhaseObserver)` with `ObservePhase(phase, took, err)`
  for the phases `tcp` and `tls`.
- Without a source and an observer behaviour and cost stay as upstream.
- netivri reaches the adapter by unwrapping the `C.Proxy` of the node
  (`adapter.Proxy` embeds the `ProxyAdapter`); nodes that are not VLESS
  keep working without the seam.

## `netivri diag dial`

Runs in its own process with no listener (a running daemon is not touched).
For an account or a node id it parses the node adapters, and per node takes
N samples (default 5) of: `dns`, `tcp`, `tls` (VLESS only), `first_byte` (an
HTTP request to the probe URL through the node, as the warm-up does). Then
the idle survival test: prepare transports, wait 0.5, 1, 2, 4, 8 s, attach
and request; the result is the success ratio per idle time and a suggested
pool TTL (half of the longest idle time with all successes).

JSON envelope as the other commands. Per node (label only; no host, port,
uuid or key anywhere in the output):

```
{ "label": "...", "samples": 5,
  "phases_ms": { "dns": {"p50":..,"p90":..,"max":..}, "tcp": .., "tls": .., "first_byte": .. },
  "failures":  { "dns": 0, "tcp": 0, "tls": 0, "first_byte": 1 },
  "idle_survival": [ {"idle_ms": 500, "ok": 5, "fail": 0}, ... ],
  "suggested_pool_ttl_ms": 1000 }
```

## Hedged dial

In `stickyGroup.DialContext` (TCP), against stalls in the transport
handshake:

- A dial that has not returned within
  `threshold = clamp(2 x dial EWMA, 250 ms, 2 s)` (1 s without a dial EWMA)
  gets one more attempt to the **same** member. The first success wins; the
  other attempt is cancelled through its own context and closed if it still
  succeeds. The remote server keeps seeing the same exit address.
- A failure before the threshold starts no hedge (the existing
  retry-on-down logic applies).
- One logical dial produces one health and observer record: a success with
  the elapsed time of the logical dial, or one failure when every attempt
  failed. A cancelled attempt records nothing.
- Budget per member: a token bucket of 4 tokens refilled by one token per
  10 dials; without a token no hedge is started.
- Only member types verified in the module sources to send nothing to the
  target before the first payload are hedged (VLESS first; extend only after
  reading the source of the type). Eager types (trojan, socks5, http) are not
  hedged.
- `AffinityStats` and `status.affinity` gain `hedges` and `hedge_wins`. If
  the field shows no wins, the feature is removed.
- With a transport pool the hedge takes a pooled transport first.

## Transport pool

Per route node, over the VLESS seam, gated by the field measurement:

- Active only while the node had traffic within the last 30 s (no
  background load on idle nodes).
- Size follows the arrival rate times the handshake time, at most 4 per node
  and a global cap. The existing warm options (`PoolSize`, `PoolTTL`) are
  reused as the caps; no new configuration mode.
- TTL = half of the measured idle survival, capped. Checkout liveness: a read
  with a 1 ms deadline on the TLS connection (timeout means alive; data, EOF
  or an error means discard).
- A miss or an empty pool falls back to a normal dial. The default node
  moves to the same pool; node types without the seam keep the TCP pool.
- Per-node hits, misses, expired and discarded in `status`.

## Lock-free hot path

Bench-gated. Candidates, in order of likely effect: `nodeHealth` (a mutex per
dial result) to atomics: consecutive-failure counters, a 32-bit outcome
bitmap with popcount, EWMA by compare-and-swap on the float bits, the down
flag recomputed; `choose` without a closure allocated on every dial;
`affinityTable.acquire` with a lock-free read path (immutable per-shard
snapshot behind `atomic.Pointer`, `lastUsed` already atomic, writes under the
lock); `siteKey` allocations and the statistics `Record` channel only if the
benchmarks show them. Existing tests stay unchanged and pass with `-race`.

## O(1) account routing

One `MATCH` target replaces the per-account `IN-USER` rules: a dispatcher
adapter (type Selector, so the warm-up skips it) with a map from login to the
route's sticky group and a default (the default node or DIRECT). TCP, UDP,
`Unwrap`, chains and the throughput poller keep working. Benchmarked at
1, 100 and 10 000 accounts: the per-connection cost must be flat.

## Dial settings

Only with numbers: `tcp-concurrent` for hosts that resolve to several
addresses; a cache for node-host DNS if `diag dial` shows a DNS cost (the
system resolver already caches); TCP keep-alive for long tunnels. Each change
records its number in `docs/perf`.

## Gates and acceptance

- Hedged dial: shipped with counters; kept only if `hedge_wins` is non-zero
  in the field.
- Transport pool: only if the TLS/Reality phase is at least about 30 % of
  the time to first byte and idle survival is at least 1 s.
- Lock-free and O(1) changes: kept only if the CI benchmarks improve
  (time, allocations or tail) without regressions.
- Final before/after on the user's nodes: the share of new connections
  slower than 3 s (about 6 % at baseline, target below 1 %), p50 and p90
  time to first byte, pool and hedge counters.

## Testing

Per [testing.md](../testing.md): hedging with fake members (slow first and
fast second, both fail, late winner closed, cancellation not counted,
budget) and an integration test against an upstream that stays silent on
the first connection; the seam and the pool against a local VLESS +
Reality + Vision server built from Mihomo's listener packages (an
`internal/testutil` helper with a local TLS destination); `diag dial`
against that server, including a mode that closes idle transports after a
set time; the benchmarks of the sticky, health and routing paths in the
`bench` build.
