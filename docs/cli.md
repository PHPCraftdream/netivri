# CLI contract

This document is the contract between `netivri` and its callers (humans,
scripts, AI agents). Changing anything here is a breaking change and bumps
`schema_version`.

## Global flags

| Flag | Meaning |
|---|---|
| `--json` | Write results and errors as JSON instead of Ktav, the default (see [Output](#output)). Env: `NETIVRI_OUTPUT=json`. |
| `--home {DIR}` | State directory. Default: a `.netivri` directory in the current directory if there is one, else `$NETIVRI_HOME`, else `%APPDATA%\netivri` / `$XDG_CONFIG_HOME/netivri`. See [Separate state directories](#separate-state-directories). |
| `--local` | Use `./.netivri` in the current directory as the state directory, creating it. Cannot be combined with `--home`. |
| `--quiet` | No human-oriented output on stderr. |
| `--no-color` | No ANSI. Implied when not a TTY or `NO_COLOR` is set. |
| `-h`, `--help` | Help for the command; see [`help`](#help). |

Commands never prompt. Anything that would need confirmation takes a flag
instead (`--yes`).

## Commands

```
netivri sub add {URL} [--name {NAME}] [--via {PROXY}]
                                          add a subscription (idempotent by URL)
netivri sub list
netivri sub update [{SUBSCRIPTION} | --all]   re-fetch
netivri sub rm {SUBSCRIPTION}
netivri sub countries [{SUBSCRIPTION} | --url {URL} [--via {PROXY}]]

netivri node list [--sub {SUBSCRIPTION}] [--country {COUNTRY}] [--alive]
netivri node test [{NODE} | --all] [--exit] [--timeout {DURATION}]   (planned)

netivri account add {ACCOUNT} [--provider {SUBSCRIPTION}[,{SUBSCRIPTION}...]] [--country {COUNTRY}[,{COUNTRY}...]] [--clients {RULES}] [--pre-socks5 {URL}] [--post-socks5 {URL}]
netivri account clone {SOURCE} {ACCOUNT} [--password {PASSWORD}|init] [--provider {SUBSCRIPTION}[,{SUBSCRIPTION}...]] [--country {COUNTRY}[,{COUNTRY}...]] [--clients {RULES}]
netivri account list
netivri account show {ACCOUNT}
netivri account set-country {ACCOUNT}[,{ACCOUNT}...] {COUNTRY}[,{COUNTRY}...] | --all {COUNTRY}[,{COUNTRY}...]
netivri account set-country {ACCOUNT}[,{ACCOUNT}...] --none | --all --none
netivri account set-provider {ACCOUNT}[,{ACCOUNT}...] {SUBSCRIPTION}[,{SUBSCRIPTION}...] | --all {SUBSCRIPTION}[,{SUBSCRIPTION}...]
netivri account set-provider {ACCOUNT}[,{ACCOUNT}...] --none | --all --none
netivri account set-clients {ACCOUNT}[,{ACCOUNT}...] {RULES} | --all {RULES}
netivri account set-socks5 {ACCOUNT}[,{ACCOUNT}...] [--pre {URL|none|inherit}] [--post {URL|none|inherit}]
netivri account set-clients {ACCOUNT}[,{ACCOUNT}...] --none
netivri account rm {ACCOUNT}

netivri stats [--sub {SUBSCRIPTION}] [--country {COUNTRY}] [--node {NODE}] [--since {DURATION}] [--by {node|country|provider|account}] [--account {ACCOUNT}]

netivri diag dial --account {ACCOUNT} | --node {NODE} [--samples {N}] [--idle {LIST|off}] [--max-nodes {N}] [--url {URL}]

netivri use {NODE}                        (planned)
netivri use --country {COUNTRY} [--fastest | --auto]
netivri use --fastest | --auto            --auto keeps re-selecting by latency

netivri up [--foreground] [--port {PORT}] [--bind {ADDR}] [--home {DIR} | --local]
netivri down
netivri status [--full] [--watch] [--interval {DURATION}]

netivri logs [--follow] [--level {debug|info|warn|error}] [--since {DURATION}] [--tail {N}]
netivri doctor
netivri schema                            this contract as JSON (planned)
netivri version
netivri help [{COMMAND} [{SUBCOMMAND}]]   also: netivri, netivri {COMMAND} --help
```

Placeholders are written in curly braces, here and in the help of the
program. `{ACCOUNT}` is the login of an account (`alice`) and `{SUBSCRIPTION}`
the name of a subscription (`main`). `{NODE}` is a node id (stable hash) or an
exact node name. `{COUNTRY}` is an ISO 3166-1 alpha-2 country code (`DE`, `NL`,
`FI`). `{RULES}` is an allowed-clients rule list (`loopback`, `10.0.0.0/8`,
`*`; see `netivri help config`). Commands marked (planned) are described here
but not implemented yet. `{URL}`, `{PROXY}`, `{DIR}`, `{ADDR}`, `{PORT}`, `{N}`, `{DURATION}` and
`{LIST}` stand for a value of the kind they name. Brackets mean optional, a
bar means one of, and `{a|b}` is a choice written out.

## Output

Every result and every error is one document with the same keys, in one of two
formats. The default is [Ktav](https://github.com/ktav-lang), the format of the
configuration file; `--json` (after the command, before a bare `--`)
or the environment variable `NETIVRI_OUTPUT=json` selects JSON instead. The
format changes how the document is written, never what is in it, and the
examples in this document are written in JSON.

Success: one document on stdout.

```ktav
schema_version: 1
ok: true
data: {
    version: 0.1.0
}
```

```json
{"schema_version": 1, "ok": true, "data": { ... }}
```

Failure: one document on stderr, non-zero exit code.

```ktav
schema_version: 1
ok: false
error: {
    code: FETCH_HTTP_STATUS
    hint: the subscription server rejected the request
    message: unexpected HTTP status 403
}
```

```json
{"schema_version": 1, "ok": false,
 "error": {"code": "FETCH_HTTP_STATUS", "message": "unexpected HTTP status 403", "hint": "the subscription server rejected the request"}}
```

A Ktav document starts with `schema_version` and `ok`, then holds `data` or
`error`; the keys inside are sorted. Values keep their types (numbers,
booleans, `null`, lists) and a text that looks like a number, such as a login
`123456`, is written with the raw marker (`login:: 123456`), so a Ktav reader
gets the same values a JSON reader would. Scripts and agents that parse the
output should ask for JSON, with `--json` or with `NETIVRI_OUTPUT=json` set
once. If the Ktav renderer ever fails, the document is written as JSON instead,
so that an error is never lost.

Subscription URLs are credentials and are never printed: not on stdout,
not on stderr, not in error messages. The same holds for `--via` proxies.

`netivri logs --follow` and `netivri status --watch` are the streaming commands;
with `--json` they print one JSON object per line (NDJSON) instead of frames
or log lines.

### `help`

```
netivri help [{COMMAND} [{SUBCOMMAND}]] [--json]
netivri [{COMMAND} [{SUBCOMMAND}]] --help [--json]
```

Every way to ask for help prints it to stdout and exits 0, and no command is
run: no arguments at all, `help`, `-h`, `--help` or `-help` in the first
position, and a help flag anywhere after a command (`netivri sub add --help`).
After a bare `--` a help flag is an ordinary argument. `netivri help` shows
the overview: the quick start, the commands by group, the global flags, the
output contract, the exit codes and the commands that are described here but
not implemented yet. `netivri help sub add` (or `netivri sub add --help`)
shows one command: usage, description, flags, examples with comments, the
errors it can return with their exit codes, and related commands. A command
with subcommands (`sub`, `node`, `account`, `diag`) lists them.

Besides commands, `netivri help` has three concept topics, listed in the
overview under the group "Concepts" and available as `netivri help
{config|routing|output}` and with `--json`: `config` (the `config.ktav`
file, where it lives and every key it accepts), `routing` (route tiers,
the `*` country, sticky bindings, the `init` claim and the
`allowed_clients` cascade) and `output` (the fields each command prints).

Help is text for people unless `--json` is given, whatever `NETIVRI_OUTPUT`
says, so `netivri help | less` works. The text is ASCII only and not part of
the contract; the JSON is.

```json
{"kind": "overview", "summary": "...", "description": "...", "usage": ["..."],
 "quick_start": ["..."], "planned": ["use", "node test", "schema"],
 "commands": [{"name": "up", "group": "Proxy service", "summary": "...", "usage": ["..."]}],
 "global_flags": [{"name": "--home {DIR}", "meaning": "..."}],
 "exit_codes": [{"code": 0, "meaning": "success"}]}
{"kind": "topic", "name": "sub add", "group": "Subscriptions and nodes", "summary": "...",
 "usage": ["..."], "description": "...",
 "flags": [{"name": "--name {NAME}", "meaning": "..."}],
 "examples": [{"comment": "...", "command": "..."}],
 "errors": [{"code": "SUB_EXISTS_NAME", "exit": 6, "meaning": "..."}],
 "notes": ["..."], "see_also": ["sub countries"],
 "subcommands": [{"name": "sub add", "summary": "...", "usage": ["..."]}]}
```

These are the `data.help` values for the overview and for a topic;
`subcommands` appears only for a command that has some, and `group`, `flags`,
`notes` and `see_also` only when set.

An unknown topic (`netivri help nope`) and an unknown flag after `help` are
`USAGE` errors (exit 2) with the hint `run: netivri help`; so is an unknown
command (`netivri frobnicate`). A command that is described in this document
but not implemented yet (`use`, `logs`, ...) is an unknown command.

### `sub countries`

Lists the exit countries of a provider or of a subscription URL. Three
forms; the URL is never printed.

- `sub countries {SUBSCRIPTION}`: the stored snapshot, no network.
- `sub countries`: every configured provider (config order) plus `countries`,
  the summed union of their stored snapshots.
- `sub countries --url {URL} [--via {PROXY}]`: fetches through the same strict
  path as `sub add` (`--via` wins over `fetch.proxy`, no direct fallback),
  parses and prints, but stores nothing and touches no config. Fetch and
  parse failures use the `FETCH_*` / `SUBSCRIPTION_*` codes of `sub add`.
  `--url` together with a {SUBSCRIPTION}, `--via` without `--url` and an empty
  `--url` are usage errors.

A configured provider without a stored snapshot is reported with null
fields (like `sub list`); a corrupt snapshot is `SUB_SNAPSHOT_INVALID`.

```json
{"provider": "main", "nodes": 24, "countries": [{"code": "DE", "nodes": 12}, {"code": "NL", "nodes": 8}], "unknown": 4}
{"providers": [{"name": "main", "nodes": 24, "countries": [{"code": "DE", "nodes": 12}], "unknown": 0},
               {"name": "alt", "nodes": null, "countries": null, "unknown": null}],
 "countries": [{"code": "DE", "nodes": 12}]}
{"nodes": 3, "countries": [{"code": "DE", "nodes": 2}, {"code": "NL", "nodes": 1}], "unknown": 0}
```

These are the `data` values for the {SUBSCRIPTION} form, the no-argument form and the
`--url` form.

### `stats`

The recorded health, delay and throughput of the exit nodes
(design: [design/node-stats.md](design/node-stats.md)). While `up` serves
accounts that have a provider or an exit country, it records into
`{HOME}/stats.db`: the background probes of the nodes, the dial time and
outcome of every real connection, and the throughput of real traffic (bytes
moved per 5-second window; there are no synthetic speed tests). `stats` reads
that database read-only, so it works while the angel runs and after it
stopped.

| Flag | Meaning |
|---|---|
| `--sub {SUBSCRIPTION}` | Only the nodes of this subscription (provider). |
| `--country {COUNTRY}` | Only the nodes of this country. |
| `--node {NODE}` | Only this node. A label is `{PROVIDER}/{NODE_ID}`, like the `sub` and `id` of `node list`. |
| `--since D` | Window: `90m`, `24h`, `7d`, ... (default `24h`, at most `365d`). |
| `--by G` | `node` (default), `country`, `provider` or `account`. |
| `--account {ACCOUNT}` | Only this account. Works with every `--by` mode. |

```json
{"since": "24h0m0s", "by": "node", "items": [
  {"label": "main/a1b2c3", "name": "Germany 1", "provider": "main", "country": "DE",
   "probes": 120, "uptime_pct": 99.2,
   "delay_ms": {"avg": 84, "p50": 80, "p95": 140},
   "dials": 340, "dial_ok_pct": 99.1, "dial_ms_avg": 96,
   "throughput_kbps": {"p50": 1200, "max": 8400},
   "bytes": 123456789,
   "last_seen": "2026-10-20T10:00:00Z", "last_ok": "2026-10-20T10:00:00Z"}]}
```

`name` is the display name the subscription gives the node (cleaned of control
characters, at most 64 characters), present only when there is one; the grouped
views (`--by country`, `--by provider`, `--by account`) have no names.
`uptime_pct` is the share of successful probes and `dial_ok_pct` the share of
successful dials of real connections; both are `null` without samples.
`last_seen` and `last_ok` (RFC3339 UTC) are `null` when there was none.
`delay_ms` covers successful probes, `dial_ms_avg` successful dials, and
`throughput_kbps` the per-window rates in KiB/s. Windows longer than the raw
retention (7 days) are answered from hourly summaries: percentiles and
throughput are then `0`.

With `--by country` or `--by provider` each item is one group (`country`,
with `unknown` for nodes without one, or `provider`) with the number of
`nodes`, summed counters and averages weighted by the number of successful
samples; percentiles do not merge and are left out, `throughput_kbps` carries
only `max`. Items are sorted by label or group name.

With `--by account` each item is one login (the authenticated user of the
connection). Per account the store records the dials of real connections
per node with outcome and duration, and the throughput of real traffic as
bytes up and down per 5-second window, attributed by the login and the node
it used; the same 16 KiB window threshold as for node throughput applies.
Each item has `login`, `bytes_up`, `bytes_down`, `dials`, `dial_ok_pct`,
`nodes` (distinct node count), `countries` and `providers` (comma-joined
sorted lists). Items are sorted by login. `dial_ok_pct` is `null` without
dials; `countries` and `providers` are empty strings when unknown.
`--account` selects one login; with `--by account` the other node filters
(`--sub`, `--country`, `--node`) restrict the account rows to samples on
matching nodes. With `--by node`, `--by country` or `--by provider` the rows
are computed from the account samples of that login only and are a documented
subset: dials, `dial_ok_pct`, `dial_ms_avg`, bytes (up plus down combined)
and the nodes; `probes`, `uptime_pct`, `delay_ms` and `throughput_kbps` are
`0` or null because account samples carry no probes or rates. A database
created before this version (schema v1) has no account data.

```json
{"since": "24h0m0s", "by": "account", "items": [
  {"login": "alice", "bytes_up": 123456789, "bytes_down": 987654321,
   "dials": 340, "dial_ok_pct": 99.1, "nodes": 2,
   "countries": "DE", "providers": "main"}]}
```

When no database exists yet: `STATS_UNAVAILABLE`, exit 5. Invalid `--since`,
`--by` or `--country`: usage error, exit 2.

### `logs`

```
netivri logs [--follow] [--level {debug|info|warn|error}] [--since {DURATION}] [--tail {N}]
```

The angel log, `{HOME}/run/netivri.log`: start and stop, applied and rejected
configuration changes, subscription refreshes, storage problems, and route nodes going
down (`WARN`) or coming back (`INFO`), with the node label, its display name when one is known, the
provider and the country, the reason and how long it was silent, as in
`node own/a970e0d9fef1 (Finland 1, provider own, country FI) is down: 2 probe failures (timeout), last ok 3m12s ago`,
and the downtime when it is back (`... is back after 41s down`). The reason is `2 probe
failures` with the kind of the last probe error (`timeout`, `connection refused`,
`connection reset`, `dns failure`, `network unreachable`, `connection closed`,
`handshake failed`, `proxy auth failed` when a proxy hop or the node did not take the credentials,
`proxy refused` when a proxy answered but would not connect, `error: {WORDS}` for a cause it does not
know, or none) or `3 dial
failures`. `{WORDS}` is a short excerpt of the error in which every word that could be
an address, host, port, path or credential is replaced by `<addr>`. A node that goes
down 3 times within an hour is flapping: one `WARN`, `node ... is flapping: 3 downs
within the last hour (...)`, says so, and its further down and back lines are `INFO`
until it has been quiet for an hour (a flapping node does not count toward a burst). Only
a node of the first tier of some account's route raises a `WARN` for going down or flapping; the
nodes of later tiers are logged at `INFO`, since no account uses them while its first tier is up,
and what they repeat (the downs and backs of a flapping node after its `flapping` line, the down of a
twin) at `DEBUG`, which `logs` shows only with `--level debug`.

Once an hour each account that had connections gets one summary line, `account alice: last hour: 412
connections ended, 0 broken, 30 closed before any data, 3 without an answer (3 sites; ports 443 x3; waited 2.0s median, 15s longest), 0 dials failed,
412.0 MiB down, 38.1 MiB up, answers started after 0.4s median, 1.8s 95th`, so that a log
without a report of broken connections says "none" and not nothing. The bytes are those of the connections that
ended in that hour, so that a count of connections without an answer can be checked against what moved. `closed before any data` is a client that
left without sending a byte (browsers and messengers open connections they do not use); `without an answer` is a
client that sent data and got nothing back from the node's side, the one to watch; the line says how many
different sites they went to (by hash, never named), their ports (the three most used, the rest as `other`) and how
long they waited (median and longest), which tells one dead site that is retried from many, and a client that
gives up quickly from a wait that ran into a timeout. The last part says how long the node's side took to
start answering, from the first bytes the client sent to the first byte that came back (median and 95th
percentile over the connections that got an answer; a node that spoke before the client did has none), which
is what "slow" means when nothing failed.

What happens to the connections of the accounts is reported once a minute, one line per account and
node and kind, only when something went wrong, and never with a site, an address, an error text or a
password: `account alice via {NODE}: 3 dials failed (timeout x2, connection reset x1)` (`WARN` from 3
failures, `INFO` below); `account alice via {NODE}: 98 connections ended, 5 broken (connection reset
x4, timeout x1), 3 closed before any data, 2 without an answer` (a connection is broken when the node's side of it
returned an error; `WARN` from 3 broken and at least a twentieth of the ended ones);
`account alice via {NODE}: slow answers: started after 3.0s median, 8.0s 95th, over 40 connections` (`INFO`;
only when at least 10 connections of the minute got an answer and the median is 2 seconds or more);
`account alice: 3 sites moved from {NODE} to {NODE}: node down` (`WARN`; `better tier` is `INFO`);
when the sites of one account moved between several pairs of nodes in that minute for one reason
(a node went down and its sites were spread over the rest), it is one line with the counts,
`account alice: 41 sites moved from 12 nodes to 9 nodes: node down` (a side that is a single node is named),
and the pairs are `DEBUG` lines;
`4 proxy logins failed (alice x2, unknown logins x2)` (`WARN`; only configured accounts are named).
The reason of a down caused by dials is `3 dial failures (timeout)`, with the kind of the last error
(`timeout`, `connection refused`, `connection reset`, `dns failure`, `network unreachable`,
`connection closed`, `handshake failed`, `proxy auth failed`, `proxy refused` or `error`); dial failures count only when they reached 2
different sites (a run of 10 to one site counts too, so a dead node used for one site still fails over). The
same node id under two providers is one server: when its twin goes down within 10 seconds,
that line is `INFO` and ends with `, same server as {LABEL}`, and the pair counts once for
bursts and flapping. A node
that has never answered since it was added (no probe or dial has ever succeeded) is one
`INFO` line, `node ... has never answered: 2 probe failures (timeout)`, and counts toward
neither a burst nor flapping. When 3
or more nodes go down within 10 seconds (a lost network on this machine, a shared
outage, or the first probes after a start) the third one raises a single `WARN`,
`node burst: 3 of 36 nodes went down within 10s (probe failures (timeout) x3), likely a
lost network or a shared outage` (with `, right after start` in the first minute), the
nodes of the burst are logged at `INFO` from then on, and `node burst over: 9 nodes
were down, all back after 41s` closes it when the last one is back. When the computer
sleeps the angel is frozen and writes nothing; when it wakes, one `INFO` line,
`the computer was asleep for 26h22m0s (or its clock jumped forward); probing the nodes again at once,
failures in the next 2 minutes are expected`, says so, every node is probed at once, and for the next
2 minutes the failures are `INFO` lines: a node that is down ends with `, right after the computer woke
up`, counts toward neither a burst nor flapping, and the per-minute connection lines are not `WARN`.
It reads the file, so it works while the angel runs and after it stopped or failed to start.
Every line is `{TIME} {LEVEL} {MESSAGE}` with the time in UTC (RFC 3339) and the
level `DEBUG`, `INFO`, `WARN` or `ERROR`. Lines of the older log format are read
as `INFO`, and a line that is not an entry (a panic trace) belongs to the entry
before it. The file is moved to `netivri.log.1` at the next start when it is
bigger than 8 MiB; `logs` reads the current file only.

| Flag | Meaning |
|---|---|
| `--follow` | Keep running and print new entries until interrupted; wait for the log when the angel has not started yet. |
| `--level L` | Only this level and above (default: `info`; `debug` also shows the repeated reports of nodes no account uses). |
| `--since D` | Only the last period: `90m`, `24h`, `7d`, ... (at most `365d`). |
| `--tail N` | At most the newest `N` entries (default `100`, `0` for all). With `--follow` it limits the backlog shown first. |

Without `--follow` the result is one document, oldest entry first:

```json
{"entries": [
  {"time": "2026-10-24T12:00:00Z", "level": "INFO", "message": "start pid=4242 listen=127.0.0.1:7890 route=direct"},
  {"time": "2026-10-24T12:05:10Z", "level": "WARN", "message": "subscription main: refresh failed: FETCH_HTTP_STATUS: unexpected HTTP status 403"}]}
```

With `--follow` the output is not a document but a stream: the entries as log
lines, or with `--json` one JSON object per line (`time`, `level`, `message`).
`time` is an empty string for a line that has none.

No log yet: `LOG_NOT_FOUND`, exit 5 (not with `--follow`, which waits). Invalid
`--level`, `--since` or `--tail`: usage error, exit 2.

### `diag dial`

```
netivri diag dial --account {ACCOUNT} | --node {NODE} [--samples {N}] [--idle {LIST|off}] [--max-nodes {N}] [--url {URL}]
```

Measures the dial phases of nodes (design: [design/latency.md](design/latency.md),
"per node takes N samples of `dns`, `tcp`, `tls`, `first_byte`"). It runs in
its own process without the angel; a running instance is not touched. Per
node it sends a handful of small HTTP requests (default 5 samples) through
the node and reports per-phase timings and failures; then the idle survival
test reuses the prepared transports after waiting 0.5 s, 1 s, 2 s, ... and
reports the success ratio per idle time and a suggested pool TTL. The idle
test waits up to the largest `--idle` value per node.

Exactly one of `--account` (the route nodes `up` would use, in route order)
or `--node {NODE}` (`{PROVIDER}/{NODE_ID}`, like the label of `stats`) is
required.

| Flag | Meaning |
|---|---|
| `--account {ACCOUNT}` | Diagnose the route nodes of this account (at most `--max-nodes` of them). |
| `--node {NODE}` | Diagnose this single node. |
| `--samples {N}` | Samples per node, 1..20 (default 5). |
| `--idle {LIST}` | Comma-separated idle times for the survival test (at most 10 positive durations like `500ms,2s`, sorted ascending), or `off` to skip it. Default: the built-in ladder. |
| `--max-nodes {N}` | Cap on the number of account route nodes, 1..50 (default 6). |
| `--url {URL}` | Probe URL, `http` only (default: the warm-up URL, `www.gstatic.com`). |

```json
{"url_host": "www.gstatic.com", "samples": 5, "nodes": [
  {"label": "main/a1b2c3", "transport_phases": true, "samples": 5,
   "phases_ms": {"dns": {"n": 5, "p50": 12, "p90": 30, "max": 41},
                 "tcp": {"n": 5, "p50": 20, "p90": 25, "max": 27},
                 "first_byte": {"n": 5, "p50": 84, "p90": 140, "max": 150}},
   "failures": {"dns": 0, "tcp": 0, "tls": 0, "first_byte": 1},
   "idle_survival": [{"idle_ms": 500, "ok": 5, "fail": 0}],
   "suggested_pool_ttl_ms": 250}]}
```

`phases_ms` carries only phases with samples (`tls` appears only for nodes
with transport phases); `idle_survival` and `suggested_pool_ttl_ms` are
omitted when there is no result. All durations are milliseconds with at
most one decimal. The output contains no node addresses: only node labels
and the probe URL host (`url_host`).

Errors: bad flags `USAGE` (exit 2); unknown account `ACCOUNT_NOT_FOUND`;
an account with neither provider nor exit country `NO_ROUTE`; unknown node
`NODE_NOT_FOUND` (all exit 5); a route without stored nodes
`NO_NODES_FOR_COUNTRY` (exit 5); `DIAG_FAILED` (exit 3) when the run itself
cannot proceed (for example a stored node mapping the proxy library rejects).
Failed requests are not errors: they are counted in `failures`.

### `doctor`

```
netivri doctor [--home {DIR} | --local]
```

Runs every check once and prints one document on stdout, in Ktav or with
`--json` in JSON. Each check has a `name`, a `status` (`ok`, `warn` or `fail`)
and a `detail`; `summary` counts them by status. stdout always carries the
document; when a check failed the error envelope goes to stderr and the exit
code is 7, so a script can branch on the exit code alone. A check that cannot
run is a `warn` with the reason, never a failure of the whole command; no
check contacts anything but the local angel and the local port, and
subscription URLs, passwords and hashes are never printed.

```json
{"checks": [
   {"name": "config", "status": "ok", "detail": "config.ktav loaded and valid"},
   {"name": "angel", "status": "ok", "detail": "running, pid 4242, uptime 3600s"},
   {"name": "listen", "status": "ok", "detail": "listen 127.0.0.1:7890, allowed_clients *"},
   {"name": "subscriptions", "status": "ok", "detail": "every configured subscription has a usable snapshot"},
   {"name": "account alice", "status": "ok", "detail": "tier 0 DE (main): 1/1 node(s) alive"},
   {"name": "log", "status": "ok", "detail": "no warn or error entries in the last hour"}],
 "summary": {"ok": 6, "warn": 0, "fail": 0},
 "home": "/home/me/.config/netivri", "home_source": "user config directory"}
```

`home` is the state directory the checks looked at and `home_source` how it was
chosen (see [Separate state directories](#separate-state-directories)).

The checks:

| Name | What it covers |
|---|---|
| `config` | `config.ktav` loads and is valid (the message names the bad key, never a secret). |
| `angel` | An angel runs for this state directory (pid, uptime). |
| `listen` | The address the running angel reports and its `allowed_clients` rule; when nothing runs, whether the configured port is free (a local probe). |
| `subscriptions` | Every configured subscription has a stored snapshot no older than twice `fetch.update_interval` (skipped when the interval is `off`), and every account names configured subscriptions only (a `fail` even while the angel is down). |
| `account {ACCOUNT}` | One per account: its route has stored nodes (`fail` otherwise, like `NO_NODES_FOR_COUNTRY`); with a running angel, a node in service in the first tier that has nodes (`warn` when only a fallback tier is in service, `fail` when none is; a node is in service unless the routing has taken it out, so a single failed probe does not count against it). Without an angel the stored nodes are counted and the liveness part is a `warn`; until the warm-up has probed the route it is a `warn` too. An account with neither provider nor exit country is `ok` ("direct"). |
| `log` | No `WARN` or `ERROR` entries in the last hour of the angel log (count and newest message otherwise). Reports of a node going down, the summary of a burst of them and the warning about a flapping node are only counted ("besides N node-down reports"): the `account` checks already say whether the routes have live nodes. The log is read even when the angel is down; a missing log file is `ok` ("no log yet"). |

Errors: `DOCTOR_FAILED` (exit 7) when at least one check failed; the document
on stdout says which.

### `account`

An account is a proxy login with an optional provider (a subscription name)
and exit country (the design is in [design/accounts.md](design/accounts.md)).

- `add` creates the account with the `init` password placeholder: the first
  client that authenticates with that login sets the password. `--provider
  {SUBSCRIPTION}` binds it to a provider (subscription), `--country
  {COUNTRY}` to an exit country. Each takes one value or **several separated
  by commas, in priority order** (`--provider main,backup --country
  DE,NL`; spaces around a comma are ignored, a repeated item counts once, the
  value is stored in this canonical form, with upper-case codes).
  `--clients {RULES}` restricts the addresses the account may authenticate
  from: one comma-separated allowed-clients rule list (same grammar as
  `listen.allowed_clients`; see [config.md](config.md), "Allowed clients").
  `--pre-socks5 {URL}` and `--post-socks5 {URL}` give the account its own
  proxy before and after the VPN node, each `{socks5|http|https}://[user:password@]host:port`
  with a required port or `none` (see `set-socks5`). Repeating
  `add` with the same providers and countries in the same order succeeds with
  `existing: true`; any difference, the order included, is `ACCOUNT_EXISTS`
  (exit 6).
- `clone {SOURCE} {ACCOUNT}` creates `{ACCOUNT}` with the provider, exit
  country, `allowed_clients`, `pre_socks5` and `post_socks5` of `{SOURCE}` and, by default, its password: the stored hash is copied,
  so both logins accept the same password (a source that still has the `init`
  placeholder gives a copy with the placeholder). `--password {PASSWORD}` gives
  the new account a password of its own (1 to 1024 bytes, hashed with argon2id
  like any other), and `--password init` leaves it to the first client that
  connects. `--clients {RULES}` replaces the copied `allowed_clients` with the
  same rules as `add`. `--provider` and `--country` replace the copied route with the same
  rules as `add`; a replaced route is checked like `set-country` checks it, and
  with no stored node nothing is created (`NO_NODES_FOR_COUNTRY`). Unlike
  `add`, an existing `{ACCOUNT}` is always `ACCOUNT_EXISTS` (exit 6), and an
  unknown `{SOURCE}` is `ACCOUNT_NOT_FOUND` (exit 5). The result carries
  `account` (as in `list`), `cloned_from` and `password` (`copied`, `new` or
  `init`); the password and its hash are never printed. A password given with
  `--password` is visible in the shell history and the process list: prefer
  `--password init` when that matters. `{SOURCE}` is not changed.
- `list` reports `login`, `provider`, `exit_country`, `allowed_clients`
  (empty when the account has no rule of its own), `pre_socks5` and
  `post_socks5` (the effective SOCKS5 proxy of each side, see `set-socks5`)
  and `password_set` (`false` until the placeholder is claimed); it never
  returns a password, a hash or the credentials of a SOCKS5 URL.
- `show {ACCOUNT}` reports one account: its login, its configured provider
  and `exit_country` lists, and its route as ordered tiers (countries in
  list order, inside a country the providers in list order, so a tier is the
  nodes of one (country, provider) pair; a `*` in the country list is the
  nodes of every country the list does not name, at its place in the order;
  without a provider list every provider shares one tier per country). Per
  node it shows the label, the display name when the subscription gives one,
  provider, country and tier index (from 0) and, while the
  angel runs, live data: alive or down with the last probe delay and the
  account's live connections through the node. Without a running angel
  `live` is false and every live field is null. With one, `alive` (the last
  probe succeeded) and `down` (the routing skips the node for new connections:
  a single failed probe makes it not alive but not down) and
  `delay_ms` are null for a node the angel's warm-up list does not hold, and
  `connections` is the account's live count (0 for a node it is not using)
  when the angel tracks the account, null when it does not. A (country,
  provider) pair without nodes is listed as a skipped tier with a reason; it
  keeps the index it would have had and does not consume one, so the indexes
  of the other tiers match the order the angel actually tries. A tier after
  the first one that has nodes carries `"fallback": true`, and the top-level
  `fallback_connections` is the number of the account's open connections on
  such tiers (null without a running angel): traffic sits there while the
  better tiers were down, and a site goes back for its next connection once a
  node of a better tier has been up for 5 minutes. `pre_socks5`
  and `post_socks5` give the effective SOCKS5 proxy of each side (see
  `set-socks5`); in `show` they also carry `own`, the account's own value
  (`none`, or a redacted URL), and no `own` when the account has none. The
  document:

  ```json
  {"login": "alice", "provider": "main,backup", "exit_country": "DE,*",
   "pre_socks5": {"value": "socks5://pre.example:1080", "source": "global", "auth": true},
   "post_socks5": {"value": "socks5://alice-exit.example:1080", "source": "account", "auth": false,
                   "own": "socks5://alice-exit.example:1080"},
   "providers": ["main", "backup"], "countries": ["DE", "*"], "live": true,
   "tiers": [
     {"tier": 0, "country": "DE", "providers": ["main"],
      "nodes": [{"label": "main/a1b2c3d4e5f6", "name": "Germany 1",
                 "provider": "main", "country": "DE", "tier": 0,
                 "alive": true, "delay_ms": 84, "connections": 2}]},
     {"tier": 1, "country": "DE", "providers": ["backup"], "skipped": true,
      "reason": "no nodes for this country and provider", "nodes": []}]}
  ``` Unknown login: `ACCOUNT_NOT_FOUND` (exit 5);
  malformed login or not exactly one argument: exit 2. Passwords, hashes and
  subscription URLs are never shown.
- `set-clients` takes one comma-separated allowed-clients rule list (same
  grammar as `listen.allowed_clients`: `*`, `loopback`, exact addresses,
  IPv4 whole-octet masks like `192.168.1.*`, CIDR prefixes; see
  [config.md](config.md), "Allowed clients"), or `--none` to clear the
  account's rule. A malformed list is `CONFIG_INVALID` (exit 2; the message
  names the bad item). Like `set-country` and `set-provider`, it changes
  several accounts at once: `{ACCOUNT}` may be several logins separated by
  commas with no spaces, or `--all` for every account (`ACCOUNT_NOT_FOUND`,
  exit 5, when a named login does not exist or there are no accounts;
  `ACCOUNT_INVALID`, exit 2, for a malformed login). The account rule only
  narrows what the global `listen.allowed_clients` already allows; a client
  allowed by neither is refused before the password is verified, so an
  `init` account cannot be claimed from a disallowed address. A running
  angel applies the change within a few seconds.
- `set-socks5` sets the SOCKS5 proxies of one or several accounts (`{ACCOUNT}`
  is one login or several separated by commas, as in `set-clients`):
  `--pre {VALUE}` is the proxy netivri connects through to reach the VPN node,
  `--post {VALUE}` the proxy the traffic goes to after the node. At least one
  of the two is required (exit 2 otherwise); a side that is not given stays as
  it is. `{VALUE}` is `{socks5|http|https}://[user:password@]host:port` (`http`
  is an HTTP CONNECT proxy, `https` the same over TLS, a host, a required port 1..65535, optional percent-encoded
  credentials, no path or query), or `none` to cancel the global value of that
  side for these accounts, or `inherit` to remove the account's own value so
  that the global `chain.pre_socks5` / `chain.post_socks5` of `config.ktav`
  applies again (see [config.md](config.md), "SOCKS5 chain", for the cascade).
  A malformed URL is `CONFIG_INVALID` (exit 2); the message names the flag and
  never contains the value, because it can carry credentials. Unknown login:
  `ACCOUNT_NOT_FOUND` (exit 5). The result is the single-account form
  `{"account": {...}}` for one login and `{"accounts": [...]}` for several
  (both as in `list`). A pre applies only to accounts with a route (a
  provider and/or an exit country); a post applies to every account, also to
  one without a route, whose traffic then goes to the post proxy directly. A
  running angel applies a change of an account's pre or post in place, within
  a few seconds, and the open connections go on.
- `set-country` takes an ISO 3166-1 alpha-2 code, or a comma-separated list of
  codes in priority order, or `--none` to clear it. A malformed code or list
  is `COUNTRY_INVALID`.
  A `*` item stands for every country the list does not name, nodes of an
  unknown country included, at its place in the order: `US,DE,*` is US, then
  DE, then everything else; `*` alone is any country. Quote it where the shell
  expands globs (`--country '*'`).
- `set-provider` binds the accounts to a provider or to a comma-separated list
  of providers in priority order, or `--none` clears it. Each provider must
  match `[A-Za-z0-9._-]{1,64}` (usage error otherwise) and be a configured
  subscription (`SUB_NOT_FOUND`, exit 5). A list holds at most 16 providers,
  a country list at most 32 codes.
- `set-country` and `set-provider` change several accounts at once: the first
  argument is one login or several separated by commas with no spaces
  (`alice,bob`), or `--all` stands for every account (`ACCOUNT_NOT_FOUND`,
  exit 5, when a named login does not exist or there are no accounts;
  `ACCOUNT_INVALID`, exit 2, for a malformed login). A login repeated in the
  list counts once.
- **Check before the change.** Before anything is written, every account
  whose provider or country would change is checked as `up` would check it:
  its route (the new providers, or its own, or every subscription when it
  has none, together with the new or its own countries) must have a stored
  node; one listed provider or country without nodes does not matter while
  another one has some. If one account fails, nothing is changed for any
  account and the
  command ends with `NO_NODES_FOR_COUNTRY` (exit 5; the message names the
  account, the hint points to `netivri sub countries` and `netivri sub
  update`). An account whose value stays the same is not checked, so repeating
  a command always succeeds. The nodes need not be alive.
- `rm` of an unknown login is `ACCOUNT_NOT_FOUND` (exit 5); it is not
  idempotent.
- `ACCOUNT_INVALID` and `COUNTRY_INVALID` are usage errors (exit 2).

```json
{"login": "alice", "provider": "main,backup", "exit_country": "DE,NL", "providers": ["main", "backup"], "countries": ["DE", "NL"], "live": false, "tiers": [{"tier": 0, "country": "DE", "providers": ["main"], "nodes": [{"label": "main/de-1", "provider": "main", "country": "DE", "tier": 0, "alive": null, "delay_ms": null, "connections": null}]}, {"tier": 1, "country": "DE", "providers": ["backup"], "nodes": [{"label": "backup/de-2", "provider": "backup", "country": "DE", "tier": 1, "alive": null, "delay_ms": null, "connections": null}]}, {"tier": 2, "country": "NL", "providers": ["main"], "nodes": [{"label": "main/nl-1", "provider": "main", "country": "NL", "tier": 2, "alive": null, "delay_ms": null, "connections": null}]}, {"tier": 3, "country": "NL", "providers": ["backup"], "skipped": true, "reason": "no nodes for this country and provider", "nodes": []}]}
{"account": {"login": "alice", "provider": "main", "exit_country": "DE", "allowed_clients": "", "pre_socks5": {"value": "none", "source": "none", "auth": false}, "post_socks5": {"value": "none", "source": "none", "auth": false}, "password_set": false}, "existing": false}
{"accounts": [{"login": "alice", "provider": "main", "exit_country": "DE", "allowed_clients": "", "pre_socks5": {"value": "none", "source": "none", "auth": false}, "post_socks5": {"value": "none", "source": "none", "auth": false}, "password_set": false}]}
{"account": {"login": "alice", "provider": "", "exit_country": "NL", "allowed_clients": "loopback", "pre_socks5": {"value": "none", "source": "none", "auth": false}, "post_socks5": {"value": "none", "source": "none", "auth": false}, "password_set": false}}
{"account": {"login": "alice", "provider": "main", "exit_country": "DE", "allowed_clients": "", "pre_socks5": {"value": "socks5://pre.example:1080", "source": "account", "auth": true}, "post_socks5": {"value": "none", "source": "none", "auth": false}, "password_set": false}}
{"removed": "alice"}
```

These are the `data` values, respectively, for add, list, set-country/
set-provider with one login, show, rm and, last, `set-socks5 alice --pre
socks5://user:secret@pre.example:1080 --post none` (the credentials are not
in the output; `auth` says the URL had some). `set-country` and `set-provider`
with
several logins or `--all` print the `{"accounts": [...]}` form of `list` with
the accounts that were addressed, in the order given (`--all`: config order).

Routing is applied by `up`. An account with providers and/or exit countries
needs stored subscription snapshots that hold matching nodes (providers set:
only those providers; countries set: only nodes of those countries); `up` does
not require those nodes to be alive. If no stored node matches at all, `up`
fails with `NO_NODES_FOR_COUNTRY` (exit 5, hint `netivri sub update`; the
message names the countries and/or the providers, never a URL) before binding
anything.

**Priority.** A route is a list of tiers tried in order, and the order is the
order of the lists. The countries come first: all the nodes of the first
country, then those of the second, and so on. Inside a country the providers
follow in their order, so `--provider main,backup --country DE,NL` tries
`main` in DE, `backup` in DE, `main` in NL, `backup` in NL. A tier is the
nodes of one (country, provider) pair, and a pair without nodes is skipped.
Without a provider list every provider shares one tier per country; without a
country list the tiers are the providers alone. A `*` in the country list is one more tier at its place: the nodes of every country the list does not name (so `DE,*` is DE first, then all the rest, and `*,DE` the rest first and DE last). A new connection goes to a
node of the first tier that has a node not marked down (the fastest of them,
by the usual latency choice), so a later tier is only a fallback; a site that
is already bound to a node keeps it while it is up, until a node of a better
tier has been up for 5 minutes (the site's next connection then goes back to
that tier; open connections stay). A route is used only for the accounts that name it: there is no
fallback to a direct connection or to a country or provider that is not in the
lists. Accounts without a provider and country, and
auth's account, take the default route, which is direct until a node is selected.
A running angel applies a change to accounts by itself, within a few seconds
(no `down` and `up` needed). A new country or provider list goes into the
running listener and the open connections go on; adding or removing an
account, a new password or a new `allowed_clients` restarts the listener on
the same port, which ends the open connections. A change of `chain.post_socks5`,
or a change that makes a route appear or disappear entirely, restarts the
listener as well; a new pre or post of an account and new nodes do not. A
change it cannot apply, such as a route without
nodes, leaves the running configuration in place and is reported as
`reload.last_error` by `status`. See [design/live-reload.md](design/live-reload.md)
and [design/socks5-chain.md](design/socks5-chain.md).

### `up`

Without `--foreground`, `up` starts the angel: it validates the flags and
the configuration, checks for an already running angel, and otherwise
spawns a detached background process and returns immediately. `up` is safe
to repeat.

```json
{"schema_version": 1, "ok": true,
 "data": {"angel": "running", "pid": 12345, "listen": "127.0.0.1:7890",
          "protocols": ["socks5", "socks4", "http"], "auth": "none",
          "foreground": false, "already_running": false}}
```

`foreground` is `true` for `up --foreground`, `false` for the angel.
`already_running` is `true` when the command found and reused a running
angel (idempotent success, exit 0); these keys are additive and do not
bump `schema_version`. When an angel is already running and the caller
explicitly passed a `--port` / `--bind` that differs from the running
listen address (an explicit `--port 0` means "don't care"), `up` fails with
`ALREADY_RUNNING` (exit 6, hint: `run netivri down first`).

### `up --foreground`

Runs the proxy in the foreground: prints one document on stdout (the same
shape as above, without `already_running`; one line with `--json`), then blocks until SIGINT /
SIGTERM or `netivri down`, then exits 0. It writes nothing to stderr. A
foreground `up` is not idempotent: with an angel already running in the same
home it fails with `ALREADY_RUNNING` (exit 6).

`up` also accepts the hidden flag `--handshake {PATH}` (only together with
`--foreground`). It is internal machinery of the angel spawn, not part of
this contract; see [design/angel.md](design/angel.md).

`listen` is the actual bound address: with `--port 0` it is the random free
port that was picked. `netivri status` reports `allowed_clients`, the
effective `listen.allowed_clients` value — which client addresses may
connect; `*` when unrestricted. The angel writes its state to `run/netivri.json` and
its log to `run/netivri.log` inside the state directory. The state file
holds the local control token: mode `0600` on Unix, profile-directory ACL on
Windows; never print it. `auth` is one of:

- `none` — no authentication configured (`auth.login` / `auth.password` empty
  and no `accounts`);
- `enabled` — every configured credential is an argon2id hash (`auth` and
  `accounts` alike);
- `init-pending` — the placeholder `init` is still set for `auth.login`,
  `auth.password` or the password of an account; the first client to
  authenticate claims the credential (see [docs/config.md](config.md), "The
  `init` placeholder").

Credentials, hashes and the config file content are never printed or logged.

### Separate state directories

Everything netivri keeps lives in one state directory: `config.ktav`, the
subscriptions with their URLs (credentials), `stats.db`, the site bindings and
`run/` with the angel's state file, control token and log. A second state
directory is a second, independent netivri: its own settings, tokens, nodes,
statistics and angel, nothing shared with the first. The command line picks the
directory, first match wins:

| # | Source | Directory |
|---|---|---|
| 1 | `--home {DIR}` | `{DIR}`, as given |
| 2 | `--local` | `./.netivri`, created if missing |
| 3 | nothing given, and `./.netivri` is a directory | `./.netivri` |
| 4 | `$NETIVRI_HOME` is set | its value |
| 5 | otherwise | `%APPDATA%\netivri`, `~/.config/netivri` or `~/Library/Application Support/netivri` |

`--home` and `--local` together are a usage error (exit 2). Only the current
directory is looked at in step 3, not its parents, and only a directory counts,
not a file with that name. `--local` creates `./.netivri` with access for the
owner only and a `.gitignore` that ignores everything in it (it holds
credentials); a directory that exists is left as it is. `up` records the
absolute path in the angel it starts, so the angel keeps using that directory
whatever the current directory of later commands is.

`status` and `doctor` print `home` (the directory) and `home_source` (`--home`,
`--local`, `./.netivri`, `NETIVRI_HOME` or `user config directory`), so it is
visible which one a command looked at, which matters once a `.netivri` is picked
up on its own.

```sh
cd ~/work/experiment
netivri up --local --port 0      # a separate angel; ./.netivri is created, any free port
netivri status                   # found through ./.netivri, no flag needed
netivri sub add https://provider.example/sub/{TOKEN} --name main
netivri down                     # stops this directory's angel only

netivri up --home /srv/netivri-a --port 7891   # an explicit directory, any place
netivri status --home /srv/netivri-a
```

A new state directory starts with the defaults, which include port 7890: when
another angel already listens there, give the new one `--port {PORT}` (0 takes
any free port, and `status` shows the address) or set `listen.port` in its
`config.ktav`.

### Flags vs configuration

`up` reads `config.ktav` from the state directory (see [Separate state
directories](#separate-state-directories); [docs/config.md](config.md)). A missing file is
not an error: defaults apply, no proxy authentication, and **no config file
is created**. Explicit `--port` / `--bind` flags override `listen.port` /
`listen.bind`; otherwise the config values are used. Without a config the
defaults stay `127.0.0.1:7890`. A `--bind` that is neither an IP address nor
`localhost` is a usage error. `listen.allowed_clients` from `config.ktav`
applies at start and is picked up by the live reload.

### The dual-protocol port

`up` exposes one local TCP port that speaks both proxy protocols on the same
socket. The protocol is auto-detected from the first byte of each connection:

| First byte | Protocol |
|---|---|
| `0x05` | SOCKS5 |
| `0x04` | SOCKS4 / SOCKS4a |
| anything else | HTTP proxy (plain requests and `CONNECT`) |

Default bind is `127.0.0.1:7890`. TCP only in v1: SOCKS5 `UDP ASSOCIATE` is
accepted but no relay is set up, so UDP through the proxy does not work.

A non-loopback `--bind` is rejected with `AUTH_REQUIRED` unless proxy
authentication is enabled in the configuration: `auth.login` and
`auth.password` set, or at least one account. While any credential is still
the `init` placeholder (the legacy `auth.login`/`auth.password` or the
password of an account) it counts only with `auth.allow_init_remote: true`.
Loopback means `127.0.0.0/8`, `::1` and `localhost`.

### `status`

Reports the live status of the running instance. `home` and `home_source`
say which state directory it asked (see [Separate state
directories](#separate-state-directories)).

The default answer is **short**: enough for people and scripts that want to
know the angel is up and what it serves. The complete view — with the warm
node list, the warm pool counters, `stats` and `affinity` — needs
`netivri status --full`. Both forms accept `--home` and `--json`; unknown
flags are usage errors.

Short view:

```json
{"schema_version": 1, "ok": true,
 "data": {"angel": "running", "foreground": false, "pid": 12345,
          "listen": "127.0.0.1:7890",
          "protocols": ["socks5", "socks4", "http"], "auth": "none",
          "allowed_clients": "*", "home": "/home/me/.config/netivri",
          "home_source": "user config directory",
          "accounts": [], "route": "direct", "started_at": "2026-10-20T10:00:00Z",
          "uptime_seconds": 3721, "version": "0.1.0",
          "active_accounts": 0, "activity": [],
          "warm": {"alive": 4, "total": 5, "probes": 12, "probe_failures": 1},
          "refresh": {"interval_seconds": 3600, "ok": 3, "failed": 0,
                      "last_at": "2026-10-20T10:45:00Z", "last_error": ""},
          "reload": {"count": 1, "last_at": "2026-10-20T10:02:00Z", "last_error": ""}}}
```

Exactly these fields exist in the short view: `angel` (always `running` on success),
`foreground`, `pid`, `listen`, `protocols`, `auth`, `route` (`direct` or
`node`), `started_at` (RFC3339 UTC), `uptime_seconds` (whole seconds),
`version` (the angel binary's version), `home` and `home_source` (the state
directory this command asked and how it was chosen, see [Separate state
directories](#separate-state-directories)), `allowed_clients` (which client
addresses may connect; `*` when unrestricted), `accounts` (see `account`; each
record is `{"login", "provider", "exit_country", "allowed_clients", "chain"}`,
sorted by login; `chain` is `{"pre_socks5", "pre_socks5_source",
"pre_socks5_auth", "post_socks5", "post_socks5_source", "post_socks5_auth"}`,
the proxies of the running core (`socks5://`, `http://` or `https://`),
redacted to `scheme://host:port` or `none`, `source` being `account`, `global` or `none` and `auth` whether the URL
had credentials), `active_accounts`, `activity`, the summary form of `warm`, `refresh`
and `reload`. The full view (`--full`) adds, next to everything above: the full
`warm` (the pool counters and `nodes[]`), `stats` and `affinity`. The selection
fields of the planned shape below are not included
yet; they arrive with `netivri use`.

Scripts that read `warm.nodes`, `stats` or `affinity` must pass `--full`.

`refresh` (`{"interval_seconds", "ok", "failed", "last_at", "last_error"}`) and
`reload` (`{"count", "last_at", "last_error"}`) describe how the angel keeps
itself up to date. The angel re-fetches every subscription once per
`fetch.update_interval` (one hour by default, `interval_seconds` is `0` when
it is `off`; see [config.md](config.md)); `ok` and `failed` count those
refreshes, `last_at` is the time of the latest one (RFC3339 UTC) and
`last_error` its failure (`CODE: message`, never a URL), empty after a success.
A fetch that fails on the way (a timeout, a reset) is tried once more after 5
seconds; a failed refresh is tried again after 10 minutes (sooner than the
interval). While the stored list is younger than twice the interval, the failure
is an `INFO` line (`subscription main: refresh failed after 30.1s (two tries):
FETCH_FAILED: ...; the list of 41m ago stays, next try in 10m`); it is a `WARN`
with the third failure in a row, with a list older than twice the interval, or
without any list, so `doctor` warns only for a failure that lasts. A successful
refresh says how long it took (`refreshed, 22 nodes in 4.2s`). A fetched list
that would leave an account without nodes is rejected and the old list stays. `reload.count` is how many times the angel applied changed
files: new nodes and routes in place, keeping the open connections, and
accounts, passwords or the port by restarting its listener; `last_at` the
latest of them, and `last_error` why the newest files are not applied
(an invalid `config.ktav`, a route without nodes); it is empty when the files
on disk are what runs. A refresh that brings the same nodes, or only different
node names, changes nothing and does not count.

`stats` (`{"path", "dropped"}`) is full-only and present while the statistics
store is open;
`dropped` counts samples lost to a full write queue. `affinity`
(`{"bindings", "rebinds", "returns", "hedges", "hedge_wins", "restored", "saved_at"}`) is
also full-only and present while an account has a route: the number of
site-to-node bindings held now, how many times a binding moved to another node
because its node was down, how many times a binding went back to a better tier
(`returns`, see `docs/design/node-stats.md`), how many second dial attempts (to the same node)
were started for a slow handshake and how many dials such an attempt won.
`restored` is how many persisted bindings were loaded when the core last
started (an angel start or a reload); the angel stores the bindings in the
statistics database by node label every 30 seconds and once more before it
closes, so a site keeps its exit node across restarts and reloads. `saved_at`
is the time of the last successful write (RFC3339 UTC), omitted until one
succeeds (see [design/node-stats.md](design/node-stats.md)).

`activity` and `active_accounts` answer "who is using the proxy now, and
through what". `activity` has one record per account, sorted by login, and
`active_accounts` counts those with open connections:

```json
{"active_accounts": 1,
 "activity": [
  {"login": "alice", "active": true, "connections": 3,
   "upload_bytes": 20480, "download_bytes": 1048576,
   "providers": ["main"], "countries": ["DE", "NL"],
   "nodes": [
     {"node": "main/a1b2c3", "name": "Germany 1", "provider": "main", "country": "DE",
      "connections": 2, "upload_bytes": 16384, "download_bytes": 917504},
     {"node": "main/d4e5f6", "provider": "main", "country": "NL",
      "connections": 1, "upload_bytes": 4096, "download_bytes": 131072}]},
  {"login": "bob", "active": false, "connections": 0,
   "upload_bytes": 0, "download_bytes": 0,
   "providers": [], "countries": [], "nodes": []}]}
```

An account is `active` while it has open connections. `nodes`, `providers`
(the subscriptions) and `countries` list what those connections use right now,
the busiest node first, each provider and country once, in the order of the
nodes; they are empty for an idle account, and the byte counts are those of the
open connections, not totals. `node` is the label of `node list` and `stats`
(`{PROVIDER}/{NODE_ID}`); `name` is the display name the subscription gives the node,
cleaned of control characters and capped at 64 characters, and is omitted when
the node has none. Traffic of an account without a route is `direct`
(or `node` when the whole instance goes through one node), with no provider
and country. `activity` is present while the listener runs. The configured
provider and exit-country lists stay in `accounts`.

#### `status --watch`

`netivri status --watch [--interval {DURATION}]` keeps running and prints one
frame per tick (default every 2s, `--interval` between 1s and 60s, Go
durations) until it is interrupted or the angel stops. Each frame is the
activity view: one row per account, sorted by login, with the state
(`active`/`idle`), the open connections, upload and download per second
(the delta of the byte counters of two polls over the real elapsed time,
`B/s`, `KiB/s` or `MiB/s`; blank on the first frame and `0` after a negative
delta, which an angel restart or closed connections causes), the busiest
nodes (at most 3, then `+N more`) and the countries. A header line carries
the listen address, the uptime and `active_accounts`.

On a terminal every frame clears the screen first; otherwise frames are plain
text separated by a blank line, so a redirect keeps every frame. The text
frame is ASCII: a node display name that is not ASCII shows with every
non-ASCII rune as `?` (`??? 1`); `--json` keeps the original name.

`netivri status --watch --json` prints one JSON object per line instead:

```json
{"time": "2026-10-25T12:00:02Z", "active_accounts": 1,
 "accounts": [{"login": "alice", "active": true, "connections": 2,
               "up_bytes_per_s": 2048.0, "down_bytes_per_s": 524288.0,
               "nodes": [{"node": "main/a1b2c3", "name": "Germany 1",
                          "provider": "main", "country": "DE", "connections": 2,
                          "upload_bytes": 4096, "download_bytes": 1048576}]}]}
```

`up_bytes_per_s` and `down_bytes_per_s` are `null` on the first frame.

At the start a not-running angel is the usual `ANGEL_NOT_RUNNING` error,
exit 4. If the angel stops while watching, the watch prints a final line
(`angel stopped`, or `{"stopped": true, "time": ...}` with `--json`) and
ends with `ANGEL_NOT_RUNNING`, exit 4. "Stopped" means the state file is
gone, the control channel answers for another pid, or nothing listens on
the control port (connection refused: a crashed angel leaves its state
file behind). Timeouts and every other error are only a warning line and
are retried on the next tick, and three consecutive failures end the watch
with `INTERNAL`, exit 1. The same retry rule covers a failed first poll;
the watch never removes the state file. `--watch` cannot be combined with
`--full`, and `--interval` without `--watch` is a usage error.

`warm` is present while the angel keeps country nodes warm (an account has an
exit country): background probes of every node proxy through the node itself,
every 45 seconds (a node that stays down is probed less and less often:
45 s, 90 s, 3 m, then every 10 minutes, until a probe or a real dial
succeeds again). The short view carries only the summary
`{"alive", "total", "probes", "probe_failures"}`: how many of the `total`
nodes' last probe succeeded, and how many probes ran and failed. With
`--full`, `warm` has the counters
`{"probes", "probe_failures", "pool_hits", "pool_misses", "pool_discards",
"nodes": [{"name", "alive", "down", "delay_ms", "label", "provider", "country",
"node_name"}]}`. `down` is the routing state (omitted when unknown): a node whose last
probe failed once is not `alive` but not `down` either. `nodes[].name` is an internal proxy
name (`netivri-r{R}-{N}`, the route and the node index), never a provider name; `delay_ms` is `0` until a
probe succeeded. `label` is `{PROVIDER}/{NODE_ID}`, the label of `node list`
and `stats`; `provider` and `country` name the node's subscription and exit
country; `node_name` is the display name the subscription gives the node,
cleaned of control characters and capped at 64 characters, and is omitted when
the node has none. An instance without account routes has no labels, and
`label` is omitted on every node. The pre-dial pool is off in the angel (`pool_*` stay `0`).
The probes request `http://www.gstatic.com/generate_204` through each node. A probe that
fails is tried once more at once, against `http://cp.cloudflare.com/generate_204` and
with twice the timeout (10 s); only a failure of both counts in `probe_failures`, makes
the node count toward going down and shows in `stats` uptime. One slow or unreachable
target does not make a working node look down.

When no angel is running: error `ANGEL_NOT_RUNNING` on stderr, exit 4
(start it with `netivri up`). A stale state file (dead pid, no live control
answer) counts as not running and is removed.

Planned shape once node selection and subscriptions are wired in (additive
to the fields above):

```json
{
  "node": {"id": "a1b2c3", "name": "🇩🇪 Germany 1", "protocol": "vless",
           "exit_country": "DE", "exit_ip": "203.0.113.7", "latency_ms": 48},
  "mode": "fastest",
  "country": "DE",
  "subscriptions": [
    {"name": "main", "nodes": 24, "updated_at": "2026-10-19T10:00:00Z",
     "upload": 1073741824, "download": 5368709120, "total": 107374182400,
     "expire": "2026-11-19T00:00:00Z"}
  ]
}
```

### `down`

Stops the angel through its control channel and waits until it is gone.
Idempotent.

```json
{"schema_version": 1, "ok": true,
 "data": {"angel": "stopped", "was_running": true, "pid": 12345}}
```

With nothing running: `{"angel": "stopped", "was_running": false}`, exit 0.
`pid` is present only when something was running. If the angel does not
stop within 15 seconds: `ANGEL_STOP_TIMEOUT` (exit 1). `down` never kills
by pid.

### `node list` (after angel integration)

```json
{"nodes": [
  {"id": "a1b2c3", "name": "🇩🇪 Germany 1", "sub": "main", "protocol": "vless",
   "country": "DE", "country_source": "exit",
   "latency_ms": 48, "alive": true, "tested_at": "2026-10-19T10:05:00Z"}
]}
```

Until the angel serves health checks, `node list` prints the stored-node
shape documented below.

`country_source` is one of `exit` (checked through the node), `geoip` (GeoIP
of the server address, i.e. the entry point) or `name` (parsed from the node
name). Prefer `exit`.

### `sub add`

```
netivri sub add {URL} [--name {NAME}] [--via {PROXY}]
```

Fetches the subscription through `--via` if given, otherwise through
`fetch.proxy` from `config.ktav`; strict: with no proxy the fetch is
direct and environment proxies are never consulted. The body is parsed
and stored; only then is the subscription appended to the config. A
missing config file is created by the first successful add. If anything
fails, nothing is added and the config file is not touched.

Idempotent by URL: an already configured URL is re-fetched and re-stored
under its existing name (a given `--name` is ignored) and reported with
`"existing": true`. A different URL with an already used explicit
`--name` is `SUB_EXISTS_NAME`. Without `--name` the name is derived from
the URL host (lower-case, characters outside `A-Za-z0-9._-` become `-`,
at most 64 characters, made unique with a numeric suffix).

```json
{"name": "provider.example", "added": true, "existing": false,
 "nodes": 24, "skipped": 2, "countries": {"DE": 12, "NL": 8, "unknown": 4}}
```

`skipped` counts banner and invalid entries dropped by the parser; nodes
without an inferable country are counted under `"unknown"`.

### `sub list`

```json
{"subscriptions": [
  {"name": "main", "host": "provider.example", "via_set": true,
   "nodes": 24, "fetched_at": "2026-10-19T10:00:00Z",
   "quota": {"used": 123, "total": 107374182400, "expire": "2026-12-31T00:00:00Z"}},
  {"name": "backup", "host": "other.example", "via_set": false,
   "nodes": null, "fetched_at": null, "quota": null}
]}
```

The URL and the via proxy are never printed: only the host of the
subscription URL and a `via_set` boolean. `nodes` / `fetched_at` are
`null` when no snapshot is stored for the subscription. `quota` carries
what the provider reports in its `subscription-userinfo` header: `used`
(upload plus download bytes), `total` (bytes, `null` when the provider
sends none) and `expire` (RFC3339 UTC, `null` when absent). It is `null`
when the provider sends no such header.

### `sub update`

```
netivri sub update [{SUBSCRIPTION} | --all]
```

Re-fetches and re-stores; with a name only that subscription, with
`--all` or no argument all subscriptions in config order. A failed
subscription keeps its old stored snapshot. A running angel does the same by
itself once per `fetch.update_interval` and applies the stored result to its
routes within a few seconds, so this command is for refreshing on demand.

```json
{"results": [
  {"name": "main", "ok": true, "nodes": 24, "skipped": 1, "error": null,
   "quota": {"used": 123, "total": 107374182400, "expire": "2026-12-31T00:00:00Z"}},
  {"name": "backup", "ok": false, "nodes": null, "skipped": null, "quota": null,
   "error": {"code": "FETCH_HTTP_STATUS", "message": "unexpected HTTP status 403"}}
]}
```

`quota` carries what the provider reports in its `subscription-userinfo`
header: `used` (upload plus download bytes), `total` (bytes, `null` when
the provider sends none) and `expire` (RFC3339 UTC, `null` when absent).
It is `null` when the provider sends no such header or the fetch failed.

Exit 0 when all succeeded. When any subscription fails, the results
object is still printed on stdout and the command ends with
`SUB_UPDATE_FAILED` (exit 3) on stderr.

### `sub rm`

```json
{"removed": "backup"}
```

Removes the subscription from the config and deletes its stored
snapshot. `SUB_NOT_FOUND` when the name is not configured.

### `node list`

```
netivri node list [--sub {SUBSCRIPTION}] [--country {COUNTRY}] [--alive]
```

Prints the stored nodes of all configured subscriptions (or only
`--sub {SUBSCRIPTION}`), in a stable order: subscription order, then provider order.
`--country` filters case-insensitively by ISO 3166-1 alpha-2 code parsed
from the node names. `--alive` is reserved for future health checks and
is rejected as a usage error for now.

```json
{"nodes": [
  {"id": "a1b2c3d4e5f6", "name": "🇩🇪 Frankfurt 1", "type": "ss",
   "server": "provider.example", "port": 8388, "country": "DE", "sub": "main"}
], "count": 24}
```

Raw proxy mappings (credentials) are never printed.

## Exit codes

| Code | Meaning |
|---|---|
| 0 | Success |
| 1 | Unexpected internal error |
| 2 | Invalid arguments or usage |
| 3 | Network error (subscription fetch, node test) |
| 4 | Angel not running (for commands that need it) |
| 5 | Not found (subscription, node, country with no nodes) |
| 6 | Conflict (port in use, angel already running with other settings) |
| 7 | `doctor`: at least one check failed (the document on stdout says which) |

## Error codes

| Code | Exit | Meaning |
|---|---|---|
| `USAGE` | 2 | Bad flags or arguments |
| `AUTH_REQUIRED` | 2 | Non-loopback `--bind` rejected because no proxy authentication is configured |
| `FETCH_URL_INVALID` | 2 | Subscription or proxy URL is not a valid http(s)/socks5(h) URL |
| `FETCH_PROXY_FAILED` | 3 | The configured proxy could not be reached or negotiated with |
| `FETCH_FAILED` | 3 | Request to the subscription server failed |
| `FETCH_HTTP_STATUS` | 3 | Subscription URL returned a non-2xx status |
| `SUBSCRIPTION_EMPTY` | 3 | No valid nodes in the subscription body |
| `SUBSCRIPTION_TOO_LARGE` | 3 | Subscription body exceeds 16 MiB |
| `SUBSCRIPTION_TOO_MANY` | 3 | Subscription has more than 5000 nodes |
| `SUB_NOT_FOUND` | 5 | No subscription with that name |
| `ACCOUNT_INVALID` | 2 | Invalid account login |
| `COUNTRY_INVALID` | 2 | Invalid country code |
| `CONFIG_INVALID` | 2 | `config.ktav` is not valid (for example a bad port, an account password that is neither `init` nor an argon2id hash, an empty `listen.allowed_clients`), or an allowed-clients rule list given to `account add --clients`, `account set-clients` or `account clone --clients` is not valid; the message names the bad item |
| `ACCOUNT_NOT_FOUND` | 5 | No account with that login |
| `ACCOUNT_EXISTS` | 6 | Account already exists with conflicting settings; `account clone` onto an existing login |
| `SUB_EXISTS_NAME` | 6 | `sub add --name` already used by a different URL |
| `SUB_IN_USE` | 6 | `sub rm` of a subscription that is the provider, or one of the providers in the list, of an account (nothing is removed; hint: `netivri account set-provider {ACCOUNT} --none`) |
| `SUB_UPDATE_FAILED` | 3 | At least one subscription failed to update (results still printed on stdout) |
| `SUB_SNAPSHOT_INVALID` | 1 | A stored snapshot exists but is corrupt or unreadable (a missing one is not an error); `sub update` rewrites it |
| `NODE_NOT_FOUND` | 5 | No node with that id or name |
| `NODE_INVALID` | 2 | A stored node cannot be used by the core (unsupported type or a malformed mapping); the message names only the type and the server shape, never a secret |
| `NO_NODES_FOR_COUNTRY` | 5 | `up`: an account has no route because no stored node matches its provider and/or country (a bound provider that is not configured or holds no snapshot counts too); the nodes need not be alive. `account set-country` and `account set-provider` return it, before writing anything, when an account would be left in that state |
| `NO_ROUTE` | 5 | `diag dial`: the account has no route (neither provider nor exit country) |
| `DIAG_FAILED` | 3 | `diag dial`: the run could not proceed (for example an unusable node mapping) |
| `NODE_UNREACHABLE` | 3 | Test through the node failed |
| `ANGEL_NOT_RUNNING` | 4 | Run `netivri up` first |
| `STATS_UNAVAILABLE` | 5 | `stats`: no statistics database exists yet (nothing was recorded) |
| `LOG_NOT_FOUND` | 5 | `logs`: the angel has not written a log yet |
| `ALREADY_RUNNING` | 6 | An angel is already running; `up` requested conflicting settings (or was `--foreground`) |
| `ANGEL_START_FAILED` | 1 | The angel child exited or failed before reporting startup; details in `run/netivri.log` of the state directory |
| `ANGEL_START_TIMEOUT` | 1 | The angel did not report startup within 15 s |
| `ANGEL_STOP_TIMEOUT` | 1 | The angel did not stop within 15 s after `down` |
| `PORT_IN_USE` | 6 | Listen port is taken |
| `DOCTOR_FAILED` | 7 | `doctor`: at least one check failed (the document is still printed on stdout) |
| `INTERNAL` | 1 | Bug; details in `netivri logs` |

## Idempotency

- `sub add` with an existing URL returns the existing subscription, exit 0.
- `up` when the angel already runs is idempotent: it reports the running
  instance with `already_running: true`, exit 0. Explicit `--port` / `--bind`
  flags that differ from the running listen address are `ALREADY_RUNNING`
  (exit 6); `--port 0` explicitly means "don't care". `up --foreground` is
  not idempotent and always fails with `ALREADY_RUNNING` (exit 6) when a
  angel runs.
- `down` when not running: exit 0.
- `status` when not running: `ANGEL_NOT_RUNNING`, exit 4.
- `use` with the current selection: exit 0.
- `account add` with the same login, provider and country, repeated `account
  set-country` with the same country and repeated `account set-provider` with
  the same provider: exit 0; `account clone`, `account rm` and `sub rm` are not
  idempotent.

## Security

- The proxy binds to `127.0.0.1` by default. Binding elsewhere requires
  configured proxy authentication (`auth.login` / `auth.password` in
  `config.ktav`; the password may be `init`), otherwise `up` exits with
  `AUTH_REQUIRED`.
- Proxy authentication guards who may **use** the proxy; `listen.allowed_clients`
  (and the per-account `allowed_clients`) guard which addresses may **connect at
  all**. A refused connection is closed before any handshake, before
  authentication, and an account rule is enforced before a password is verified
  or an `init` placeholder claimed (see [config.md](config.md), "Allowed
  clients").
- The control channel is a loopback HTTP server (`127.0.0.1` only, random
  port) guarded by a bearer token that lives only in
  `{HOME}/run/netivri.json` (mode `0600` on Unix). Every request must carry
  the token; the Host header must match the listener. No remote control API.
- The angel log (`{HOME}/run/netivri.log`, read with `netivri logs`) records
  lifecycle and housekeeping lines only: start and stop with pid, listen
  address and reason, applied and rejected configuration changes, subscription
  refreshes. No tokens, no credentials, no subscription URLs.
- Subscription URLs contain secrets: they are stored in the state directory
  only and are redacted in all output (`https://provider.example/sub/****`).
- The statistics database holds, besides traffic counters, the persisted
  site bindings: which sites (hostnames) each account visited and when.
  They stay in `{HOME}/stats.db` only, expire with the binding lifetime
  (24 hours idle; stale rows are deleted on every write and load) and are
  never printed or logged: `status` reports only counts and times,
  `netivri stats` has no binding view, and the angel log names no sites.
