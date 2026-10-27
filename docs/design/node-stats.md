# Node statistics, destination affinity and data-path tuning

Status: contract for the implementation. Related: [accounts](accounts.md),
[warm-up](warmup.md).

## Goals

1. **Statistics.** Record, per exit node: liveness and latency (probes),
   dial time and success of real connections, and the throughput observed on
   real traffic. Keep them in SQLite, query them with `netivri stats`.
2. **Affinity.** For one account, all connections to one site leave through
   one exit node, so a remote server does not see the address change. A site
   is a registrable domain (the second-level domain, public-suffix aware:
   `example.com`, `example.co.uk`) **and all its subdomains**; an IP literal
   is its own site. The first binding of a site takes the **best** node of
   the account's route. The binding is kept while the node is healthy.
3. **Speed.** Remove avoidable waits and contention on the connection path;
   measure every change.

Non-goals: active bandwidth tests (no synthetic load, speed comes from real
traffic only), checking that an exit address really is in the claimed
country, node selection for the default route (`use`), hot reload.

## Findings that motivate this (concurrency review)

- The route group is a Mihomo `select`: it always uses its first member, the
  node with the smallest id (a hash), so the exit node is arbitrary and a
  dead one is never skipped.
- Probes run one node after another (5 s timeout each) and their results
  only feed `status`.
- A burst of first connections after a start runs argon2 once per
  connection (up to 4 at a time) until the password is cached.
- The TCP pool holds its mutex across a 1 ms socket read per hit.
- Mihomo defaults: `tcp-concurrent` off (serial dial over resolved
  addresses, 5 s per dead address).

## Node identity

Every node of a route has a stable **label** `{PROVIDER}/{NODE_ID}`
(subscription name and the stored node id). The core treats labels as opaque
strings; they are the key of the statistics and of persisted bindings.

## Statistics

### Sources (all passive or cheap)

| Kind | Source | Written as |
|---|---|---|
| probe | the warm-up probe of a node (`WarmOptions.OnProbe`) | ok, delay |
| dial | the sticky group times `member.DialContext` of every real connection | ok, dial time |
| throughput | every 5 s the sticky group reads Mihomo's active connection trackers (`statistic.DefaultManager`), attributes each tracker to the node in its chain and sums the bytes moved since the previous read per node | bytes, window |
| account dial | the sticky group times `member.DialContext` of every real connection; the login is the authenticated connection user (`metadata.InUser`) | ok, dial time per (login, node) |
| account throughput | from the same connection trackers, which carry the login; the bytes moved in the window are split into bytes up and down per (login, node) | bytes up, bytes down, window |

Throughput samples are written only for windows that moved at least 16 KiB;
the same threshold applies to the per-account windows. Account samples are
written separately from node samples and folded into `account_hourly` like
the node samples into `hourly`; the same retention applies (7 days raw, 90
days hourly).
The data path itself gets no wrapper: Mihomo already counts bytes per
connection, polling the counters costs nothing per packet.

### Store (`internal/stats`)

SQLite through `modernc.org/sqlite` (pure Go: release builds use
`CGO_ENABLED=0`; BSD-3-Clause, covered by the third-party licence collection).
File `{HOME}/stats.db`. Pragmas: `journal_mode=WAL`, `synchronous=NORMAL`,
`busy_timeout=5000`. One writer (the angel) and any number of readers (the
CLI, read-only).

