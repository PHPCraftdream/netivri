# CLI contract

This document is the contract between `netivri` and its callers (humans,
scripts, AI agents). Changing anything here is a breaking change and bumps
`schema_version`.

## Global flags

| Flag | Meaning |
|---|---|
| `--json` | Machine output. Implied when stdout is not a TTY. |
| `--home <dir>` | State directory. Default: `%APPDATA%\netivri` / `$XDG_CONFIG_HOME/netivri`. Env: `NETIVRI_HOME`. |
| `--quiet` | No human-oriented output on stderr. |
| `--no-color` | No ANSI. Implied when not a TTY or `NO_COLOR` is set. |

Commands never prompt. Anything that would need confirmation takes a flag
instead (`--yes`).

## Commands

```
netivri sub add <url> [--name N] [--via PROXY]
                                          add a subscription (idempotent by URL)
netivri sub list
netivri sub update [N | --all]            re-fetch
netivri sub rm N

netivri node list [--sub N] [--country CC] [--alive]
netivri node test [NODE | --all] [--exit] [--timeout 5s]

netivri use NODE
netivri use --country CC [--fastest | --auto]
netivri use --fastest | --auto            --auto keeps re-selecting by latency

netivri up [--foreground] [--port 7890] [--bind 127.0.0.1] [--home DIR]
netivri down
netivri status

netivri logs [--follow] [--level debug|info|warn|error] [--since 10m]
netivri doctor
netivri schema                            this contract as JSON
netivri version
```

`NODE` is a node id (stable hash) or an exact node name. `CC` is an ISO
3166-1 alpha-2 country code (`DE`, `NL`, `FI`).

## Output

Success: one JSON object on stdout.

```json
{"schema_version": 1, "ok": true, "data": { ... }}
```

Failure: one JSON object on stderr, non-zero exit code.

```json
{"schema_version": 1, "ok": false,
 "error": {"code": "FETCH_HTTP_STATUS", "message": "unexpected HTTP status 403", "hint": "the subscription server rejected the request"}}
```

Subscription URLs are credentials and are never printed: not on stdout,
not on stderr, not in error messages. The same holds for `--via` proxies.

`netivri logs --follow --json` is the only streaming command: one JSON object
per line (NDJSON).

### `up --foreground`

Runs the proxy in the foreground: prints one JSON line on stdout, then blocks
until SIGINT / SIGTERM, then exits 0. Until the daemon exists, `--foreground`
is required; plain `up` is a usage error.

```json
{"schema_version": 1, "ok": true,
 "data": {"daemon": "running", "pid": 12345, "listen": "127.0.0.1:7890",
          "protocols": ["socks5", "socks4", "http"], "auth": "none"}}
```

`listen` is the actual bound address: with `--port 0` it is the random free
port that was picked. `auth` is one of:

- `none` — no authentication configured (`auth.login` / `auth.password` empty);
- `enabled` — login and password (an argon2id hash) are configured;
- `init-pending` — the placeholder `init` is still set for the login or the
  password; the first client to authenticate claims the credential (see
  [docs/config.md](config.md), "The `init` placeholder").

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

```json
{
  "daemon": "running",
  "pid": 1234,
  "listen": "127.0.0.1:7890",
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
netivri sub add <url> [--name N] [--via PROXY]
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
netivri sub update [N | --all]
```

Re-fetches and re-stores; with a name only that subscription, with
`--all` or no argument all subscriptions in config order. A failed
subscription keeps its old stored snapshot.

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
netivri node list [--sub N] [--country CC] [--alive]
```

Prints the stored nodes of all configured subscriptions (or only
`--sub N`), in a stable order: subscription order, then provider order.
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
| `SUB_EXISTS_NAME` | 6 | `sub add --name` already used by a different URL |
| `SUB_UPDATE_FAILED` | 3 | At least one subscription failed to update (results still printed on stdout) |
| `SUB_SNAPSHOT_INVALID` | 1 | A stored snapshot exists but is corrupt or unreadable (a missing one is not an error); `sub update` rewrites it |
| `NODE_NOT_FOUND` | 5 | No node with that id or name |
| `NO_NODES_FOR_COUNTRY` | 5 | No alive node for the requested country |
| `NODE_UNREACHABLE` | 3 | Test through the node failed |
| `DAEMON_NOT_RUNNING` | 4 | Run `netivri up` first |
| `PORT_IN_USE` | 6 | Listen port is taken |
| `INTERNAL` | 1 | Bug; details in `netivri logs` |

## Idempotency

- `sub add` with an existing URL returns the existing subscription, exit 0.
- `up` when the daemon already runs with the same settings returns `status`, exit 0.
  With different settings: `PORT_IN_USE` / exit 6, unless `--restart`.
- `down` when not running: exit 0.
- `use` with the current selection: exit 0.

## Security

- The proxy binds to `127.0.0.1` by default. Binding elsewhere requires
  configured proxy authentication (`auth.login` / `auth.password` in
  `config.ktav`; the password may be `init`), otherwise `up` exits with
  `AUTH_REQUIRED`.
- The control channel is a named pipe / Unix socket restricted to the current
  user. No TCP control API.
- Subscription URLs contain secrets: they are stored in the state directory
  only and are redacted in all output (`https://provider.example/sub/****`).
