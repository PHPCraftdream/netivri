# netivri

[![CI](https://github.com/PHPCraftdream/netivri/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/PHPCraftdream/netivri/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/PHPCraftdream/netivri?include_prereleases&sort=semver)](https://github.com/PHPCraftdream/netivri/releases)
[![npm](https://img.shields.io/npm/v/netivri)](https://www.npmjs.com/package/netivri)
[![License: GPL-3.0-only](https://img.shields.io/github/license/PHPCraftdream/netivri)](LICENSE)
[![Go](https://img.shields.io/github/go-mod/goversion/PHPCraftdream/netivri)](go.mod)
![Platforms](https://img.shields.io/badge/platforms-linux%20%7C%20macos%20%7C%20windows-informational)

> נתיב עברי — "the path of the one who crossed over".

`netivri` is a command-line proxy service. It takes a provider subscription
link, keeps a local proxy running on one port (SOCKS5, SOCKS4 and HTTP), and
routes each login through the provider and exit country you choose.

It is built to be driven equally well by a person in a terminal and by an AI
agent: every command has a stable output (Ktav by default, JSON with `--json`),
documented exit codes and machine-readable errors. See [`AGENTS.md`](AGENTS.md) and
[`docs/cli.md`](docs/cli.md).

## Features

- **One port, three protocols.** SOCKS5, SOCKS4/4a and HTTP (plain requests
  and `CONNECT`) on the same socket, detected from the first byte. Loopback by
  default; a non-loopback bind requires proxy authentication. TCP only.
- **Subscriptions.** Clash/Mihomo YAML and base64 lists of share links
  (`vless://`, `vmess://`, `trojan://`, `ss://`, `hysteria2://`, ...). A
  subscription is fetched through a strict proxy and never silently directly;
  its URL is a credential and is never printed.
- **Accounts with an exit country.** Several logins share the port; each can
  name a provider and an exit country and is routed only through matching
  nodes, with no fallback to a direct connection or another country.
- **Steady exits.** An account keeps one exit node per destination site, nodes
  are probed in the background, and a slow handshake on a VLESS node gets one
  extra attempt against the same node.
- **Numbers, not guesses.** Node statistics are stored in a local SQLite
  database (`netivri stats`), and `netivri diag dial` shows where a connection
  spends its time: DNS, TCP, TLS and first byte.
- **Small and self-contained.** One binary with [Mihomo](https://github.com/MetaCubeX/mihomo)
  embedded as a library; no external core process and no native libraries
  next to it. No postinstall scripts, no downloads at install time.

## Install

```sh
npm i -g netivri
```

or the standalone installers, which verify the SHA256 checksum:

```sh
# macOS / Linux
curl -fsSL https://raw.githubusercontent.com/PHPCraftdream/netivri/main/install.sh | sh
```

```powershell
# Windows PowerShell
iwr -useb https://raw.githubusercontent.com/PHPCraftdream/netivri/main/install.ps1 | iex
```

or download an archive from [GitHub Releases](https://github.com/PHPCraftdream/netivri/releases)
and check it against `SHA256SUMS`. Details, options, upgrading while netivri
runs and the supported platforms (Linux needs glibc; Alpine is not supported):
[`docs/install.md`](docs/install.md) and [`docs/npm.md`](docs/npm.md).

`go install` does not work: the module vendors Mihomo through a `replace`
directive.

## Quick start

```sh
# 1. Add a subscription (the URL is a credential; keep it private)
netivri sub add https://provider.example/sub/{TOKEN} --name main

# 2. Create a login that exits through Germany
netivri account add alice --provider main --country DE

# 3. Start the background daemon
netivri up --port 7890
netivri status
```

The first client that connects as `alice` sets the password (the `init`
placeholder, see [`docs/config.md`](docs/config.md)). Then point a browser or
a tool at `127.0.0.1:7890`; HTTP and SOCKS5 share the port:

```sh
curl -x socks5h://alice:choose-a-password@127.0.0.1:7890 https://example.org
```

`netivri down` stops the daemon. A running daemon picks up changes to
accounts and subscriptions by itself within a few seconds, and re-fetches
every subscription once an hour (`fetch.update_interval`).

`netivri help` lists every command; `netivri help sub add` (or
`netivri sub add --help`) shows the flags, examples and possible errors of one
command.

## How it works

- **Core:** Mihomo (Clash.Meta) is embedded as a Go library and vendored in
  [`third_party/mihomo`](third_party/README.md) so that its adapters can be
  patched when measurements call for it.
- **Daemon and CLI:** `netivri up` starts a background daemon; the other
  commands talk to it over a local control channel (a named pipe on Windows, a
  Unix socket elsewhere), never over TCP.
- **Routing:** each account's provider and exit country select a set of
  nodes; a sticky group keeps one node per site and re-binds when it
  degrades. Design notes are in [`docs/design/`](docs/design/).
- **Configuration:** a Ktav file in the state directory
  ([`docs/config.md`](docs/config.md)); passwords are stored as argon2id
  hashes.

## Documentation

| Topic | Document |
|---|---|
| CLI contract, JSON, exit and error codes | [`docs/cli.md`](docs/cli.md) |
| Configuration file | [`docs/config.md`](docs/config.md) |
| Installation | [`docs/install.md`](docs/install.md), [`docs/npm.md`](docs/npm.md) |
| Releasing | [`docs/release.md`](docs/release.md) |
| Dependency licenses | [`docs/licenses.md`](docs/licenses.md) |
| Testing | [`docs/testing.md`](docs/testing.md) |
| Performance measurements | [`docs/perf/`](docs/perf/) |
| Design notes | [`docs/design/`](docs/design/) |
| For AI agents | [`AGENTS.md`](AGENTS.md) |
| History | [`CHANGELOG.md`](CHANGELOG.md) |

## Status

Version 0.x: the CLI contract may still change between minor versions, and
every change is listed in the [changelog](CHANGELOG.md). Command results are
documents only, in Ktav or with `--json` in JSON (`netivri help` is the one text output). Described in
[`docs/cli.md`](docs/cli.md) but not implemented yet: `use`, `node test`,
`logs`, `doctor` and `schema`.

Non-goals for 0.x: TUN or a system-wide VPN mode (it needs admin rights),
changing the operating system's proxy settings, and a GUI.

## Contributing and security

Contributions are welcome, see [`CONTRIBUTING.md`](CONTRIBUTING.md). Report a
vulnerability privately as described in [`SECURITY.md`](SECURITY.md).

## License

SPDX: `GPL-3.0-only`. See [`LICENSE`](LICENSE).

The license is inherited from Mihomo and its dependencies (`sing`,
`sing-tun`), which are GPL-3.0 and are linked into the `netivri` binary, so
the program cannot be offered under MIT or Apache-2.0; the reasoning is in
[`docs/licenses.md`](docs/licenses.md), together with the third-party license
policy and the audited exceptions.