```sql
CREATE TABLE node (
  id INTEGER PRIMARY KEY, label TEXT NOT NULL UNIQUE,
  provider TEXT NOT NULL DEFAULT '', country TEXT NOT NULL DEFAULT '',
  name TEXT NOT NULL DEFAULT '',        -- display name, new in schema v3
  first_seen INTEGER NOT NULL, last_seen INTEGER NOT NULL);       -- unix ms
CREATE TABLE sample (
  ts INTEGER NOT NULL, node INTEGER NOT NULL, kind INTEGER NOT NULL,
  ok INTEGER NOT NULL, ms INTEGER NOT NULL DEFAULT 0,
  bytes INTEGER NOT NULL DEFAULT 0);                              -- unix ms
CREATE INDEX sample_node_ts ON sample(node, ts);
CREATE INDEX sample_ts ON sample(ts);
CREATE TABLE hourly (
  hour INTEGER NOT NULL, node INTEGER NOT NULL, kind INTEGER NOT NULL,
  n INTEGER NOT NULL, ok INTEGER NOT NULL, ms_sum INTEGER NOT NULL,
  ms_max INTEGER NOT NULL, bytes INTEGER NOT NULL,
  PRIMARY KEY (hour, node, kind)) WITHOUT ROWID;                  -- unix hour
CREATE TABLE account (
  id INTEGER PRIMARY KEY, login TEXT NOT NULL UNIQUE,
  first_seen INTEGER NOT NULL, last_seen INTEGER NOT NULL);       -- unix ms
CREATE TABLE account_sample (
  ts INTEGER NOT NULL, account INTEGER NOT NULL, node INTEGER NOT NULL,
  kind INTEGER NOT NULL, ok INTEGER NOT NULL, ms INTEGER NOT NULL,
  bytes INTEGER NOT NULL, up INTEGER NOT NULL, down INTEGER NOT NULL);
CREATE INDEX account_sample_account_ts ON account_sample(account, ts);
CREATE INDEX account_sample_node_ts ON account_sample(node, ts);
CREATE INDEX account_sample_ts ON account_sample(ts);
CREATE TABLE account_hourly (
  hour INTEGER NOT NULL, account INTEGER NOT NULL, node INTEGER NOT NULL,
  kind INTEGER NOT NULL, n INTEGER NOT NULL, ok INTEGER NOT NULL,
  ms_sum INTEGER NOT NULL, ms_max INTEGER NOT NULL, bytes INTEGER NOT NULL,
  up INTEGER NOT NULL, down INTEGER NOT NULL,
  PRIMARY KEY (hour, account, node, kind)) WITHOUT ROWID;         -- unix hour
```

`PRAGMA user_version` carries the schema version (4). The account tables are
new in schema version 2 and the migration from version 1 is additive: a v1
database keeps its data and opens read-only; a `stats` query on it simply
has no account data. Schema version 3 adds the `node.name` display-name
column (`ALTER TABLE node ADD COLUMN name TEXT NOT NULL DEFAULT ''`); the
migration from v1 or v2 is additive too, and a read-only open of a v1 or v2
database (which does not migrate) reads the node rows without a name. Schema
version 4 adds the `binding` table (below); the migration from every older
version is additive, and a read-only open of an older database simply has no
bindings. Account samples are separate from the node samples. Writes are batched: a
bounded queue, flushed every second or at 256 samples in one transaction;
`Record` never blocks (a full queue drops the sample and counts it).
Retention: raw samples older than `retention` (default 7 days) are folded
into `hourly` and deleted at open and then hourly; `hourly` rows older than
90 days are deleted. A query window inside the raw retention is exact
(percentiles from raw rows); a longer window uses `hourly` and reports no
percentiles.

### Go contract: package `internal/stats`

```go
type Kind uint8

const (
	KindProbe      Kind = 1 // ms = probe delay
	KindDial       Kind = 2 // ms = dial time
	KindThroughput Kind = 3 // bytes moved in a window of ms milliseconds
)

type Sample struct {
	At    time.Time
	Label string
	Kind  Kind
	OK    bool          // always true for KindThroughput
	Delay time.Duration // probe delay, dial time or throughput window
	Bytes int64         // KindThroughput only
}

type NodeInfo struct{ Label, Provider, Country, Name string } // Name: sanitized display name

type Options struct {
	Path        string
	Retention   time.Duration    // raw samples; 0 = 7 days
	QueueSize   int              // 0 = 4096
	Now         func() time.Time // nil = time.Now (tests)
}

type Store struct{ /* ... */ }

func Open(o Options) (*Store, error)           // creates the directory, migrates
func OpenReadOnly(path string) (*Store, error) // os.ErrNotExist when absent
func (s *Store) Record(Sample)                 // non-blocking, concurrency-safe
func (s *Store) Dropped() uint64
func (s *Store) SetNodes(infos []NodeInfo) error // upsert provider/country/name
func (s *Store) Flush(ctx context.Context) error // queued samples are on disk
func (s *Store) Close() error                    // flush, stop the writer, close

type Query struct {
	Since    time.Duration // 0 = 24h
	Provider string        // empty = any
	Country  string
	Label    string
}

type NodeStats struct {
	Label, Provider, Country, Name string // Name empty when unknown
	Probes, ProbesOK         int
	DelayAvgMS, DelayP50MS, DelayP95MS int64
	Dials, DialsOK           int
	DialAvgMS                int64
	ThroughputP50KBps, ThroughputMaxKBps int64 // per window, KiB/s
	Bytes                    int64
	LastSeen, LastOK         time.Time // zero when never
}

func (s *Store) Nodes(q Query) ([]NodeStats, error) // sorted by label
```

