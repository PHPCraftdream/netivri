# Server CLI

`netivri server` manages registered inbound protocol listeners. The commands accept `--home {DIR}` or `--local`; each also accepts `--json`. `protocols`, `list`, `show`, `add`, `link`, `rotate` and `remove` are implemented. Use `--help` for command usage. Results use the standard versioned CLI envelope; no command prompts.

## Commands

```
netivri server protocols
netivri server add --protocol {PROTOCOL} --account {ACCOUNT|*} --port {PORT} [--name {NAME}] [--bind {ADDR}] [--public-host {HOST}] [--param {KEY=VALUE} ...]
netivri server list
netivri server show {NAME}
netivri server link {NAME} [--account {ACCOUNT}] [--host {HOST}] [--format {uri|mihomo}] [--qr]
netivri server rotate {NAME} [--account {ACCOUNT}]
netivri server remove {NAME}
```

`protocols` lists registered protocols and `single_user`. The protocol interface currently has no parameter metadata, so `params` is an empty list; the parent `server` help topic dynamically lists this build's registered protocol names.

`add` requires a registered protocol, account selection and a port 1–65535. `*` expands to all configured accounts for multi-user protocols; single-user protocols require exactly one account. Bind defaults to `0.0.0.0`; name defaults to `ss-{PORT}` for Shadowsocks, otherwise `{PROTOCOL}-{PORT}`. Repeat `--param key=value` for protocol parameters. The full configuration is validated before secret generation/persistence. Name conflicts return `SERVER_EXISTS`; overlapping configured binds on a port return `PORT_IN_USE`. Core listeners bind per transport, but current config validation and `server add` reject overlapping bind/port pairs regardless of transport. Thus TCP and UDP may share a numeric port in the core, but the CLI does not support it.

`list`/`show` expose endpoint details and parameter keys only, never values or secrets. `link` returns `{name, account, format, link}` and is the intentional credential disclosure. `--host` overrides `public_host`; otherwise a host is required. A host must be a hostname or IP without a port or credentials. Multi-account servers require `--account`. Formats are `uri` (default) and `mihomo` when supported by the protocol. `--qr` is rejected: there is no QR encoder dependency. `rotate` replaces one account secret or, without `--account`, server material and user secrets. `remove` deletes config and secrets.

Configuration is `{HOME}/config.ktav`; generated secrets/material are stored separately in `{HOME}/server-secrets.ktav` using owner-only atomic writes. Add persists secrets before config, and removes its newly created entry if config save fails. Commands persist the requested config/secret changes; live watcher/core handling applies valid changes when the angel is running. A failed restart preserves the last good configuration where possible and the angel stays alive to retry.

## Errors

| Code | Exit | Meaning |
| --- | ---: | --- |
| `USAGE` | 2 | Invalid flags, host, account selection, format or unsupported QR output. |
| `CONFIG_INVALID` | 2 | Invalid endpoint, account reference, protocol configuration or parameter. Values are not echoed. |
| `SERVER_NOT_FOUND` | 5 | Requested server is not configured. |
| `SERVER_EXISTS` | 6 | Server name is already configured. |
| `PORT_IN_USE` | 6 | Port overlaps the proxy listener or a configured server listener. |
| `INTERNAL` | 1 | Secrets/config storage or link generation failed. |

Success data: `protocols` contains protocol descriptors; `list` returns `{servers:[...]}`; `show`/`add` return `{server:{...}}`; link, rotate and remove return the fields described above. Every response uses the CLI output envelope.

## Runtime diagnostics

`status` reports running server listeners and their available counters; see [`docs/cli.md`](../cli.md#status). `status --full` adds the usual full proxy details but server entries appear in either view. The JSON server entry has exactly `name`, `protocol`, `listen`, `accounts`, `connections` (current active connection count) and `dropped_clients` (unsigned aggregate of client drops from allowed-client rejection and unsupported UDP packets). Empty listener sets omit the optional `servers` field. `status --watch --json` includes it on each frame when nonempty; text watch prints a `Servers` table.

`doctor` inspects configured servers: config validation catches malformed definitions, account references and configured port conflicts; with the angel running it reports configured server endpoints as part of the server check, and without it diagnoses whether configured ports are available. Secrets-file readability/mode and orphan secret names are checked. `logs` reads the same angel log as other commands; server start/stop/change lines are logged literally as `server {NAME} ({PROTOCOL}) listening on {BIND}:{PORT} for accounts {ACCOUNT,...}` and `server {NAME} stopped`. Allowed-client drops are summarized once per minute as `server {NAME}: {COUNT} clients dropped by allowed_clients in the last minute` (`WARN` at 100 or more, otherwise `INFO`). Certificate expiry checking is not implemented because the current server protocol API does not expose a common certificate-expiry interface. See `docs/cli.md` for the `doctor` and `logs` envelopes and errors.

Examples:

```
netivri server protocols
netivri server add --protocol shadowsocks --account alice --port 8388 --bind 127.0.0.1 --public-host edge.example.org --param cipher=aes-128-gcm
netivri server list --json
netivri server link ss-8388 --host edge.example.org --format uri --json
netivri server rotate ss-8388 --account alice
netivri server remove ss-8388
```

The global `listen.allowed_clients` and, for account authentication, account `allowed_clients` policy both gate server clients. An account allowlist-only change is hot without listener restart and existing connections survive, including mixed authentication. See [configuration](../config.md#server-listeners).
