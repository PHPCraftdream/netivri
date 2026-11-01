# Server CLI

`netivri server` manages registered inbound protocol listeners. The commands accept `--home {DIR}` or `--local`; each also accepts `--json`. `protocols`, `list`, `show`, `add`, `link`, `rotate` and `remove` are implemented. Use `--help` for command usage. Results use the standard versioned CLI envelope; no command prompts.

## Commands

```
netivri server protocols
netivri server add --protocol {PROTOCOL} --account {ACCOUNT|*} --port {PORT} [--name {NAME}] [--bind {ADDR}] [--public-host {HOST}] [--default] [--param {KEY=VALUE} ...]
netivri server list
netivri server show {NAME}
netivri server link {NAME} [--account {ACCOUNT}] [--host {HOST}] [--format {uri|mihomo}] [--qr]
netivri server rotate {NAME} [--account {ACCOUNT}]
netivri server remove {NAME}
```

`protocols` lists registered protocols and `single_user`. The protocol interface currently has no parameter metadata, so `params` is an empty list; the parent `server` help topic dynamically lists this build's registered protocol names.

`add` requires a registered protocol, account selection and a port 1–65535. `*` expands to all configured accounts for multi-user protocols; single-user protocols require exactly one account. `--default` marks this TLS listener as fallback for valid unmatched ClientHellos on a shared endpoint; it never resolves overlapping matches. Shared members require protocol-declared explicit match keys matching the exported link: Trojan and AnyTLS require `sni` unless the server is the default; VLESS-Reality uses `sni` or a destination-derived servername. TrustTunnel without explicit `sni` matches h2 at any SNI; with explicit `sni`, it matches exactly. An absent-SNI key otherwise matches only absent SNI. Explicit `sni` is recommended for certificate-TLS members. A sole TLS server may be the default; a shared server without an explicit usable key must itself be the default. A default link without `sni` may use host-derived SNI that matches no key and falls back. A missing explicit key returns `SERVER_PORT_AMBIGUOUS` with hint `set sni on {NAME} or mark it as the default`; overlapping keys require changing `sni` (a default cannot resolve overlap). Bind defaults to `0.0.0.0`; name defaults to `ss-{PORT}` for Shadowsocks, otherwise `{PROTOCOL}-{PORT}`. Repeat `--param key=value` for protocol parameters. The full configuration is validated before secret generation/persistence. Name conflicts return `SERVER_EXISTS`; exclusive same-transport listeners with overlapping configured binds return `PORT_IN_USE`. TCP and UDP may share a port number.

`list`/`show` expose endpoint details and parameter keys only, never values or secrets. `link` returns `{name, account, format, link}` and is the intentional credential disclosure. `--host` overrides `public_host`; otherwise a host is required. A host must be a hostname or IP without a port or credentials. Multi-account servers require `--account`. Formats are `uri` (default) and `mihomo` when supported by the protocol. `--qr` is rejected: there is no QR encoder dependency. `rotate` replaces one account secret or, without `--account`, server material and user secrets. `remove` deletes config and secrets.

Configuration is `{HOME}/config.ktav`; generated secrets/material are stored separately in `{HOME}/server-secrets.ktav` using owner-only atomic writes. Add persists secrets before config, and removes its newly created entry if config save fails. Commands persist the requested config/secret changes; live watcher/core handling applies valid changes when the angel is running. A failed restart preserves the last good configuration where possible and the angel stays alive to retry.

## Errors

| Code | Exit | Meaning |
| --- | ---: | --- |
| `USAGE` | 2 | Invalid flags, host, account selection, format or unsupported QR output. |
| `CONFIG_INVALID` | 2 | Invalid endpoint, account reference, protocol configuration or parameter. Values are not echoed. |
| `SERVER_NOT_FOUND` | 5 | Requested server is not configured. |
| `SERVER_EXISTS` | 6 | Server name is already configured. |
| `SERVER_PORT_AMBIGUOUS` | 6 | TLS match keys overlap on a shared endpoint; give one of them another SNI. A default cannot resolve the conflict. |
| `PORT_IN_USE` | 6 | Port overlaps the proxy listener or an exclusive same-transport listener, or a mixed proxy listener. |
| `INTERNAL` | 1 | Secrets/config storage or link generation failed. |

Success data: `protocols` contains protocol descriptors; `list` returns `{servers:[...]}`; `show`/`add` return `{server:{...}}`; link, rotate and remove return the fields described above. Every response uses the CLI output envelope.

## Runtime diagnostics

`status` reports running server listeners and their available counters; see [`docs/cli.md`](../cli.md#status). `status --full` adds the usual full proxy details but server entries appear in either view. The JSON server entry has `name`, `protocol`, `listen`, `accounts`, `connections` (current active connection count), `dropped_clients` (unsigned aggregate of client drops from allowed-client rejection and unsupported UDP packets), plus shared-endpoint `members`, `default`, `unclassified` and `slow_first_byte`. Empty listener sets omit the optional `servers` field. `status --watch --json` includes it on each frame when nonempty; text watch prints a `Servers` table.

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