`Record` on a read-only store or after `Close` is a no-op.

## Affinity

### The sticky group (`internal/core`)

Each route gets, instead of a `select` group, a custom Mihomo group adapter
(`C.ProxyAdapter` wrapped with `adapter.NewProxy`) named like the old group
(`netivri-group-{R}`). The `select` groups stay in the raw config so the
rules validate; after `ApplyConfig` the sticky adapters replace them in the
proxy map through `tunnel.UpdateProxies`, the way the pool replaces the
default node. Members are the already built leaf proxies `netivri-r{R}-{N}`.

### Site key and binding

- key = `(metadata.InUser, site)`; `site` = lower-case host without trailing
  dot reduced with `publicsuffix.EffectiveTLDPlusOne`; a host that is itself
  a public suffix, or has no dot, is used as is; an IP literal, or an empty
  host with `metadata.DstIP`, is the textual IP.
- A binding holds the member index, `boundAt` and an atomic `lastUsed`.
- Bindings are idle-expired after `AffinityTTL` (default 24 h) and bounded in
  number (100 000, oldest `lastUsed` evicted first).

### Choosing the best node

Per node the core keeps: EWMA of probe delay (alpha 0.3), EWMA of dial time
(alpha 0.2), a ring of the last 32 outcomes (probes and dials) for the
failure rate, the EWMA of throughput windows of at least 64 KiB, and
consecutive failure counters.

```
delay  = mean of the known EWMAs (probe, dial); unknown without samples
down   = 2 consecutive probe failures or 3 consecutive dial failures;
         any success clears it
score  = delay * (1 + 4*failRate) / (1 + min(throughputMiBps, 20) / 20)   lower is better
```

Background probes back off while a node stays down: the probe interval
of a node is 45 s while it is not down, then 45 s, 90 s, 3 m and every
10 minutes (cap) over its consecutive down probes. The first success of
any kind (probe or real dial) resets it to 45 s.

Selection for a new binding: candidates are the members that are not down
and have a delay; if there are none, the members that are not down; if there
are none, all members (the route is never left, and nothing falls back to
another country or provider). `best` is the lowest score. Candidates within
`max(20 ms, 15 % of best)` of `best` are near-equal: among them the one with
the highest `fnv64a(login | site | label)` wins, so equal nodes share the
sites of an account deterministically instead of flapping or piling up.

A binding created while more than half of the route's members still had no
delay is **provisional**: it expires after 2 minutes and the next use selects
again (a site may move once at startup, never later because of a better
node).

While the bound node is not down it is used for every connection of the site,
even if another node became faster: stability beats optimality. When the
bound node is down, the next connection rebinds to the best remaining node.
A failed dial that makes the node down is retried once on the re-selected
node; any other failure is returned to the client.

UDP associations use the same choice and the same site key as TCP (login and
site), so one site keeps one exit node over TCP and UDP.

The hit path (a bound, healthy node) takes one shard lock and one atomic
load and allocates nothing; selection runs only for a new site or a down
node. Look-up, selection and installation of a binding are one atomic step per
key, so concurrent first connections to a site agree on one node.

### Hedged dial

