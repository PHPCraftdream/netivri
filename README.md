# netivri

> נתיב עברי — "the path of the one who crossed over".

`netivri` is a command-line proxy service. It takes a provider subscription
link, keeps a local HTTP/SOCKS5 proxy running, and lets you pick the exit node
by name, by country, or automatically by latency.

It is designed to be driven equally well by a human in a terminal and by an
AI agent: every command has a stable JSON output, documented exit codes and
machine-readable errors. See [`AGENTS.md`](AGENTS.md) and
[`docs/cli.md`](docs/cli.md).

## Status

Work in progress. Working today: the local proxy on one dual-protocol port
(`netivri up --foreground`: SOCKS5, SOCKS4 and HTTP, detected from the first
byte), proxy authentication with a hashed password stored in the Ktav config
(including the first-use `init` placeholder), the `sub` and `node` commands
(subscriptions are fetched through a strict proxy, never silently directly),
routing through one chosen upstream node in the core, release archives for
Linux, macOS and Windows, and the installers. Not done yet: `netivri use`, the
background daemon (`down`, `status`), health checks and connection warm-up.
The sections below describe the target design.

## Install

- Standalone, no npm: `install.sh` / `install.ps1`, see
  [`docs/install.md`](docs/install.md).
- npm: `npm i -g netivri`, see [`docs/npm.md`](docs/npm.md) (published with
  the first release).
- Release archives with checksums: GitHub Releases.

## How it works

- **Core:** [Mihomo](https://github.com/MetaCubeX/mihomo) (Clash.Meta) embedded
  as a Go library. One binary, no external core process.
- **Subscriptions:** both formats that providers serve are accepted: Clash/Mihomo
  YAML and base64 lists of share links (`vless://`, `vmess://`, `trojan://`,
  `ss://`, `hysteria2://`, ...). Quota and expiry come from the
  `subscription-userinfo` header.
- **Daemon + CLI:** `netivri up` starts a background daemon. All other commands
  talk to it over a local control channel (named pipe on Windows, Unix socket
  elsewhere). The control channel is never exposed over TCP.
- **Exit country:** a node's name is only a hint. `netivri node test --exit`
  checks the real exit IP through the node itself and caches the result.

## Quick start (planned)

```
netivri sub add https://provider.example/sub/<token> --name main
netivri up --port 7890
netivri use --country DE --fastest
netivri status
```

Then point your browser or tool at `127.0.0.1:7890` (HTTP and SOCKS5 on the
same port).

## Non-goals for v1

- TUN / system-wide VPN mode (needs admin rights).
- Changing the OS system proxy settings.
- GUI.

## License

SPDX: `GPL-3.0-only`. See [`LICENSE`](LICENSE).

The license is inherited from Mihomo and its dependencies (`sing`,
`sing-tun`), which are GPL-3.0 and are linked into the `netivri` binary.
Third-party license policy and audited exceptions: [`docs/licenses.md`](docs/licenses.md).
