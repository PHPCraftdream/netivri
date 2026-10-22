# CLI contract

This document is the contract between `netivri` and its callers (humans,
scripts, AI agents). Changing anything here is a breaking change and bumps
`schema_version`.

## Global flags

| Flag | Meaning |
|---|---|
| `--json` | Write results and errors as JSON instead of Ktav, the default (see [Output](#output)). Env: `NETIVRI_OUTPUT=json`. |
| `--home {DIR}` | State directory. Default: `%APPDATA%\netivri` / `$XDG_CONFIG_HOME/netivri`. Env: `NETIVRI_HOME`. |
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
netivri node test [{NODE} | --all] [--exit] [--timeout {DURATION}]

netivri account add {ACCOUNT} [--provider {SUBSCRIPTION}] [--country {COUNTRY}]
netivri account list
netivri account set-country {ACCOUNT}[,{ACCOUNT}...] {COUNTRY} | --all {COUNTRY}
netivri account set-country {ACCOUNT}[,{ACCOUNT}...] --none | --all --none
netivri account set-provider {ACCOUNT}[,{ACCOUNT}...] {SUBSCRIPTION} | --all {SUBSCRIPTION}
netivri account set-provider {ACCOUNT}[,{ACCOUNT}...] --none | --all --none
netivri account rm {ACCOUNT}

netivri stats [--sub {SUBSCRIPTION}] [--country {COUNTRY}] [--node {NODE}] [--since {DURATION}] [--by {node|country|provider}]

netivri diag dial --account {ACCOUNT} | --node {NODE} [--samples {N}] [--idle {LIST|off}] [--max-nodes {N}] [--url {URL}]

netivri use {NODE}
netivri use --country {COUNTRY} [--fastest | --auto]
netivri use --fastest | --auto            --auto keeps re-selecting by latency

netivri up [--foreground] [--port {PORT}] [--bind {ADDR}] [--home {DIR}]
netivri down
netivri status

netivri logs [--follow] [--level {debug|info|warn|error}] [--since {DURATION}]
netivri doctor
netivri schema                            this contract as JSON
netivri version
netivri help [{COMMAND} [{SUBCOMMAND}]]   also: netivri, netivri {COMMAND} --help
```

Placeholders are written in curly braces, here and in the help of the
program. `{ACCOUNT}` is the login of an account (`alice`) and `{SUBSCRIPTION}`
the name of a subscription (`main`). `{NODE}` is a node id (stable hash) or an
exact node name. `{COUNTRY}` is an ISO 3166-1 alpha-2 country code (`DE`, `NL`,
`FI`). `{URL}`, `{PROXY}`, `{DIR}`, `{ADDR}`, `{PORT}`, `{N}`, `{DURATION}` and
`{LIST}` stand for a value of the kind they name. Brackets mean optional, a
bar means one of, and `{a|b}` is a choice written out.

## Output

Every result and every error is one document with the same keys, in one of two
formats. The default is [Ktav](https://github.com/ktav-lang), the format of the
configuration file; `--json` (anywhere among the arguments, before a bare `--`)
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
once. If the Ktav native library cannot be loaded (`KTAV_LIB_MISSING`), the
document is written as JSON instead, so that an error is never lost.

Subscription URLs are credentials and are never printed: not on stdout,
not on stderr, not in error messages. The same holds for `--via` proxies.

`netivri logs --follow --json` is the only streaming command: one JSON object
per line (NDJSON).

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

Help is text for people unless `--json` is given, whatever `NETIVRI_OUTPUT`
says, so `netivri help | less` works. The text is ASCII only and not part of
the contract; the JSON is.

```json
{"kind": "overview", "summary": "...", "description": "...", "usage": ["..."],
 "quick_start": ["..."], "planned": ["use", "node test", "logs", "doctor", "schema"],
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
that database read-only, so it works while the daemon runs and after it
stopped.

| Flag | Meaning |
|---|---|
| `--sub {SUBSCRIPTION}` | Only the nodes of this subscription (provider). |
| `--country {COUNTRY}` | Only the nodes of this country. |
| `--node {NODE}` | Only this node. A label is `{PROVIDER}/{NODE_ID}`, like the `sub` and `id` of `node list`. |
| `--since D` | Window: `90m`, `24h`, `7d`, ... (default `24h`, at most `365d`). |
| `--by G` | `node` (default), `country` or `provider`. |

```json
{"since": "24h0m0s", "by": "node", "items": [
  {"label": "main/a1b2c3", "provider": "main", "country": "DE",
   "probes": 120, "uptime_pct": 99.2,
   "delay_ms": {"avg": 84, "p50": 80, "p95": 140},
   "dials": 340, "dial_ok_pct": 99.1, "dial_ms_avg": 96,
   "throughput_kbps": {"p50": 1200, "max": 8400},
   "bytes": 123456789,
   "last_seen": "2026-10-20T10:00:00Z", "last_ok": "2026-10-20T10:00:00Z"}]}
```

`uptime_pct` is the share of successful probes and `dial_ok_pct` the share of
successful dials of real connections; both are `null` without samples.
`delay_ms` covers successful probes, `dial_ms_avg` successful dials, and
`throughput_kbps` the per-window rates in KiB/s. Windows longer than the raw
retention (7 days) are answered from hourly summaries: percentiles and
throughput are then `0`.

With `--by country` or `--by provider` each item is one group (`country`,
with `unknown` for nodes without one, or `provider`) with the number of
`nodes`, summed counters and averages weighted by the number of successful
samples; percentiles do not merge and are left out, `throughput_kbps` carries
only `max`. Items are sorted by label or group name.

When no database exists yet: `STATS_UNAVAILABLE`, exit 5. Invalid `--since`,
`--by` or `--country`: usage error, exit 2.

### `diag dial`

```
netivri diag dial --account {ACCOUNT} | --node {NODE} [--samples {N}] [--idle {LIST|off}] [--max-nodes {N}] [--url {URL}]
```

Measures the dial phases of nodes (design: [design/latency.md](design/latency.md),
"per node takes N samples of `dns`, `tcp`, `tls`, `first_byte`"). It runs in
its own process without the daemon; a running instance is not touched. Per
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

### `account`

An account is a proxy login with an optional provider (a subscription name)
and exit country (the design is in [design/accounts.md](design/accounts.md)).

- `add` creates the account with the `init` password placeholder: the first
  client that authenticates with that login sets the password. `--provider
  {SUBSCRIPTION}` binds it to a provider (subscription). Repeating `add` with
  the same provider and country succeeds with `existing: true`; any
  difference is `ACCOUNT_EXISTS` (exit 6).
- `list` reports `login`, `provider`, `exit_country` and `password_set`
  (`false` until the placeholder is claimed); it never returns a password or
  a hash.
- `set-country` takes an ISO 3166-1 alpha-2 code or `--none` to clear it.
- `set-provider` binds the accounts to a provider, or `--none` clears it. A
  provider must match `[A-Za-z0-9._-]{1,64}` (usage error otherwise) and be
  a configured subscription (`SUB_NOT_FOUND`, exit 5).
- `set-country` and `set-provider` change several accounts at once: the first
  argument is one login or several separated by commas with no spaces
  (`alice,bob`), or `--all` stands for every account (`ACCOUNT_NOT_FOUND`,
  exit 5, when a named login does not exist or there are no accounts;
  `ACCOUNT_INVALID`, exit 2, for a malformed login). A login repeated in the
  list counts once.
- **Check before the change.** Before anything is written, every account
  whose provider or country would change is checked as `up` would check it:
  its route (the new provider, or its own provider, or every subscription when
  it has none, together with the new or its own country) must have a stored
  node. If one account fails, nothing is changed for any account and the
  command ends with `NO_NODES_FOR_COUNTRY` (exit 5; the message names the
  account, the hint points to `netivri sub countries` and `netivri sub
  update`). An account whose value stays the same is not checked, so repeating
  a command always succeeds. The nodes need not be alive.
- `rm` of an unknown login is `ACCOUNT_NOT_FOUND` (exit 5); it is not
  idempotent.
- `ACCOUNT_INVALID` and `COUNTRY_INVALID` are usage errors (exit 2).

```json
{"account": {"login": "alice", "provider": "main", "exit_country": "DE", "password_set": false}, "existing": false}
{"accounts": [{"login": "alice", "provider": "main", "exit_country": "DE", "password_set": false}]}
{"account": {"login": "alice", "provider": "", "exit_country": "NL", "password_set": false}}
{"removed": "alice"}
```

These are the `data` values, respectively, for add, list, set-country/
set-provider with one login, and rm. `set-country` and `set-provider` with
several logins or `--all` print the `{"accounts": [...]}` form of `list` with
the accounts that were addressed, in the order given (`--all`: config order).

Routing is applied by `up`. An account with a provider and/or an exit country
needs stored subscription snapshots that hold matching nodes (provider set:
only that provider; country set: only nodes of that country); `up` does not
require those nodes to be alive. If no stored node matches, `up` fails with
`NO_NODES_FOR_COUNTRY` (exit 5, hint `netivri sub update`; the message names
the country and/or the provider, never a URL) before binding anything.

The nodes of a route are ordered by ascending node id (ties across
providers by provider name) and used only for the accounts of that
route: there is no fallback to a direct connection or to another
country or provider. Accounts without a provider and country, and
auth's account, take the default route, which is direct until a node is selected.
A running daemon applies a change to accounts by itself, within a few seconds
(no `down` and `up` needed): it restarts its listener on the same port, which
ends the open connections. A change it cannot apply, such as a route without
nodes, leaves the running configuration in place and is reported as
`reload.last_error` by `status`. See [design/live-reload.md](design/live-reload.md).

### `up`

Without `--foreground`, `up` starts the daemon: it validates the flags and
the configuration, checks for an already running daemon, and otherwise
spawns a detached background process and returns immediately. `up` is safe
to repeat.

```json
{"schema_version": 1, "ok": true,
 "data": {"daemon": "running", "pid": 12345, "listen": "127.0.0.1:7890",
          "protocols": ["socks5", "socks4", "http"], "auth": "none",
          "foreground": false, "already_running": false}}
```

`foreground` is `true` for `up --foreground`, `false` for the daemon.
`already_running` is `true` when the command found and reused a running
daemon (idempotent success, exit 0); these keys are additive and do not
bump `schema_version`. When a daemon is already running and the caller
explicitly passed a `--port` / `--bind` that differs from the running
listen address (an explicit `--port 0` means "don't care"), `up` fails with
`ALREADY_RUNNING` (exit 6, hint: `run netivri down first`).

### `up --foreground`

Runs the proxy in the foreground: prints one document on stdout (the same
shape as above, without `already_running`; one line with `--json`), then blocks until SIGINT /
SIGTERM or `netivri down`, then exits 0. It writes nothing to stderr. A
foreground `up` is not idempotent: with a daemon already running in the same
home it fails with `ALREADY_RUNNING` (exit 6).

`up` also accepts the hidden flag `--handshake {PATH}` (only together with
`--foreground`). It is internal machinery of the daemon spawn, not part of
this contract; see [design/daemon.md](design/daemon.md).

`listen` is the actual bound address: with `--port 0` it is the random free
port that was picked. The daemon writes its state to `run/netivri.json` and
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

### Flags vs configuration

`up` reads `config.ktav` from the state directory (`--home`, `NETIVRI_HOME`,
or the user config dir; see [docs/config.md](config.md)). A missing file is
not an error: defaults apply, no proxy authentication, and **no config file
is created**. Explicit `--port` / `--bind` flags override `listen.port` /
`listen.bind`; otherwise the config values are used. Without a config the
defaults stay `127.0.0.1:7890`. A `--bind` that is neither an IP address nor
`localhost` is a usage error.

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
authentication is enabled in the configuration (`auth.login` and
`auth.password` set; a pending `init` counts only with
`auth.allow_init_remote: true`). Loopback means `127.0.0.0/8`, `::1` and
`localhost`.

### `status`

Reports the live status of the running instance.

```json
{"schema_version": 1, "ok": true,
 "data": {"daemon": "running", "foreground": false, "pid": 12345,
          "listen": "127.0.0.1:7890",
          "protocols": ["socks5", "socks4", "http"], "auth": "none",
          "accounts": [], "route": "direct", "started_at": "2026-10-20T10:00:00Z",
          "uptime_seconds": 3721, "version": "0.1.0"}}
```

Exactly these fields exist today: `daemon` (always `running` on success),
`foreground`, `pid`, `listen`, `protocols`, `auth`, `route` (`direct` or
`node`), `started_at` (RFC3339 UTC), `uptime_seconds` (whole seconds),
`version` (the daemon binary's version), `accounts` (see `account`; each record is
`{"login", "provider", "exit_country"}`, sorted by login), `warm`, `stats`, `affinity`, `refresh` and `reload`. The selection fields of the planned shape below are not included
yet; they arrive with `netivri use`.

`refresh` (`{"interval_seconds", "ok", "failed", "last_at", "last_error"}`) and
`reload` (`{"count", "last_at", "last_error"}`) describe how the daemon keeps
itself up to date. The daemon re-fetches every subscription once per
`fetch.update_interval` (one hour by default, `interval_seconds` is `0` when
it is `off`; see [config.md](config.md)); `ok` and `failed` count those
refreshes, `last_at` is the time of the latest one (RFC3339 UTC) and
`last_error` its failure (`CODE: message`, never a URL), empty after a success.
A fetched list that would leave an account without nodes is rejected and the
old list stays. `reload.count` is how many times the daemon applied changed
files by restarting its listener (accounts, passwords, nodes, port), `last_at`
the latest such restart, and `last_error` why the newest files are not applied
(an invalid `config.ktav`, a route without nodes); it is empty when the files
on disk are what runs. A refresh that brings the same nodes, or only different
node names, changes nothing and does not count.

`stats` (`{"path", "dropped"}`) is present while the statistics store is open;
`dropped` counts samples lost to a full write queue. `affinity`
(`{"bindings", "rebinds", "hedges", "hedge_wins"}`) is present while an account has a route: the
number of site-to-node bindings held now, how many times a binding moved
to another node because its node was down, how many second dial attempts
(to the same node) were started for a slow handshake and how many dials such
an attempt won (see [design/node-stats.md](design/node-stats.md)).

`warm` is present while the daemon keeps country nodes warm (an account has an
exit country): background probes of every node proxy through the node itself,
every 45 seconds, with the counters
`{"probes", "probe_failures", "pool_hits", "pool_misses", "pool_discards",
"nodes": [{"name", "alive", "delay_ms"}]}`. `nodes[].name` is an internal proxy
name (`netivri-r{R}-{N}`, the route and the node index), never a provider name; `delay_ms` is `0` until a
probe succeeded. The pre-dial pool is off in the daemon (`pool_*` stay `0`).
The probes request `http://www.gstatic.com/generate_204` through each node.

When no daemon is running: error `DAEMON_NOT_RUNNING` on stderr, exit 4
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

Stops the daemon through its control channel and waits until it is gone.
Idempotent.

```json
{"schema_version": 1, "ok": true,
 "data": {"daemon": "stopped", "was_running": true, "pid": 12345}}
```

With nothing running: `{"daemon": "stopped", "was_running": false}`, exit 0.
`pid` is present only when something was running. If the daemon does not
stop within 15 seconds: `DAEMON_STOP_TIMEOUT` (exit 1). `down` never kills
by pid.

### `node list` (after daemon integration)

```json
{"nodes": [
  {"id": "a1b2c3", "name": "🇩🇪 Germany 1", "sub": "main", "protocol": "vless",
   "country": "DE", "country_source": "exit",
   "latency_ms": 48, "alive": true, "tested_at": "2026-10-19T10:05:00Z"}
]}
```

Until the daemon serves health checks, `node list` prints the stored-node
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
   "nodes": 24, "fetched_at": "2026-10-19T10:00:00Z"},
  {"name": "backup", "host": "other.example", "via_set": false,
   "nodes": null, "fetched_at": null}
]}
```

The URL and the via proxy are never printed: only the host of the
subscription URL and a `via_set` boolean. `nodes` / `fetched_at` are
`null` when no snapshot is stored for the subscription.

### `sub update`

```
netivri sub update [{SUBSCRIPTION} | --all]
```

Re-fetches and re-stores; with a name only that subscription, with
`--all` or no argument all subscriptions in config order. A failed
subscription keeps its old stored snapshot. A running daemon does the same by
itself once per `fetch.update_interval` and applies the stored result to its
routes within a few seconds, so this command is for refreshing on demand.

```json
{"results": [
  {"name": "main", "ok": true, "nodes": 24, "skipped": 1, "error": null},
  {"name": "backup", "ok": false, "nodes": null, "skipped": null,
   "error": {"code": "FETCH_HTTP_STATUS", "message": "unexpected HTTP status 403"}}
]}
```

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
| 4 | Daemon not running (for commands that need it) |
| 5 | Not found (subscription, node, country with no nodes) |
| 6 | Conflict (port in use, daemon already running with other settings) |

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
| `ACCOUNT_NOT_FOUND` | 5 | No account with that login |
| `ACCOUNT_EXISTS` | 6 | Account already exists with conflicting settings |
| `SUB_EXISTS_NAME` | 6 | `sub add --name` already used by a different URL |
| `SUB_IN_USE` | 6 | `sub rm` of a subscription an account is bound to as its provider (nothing is removed; hint: `netivri account set-provider {ACCOUNT} --none`) |
| `SUB_UPDATE_FAILED` | 3 | At least one subscription failed to update (results still printed on stdout) |
| `SUB_SNAPSHOT_INVALID` | 1 | A stored snapshot exists but is corrupt or unreadable (a missing one is not an error); `sub update` rewrites it |
| `NODE_NOT_FOUND` | 5 | No node with that id or name |
| `NO_NODES_FOR_COUNTRY` | 5 | `up`: an account has no route because no stored node matches its provider and/or country (a bound provider that is not configured or holds no snapshot counts too); the nodes need not be alive. `account set-country` and `account set-provider` return it, before writing anything, when an account would be left in that state |
| `NO_ROUTE` | 5 | `diag dial`: the account has no route (neither provider nor exit country) |
| `DIAG_FAILED` | 3 | `diag dial`: the run could not proceed (for example an unusable node mapping) |
| `NODE_UNREACHABLE` | 3 | Test through the node failed |
| `DAEMON_NOT_RUNNING` | 4 | Run `netivri up` first |
| `STATS_UNAVAILABLE` | 5 | `stats`: no statistics database exists yet (nothing was recorded) |
| `ALREADY_RUNNING` | 6 | A daemon is already running; `up` requested conflicting settings (or was `--foreground`) |
| `DAEMON_START_FAILED` | 1 | The daemon child exited or failed before reporting startup; details in `run/netivri.log` of the state directory |
| `DAEMON_START_TIMEOUT` | 1 | The daemon did not report startup within 15 s |
| `DAEMON_STOP_TIMEOUT` | 1 | The daemon did not stop within 15 s after `down` |
| `PORT_IN_USE` | 6 | Listen port is taken |
| `INTERNAL` | 1 | Bug; details in `netivri logs` |

## Idempotency

- `sub add` with an existing URL returns the existing subscription, exit 0.
- `up` when the daemon already runs is idempotent: it reports the running
  instance with `already_running: true`, exit 0. Explicit `--port` / `--bind`
  flags that differ from the running listen address are `ALREADY_RUNNING`
  (exit 6); `--port 0` explicitly means "don't care". `up --foreground` is
  not idempotent and always fails with `ALREADY_RUNNING` (exit 6) when a
  daemon runs.
- `down` when not running: exit 0.
- `status` when not running: `DAEMON_NOT_RUNNING`, exit 4.
- `use` with the current selection: exit 0.
- `account add` with the same login, provider and country, repeated `account
  set-country` with the same country and repeated `account set-provider` with
  the same provider: exit 0; `account rm` and `sub rm` are not idempotent.

## Security

- The proxy binds to `127.0.0.1` by default. Binding elsewhere requires
  configured proxy authentication (`auth.login` / `auth.password` in
  `config.ktav`; the password may be `init`), otherwise `up` exits with
  `AUTH_REQUIRED`.
- The control channel is a loopback HTTP server (`127.0.0.1` only, random
  port) guarded by a bearer token that lives only in
  `{HOME}/run/netivri.json` (mode `0600` on Unix). Every request must carry
  the token; the Host header must match the listener. No remote control API.
- The daemon log (`{HOME}/run/netivri.log`) records lifecycle lines only:
  start and stop with pid, listen address and reason. No tokens, no
  credentials, no subscription URLs.
- Subscription URLs contain secrets: they are stored in the state directory
  only and are redacted in all output (`https://provider.example/sub/****`).