A TCP dial on a hedgeable member that has not returned within
`clamp(2 x dial EWMA, 250 ms, 2 s)` (1 s without a dial EWMA) is raced
against one more attempt to the **same** member, so a stalled transport
handshake does not cost the whole tunnel timeout. The first success wins;
the loser attempt is cancelled through its own context and its connection,
if one still arrives, is closed. One logical dial produces one health and
observer record: a success with the elapsed time of the logical dial, or one
failure when every started attempt failed; a cancelled attempt records
nothing. Only member types verified in the module sources to send nothing to
the target before the first payload are hedged (VLESS; eager types such as
vmess, shadowsocks, trojan, socks5 and http are not). Each member has a
budget of 4 hedge tokens, refilled by one token per 10 dials; without a
token no hedge is started. `Hedges` and `HedgeWins` count started attempts
and logical dials won by the hedge; a feature with no wins is removed.
UDP associations are not hedged. See [latency.md](latency.md) for the
contract and the gate.

### Go contract: `internal/core` additions

```go
// Options gains:
NodeLabels  map[string][]string // route key -> label per node, same order and length as RouteNodes[key]; nil = "r{R}-{N}"
Observer    Observer           // nil = none
AffinityTTL time.Duration      // 0 = 24h; negative is invalid

// Observer receives measurements; methods must not block and must be safe
// for concurrent use.
type Observer interface {
	ObserveProbe(label string, alive bool, delay time.Duration)
	ObserveDial(label string, ok bool, took time.Duration)
	ObserveThroughput(label string, bytes int64, window time.Duration)
}

// Instance gains:
func (i *Instance) AffinityStats() AffinityStats

type AffinityStats struct {
	Bindings  int
	Rebinds   uint64
	Hedges    uint64
	HedgeWins uint64
}

// WarmOptions gains (warm.go):
ProbeConcurrency int // 0 = 8; probes of one round run this many at a time
OnProbe func(name string, alive bool, delay time.Duration) // after every probe, from probe goroutines; name is the internal proxy name netivri-r{R}-{N}
Down func(name string) bool // reports the down state of a node proxy; down nodes are probed with the backoff interval above
```

The core maps the internal name to its label and forwards probe results to
both the sticky group's health table and `Observer.ObserveProbe`.

### Down and back events

`Options.OnNodeHealth` receives a `core.NodeEvent` on every change of a node's
down state, built under the node's lock and delivered after it is released:

```go
type NodeEvent struct {
	Label    string
	Down     bool          // true: just went down, false: back
	Reason   string        // down: "2 probe failures (timeout)" or "3 dial failures"
	SinceOK  time.Duration // down: time since the last successful probe or dial, 0 if never
	Downtime time.Duration // back: how long the node was down
}
```

`WarmOptions.OnProbeError(name, err)` runs just before `OnProbe` for a failed
probe; the core keeps the kind of the error on the node (`timeout`,
`connection refused`, `connection reset`, `dns failure`, `network unreachable`,
`connection closed`, `handshake failed`, else `error`) so the next down event
can name it. A success clears it. The kind never carries an address. The down
state, the time it began and the time of the last answer are carried over by a
`Reconfigure`.

`cmd/netivri` writes the log lines. Three or more nodes going down within 10
seconds are one burst: the first two get a `WARN` line each, the third raises
one `WARN` summary (`node burst: ...`, with the reason kinds, and "right after
start" in the first minute), the others and the third's own line are `INFO`,
and an `INFO` line closes the burst when its last node is back. `doctor` counts
the lines and the summary as node-down reports.

## Wiring (`cmd/netivri`)

- `routePool` also returns labels (`provider/{NODE_ID}`) in node order.
- `up` opens `{HOME}/stats.db`, calls `SetNodes`, passes an adapter of the
  store as `core.Observer`, closes the store after the core. A store that
  cannot be opened is logged and the angel runs without statistics: the
  proxy never fails because of its bookkeeping.
- The status answer gains `stats` (`path`, `dropped`) and `affinity`
  (`bindings`, `rebinds`), both in the full view only
  (`netivri status --full`).

### Configuration

None in the first version: retention (7 days raw) and the binding lifetime
(24 hours idle) are constants. A switch is added when persisted bindings
(which hold visited sites) land.

### `netivri stats`

```
netivri stats [--sub {SUBSCRIPTION}] [--country {COUNTRY}] [--node {NODE}] [--since 24h] [--by {node|country|provider|account}] [--account {ACCOUNT}]
```

Reads the database read-only, works while the angel runs and when it is
stopped (`STATS_UNAVAILABLE`, exit 5, when no database exists). `data`:
`{"since": "24h", "by": "node", "items": [{"label", "provider", "country",
"probes", "uptime_pct", "delay_ms": {"avg","p50","p95"}, "dials",
"dial_ok_pct", "dial_ms_avg", "throughput_kbps": {"p50","max"}, "bytes",
"last_seen", "last_ok"}]}`; grouped forms sum counters and average the rest
weighted by counts. `--by account` reports one item per login: `login`,
`bytes_up`, `bytes_down`, `dials`, `dial_ok_pct`, `nodes`, `countries`,
`providers`, sorted by login. `--account {ACCOUNT}` works with every `--by`
mode; with `--by account` it selects one login and combines with
`--sub`/`--country`/`--node` (which restrict the account rows to samples on
matching nodes). With the node, country and provider views the rows are a
subset built from that login's account samples (dials, dial_ok_pct,
dial_ms_avg, bytes up+down; no probes or rates).

