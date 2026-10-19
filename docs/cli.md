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
netivri sub add <url> [--name N]          add a subscription (idempotent by URL)
netivri sub list
netivri sub update [N | --all]            re-fetch
netivri sub rm N

netivri node list [--sub N] [--country CC] [--alive]
netivri node test [NODE | --all] [--exit] [--timeout 5s]

netivri use NODE
netivri use --country CC [--fastest | --auto]
netivri use --fastest | --auto            --auto keeps re-selecting by latency

netivri up [--port 7890] [--bind 127.0.0.1] [--foreground]
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
 "error": {"code": "SUB_FETCH_FAILED", "message": "GET https://...: 403", "hint": "the subscription token may have expired"}}
```

`netivri logs --follow --json` is the only streaming command: one JSON object
per line (NDJSON).

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

### `node list`

```json
{"nodes": [
  {"id": "a1b2c3", "name": "🇩🇪 Germany 1", "sub": "main", "protocol": "vless",
   "country": "DE", "country_source": "exit",
   "latency_ms": 48, "alive": true, "tested_at": "2026-10-19T10:05:00Z"}
]}
```

`country_source` is one of `exit` (checked through the node), `geoip` (GeoIP
of the server address, i.e. the entry point) or `name` (parsed from the node
name). Prefer `exit`.

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
| `SUB_FETCH_FAILED` | 3 | Subscription URL did not return a valid list |
| `SUB_PARSE_FAILED` | 3 | Response is not YAML or share links |
| `SUB_NOT_FOUND` | 5 | No subscription with that name |
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

- The proxy binds to `127.0.0.1` by default. Binding elsewhere requires an
  explicit `--bind`.
- The control channel is a named pipe / Unix socket restricted to the current
  user. No TCP control API.
- Subscription URLs contain secrets: they are stored in the state directory
  only and are redacted in all output (`https://provider.example/sub/****`).