## Persisting bindings

Schema version 4 adds, in the same database:

```sql
CREATE TABLE binding (
  login TEXT NOT NULL, site TEXT NOT NULL, label TEXT NOT NULL,
  bound_at INTEGER NOT NULL, last_used INTEGER NOT NULL,
  PRIMARY KEY (login, site));                                     -- unix ms
```

A binding is stored **by node label** (`{PROVIDER}/{NODE_ID}`), never by
member index: labels are stable across a route change, indexes are not.
Provisional bindings (the 2-minute startup bindings) are never stored.

**Privacy.** The table holds the logins and the sites (hostnames) the user
visited; its retention is the binding lifetime. Rows idle longer than the
binding TTL (24 hours) are deleted on every write and on every load. Sites
are never printed or logged: not in `netivri stats` (which has no binding
view), not in `netivri logs`, not in `status`.

**Load.** A live reload that only changes nodes and routes keeps the core
running and moves the live bindings to the new routes by label, in memory
(see [live-reload.md](live-reload.md)). When the angel starts or restarts the
core, it reads the stored bindings and passes
them to the core as `Options.InitialBindings`. The core maps each record to
the member whose label matches within the login's route (account, route key,
plan labels) and installs it with the stored `bound_at` and `last_used`, so
the idle TTL continues from the stored value. A record whose login has no
route, whose label is not a member of that route, or that is already expired
by `last_used + TTL` is ignored, as is a site the table already holds. A
stored binding that points at a node that is currently down needs no special
case: `acquire` re-picks for a down member.

**Save.** While the angel runs, a loop every 30 seconds (off the hot path)
takes `SnapshotBindings()` — the live, non-provisional, non-expired bindings
with labels, at most 100 000, taken under the table's read locks without
creating or refreshing bindings — and replaces the stored set in one
transaction: rows are upserted, rows no longer present are deleted, and rows
older than the TTL are deleted. A final write runs before the core closes,
both for a reload and for shutdown, after the loop has stopped and before
the statistics store is closed. A write failure is logged at WARN and never
stops the angel; a store that cannot be opened leaves the angel without
persisted bindings, as it already leaves it without statistics.

**Status.** `affinity` in `status` reports `restored` (how many bindings the
latest core start loaded) and `saved_at` (RFC3339 UTC of the last successful
write, omitted when none) next to the existing counters.

## Data-path tuning

First wave (no behaviour change for callers): probes run with bounded
parallelism (default 8) with jitter and exponential backoff for dead nodes;
one argon2 verification per account for a burst of first connections; the
pool no longer holds its mutex across socket reads; the connection tracker
uses a sharded set. Second wave: a benchmark harness (build tag `bench`,
loopback echo server and fake SOCKS5 upstream, no external traffic) reporting
connect latency p50/p99, throughput and 1 000 parallel connects; then only
measured changes are kept (`tcp-concurrent` for direct dials, GC and socket
settings, the cost of recording statistics). The further latency work (hedged
dial, transport pool, lock-free paths) is specified in
[latency.md](latency.md).

## Testing

Per [testing.md](../testing.md): unit tests are hermetic; the store is tested
on a temporary directory with an injected clock; the sticky group is tested
with fake members and a fake clock, covering the site key (subdomains,
multi-label suffixes, IP literals), best selection, near-equal tie by hash,
provisional bindings, down and rebind, TTL, and concurrent use under `-race`.
