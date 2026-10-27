# netivri

[![CI](https://github.com/PHPCraftdream/netivri/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/PHPCraftdream/netivri/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/PHPCraftdream/netivri?include_prereleases&sort=semver)](https://github.com/PHPCraftdream/netivri/releases)
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
  name providers and exit countries (one or a comma-separated list in priority
  order, `--provider main,backup --country DE,NL`) and is routed only through
  matching nodes, with no fallback to a direct connection or a country it does
  not name.
- **Steady exits.** An account keeps one exit node per destination site, nodes
  are probed in the background, and a slow handshake on a VLESS node gets one
  extra attempt against the same node.
- **SOCKS5 before and after the VPN.** Optional `pre_socks5` (how netivri
  reaches the VPN node) and `post_socks5` (where the traffic goes after it),
  global and per account; an account value overrides the global one and `none`
  cancels it.
- **Client address rules.** `listen.allowed_clients` limits which client
  addresses may connect at all (`loopback`, exact addresses, `*` masks, CIDR);
  an account can narrow it with its own rule, checked before its password.
- **Accounts you can copy and inspect.** `netivri account clone` makes a new
  login from an existing one (same password, a new one, or `init`), and
  `netivri account show` prints its route tier by tier with live node state.
- **See what is going on.** `netivri status` is short by default (`--full`
  for everything); `netivri status --watch` is a live view of who is active,
  through which nodes and countries; `netivri logs` reads or follows the
  angel log; `netivri doctor` checks the whole setup in one pass and exits 7
  when something failed.
- **Numbers, not guesses.** Node statistics are stored in a local SQLite
  database (`netivri stats`, per node or per account), and `netivri diag dial` shows where a connection
  spends its time: DNS, TCP, TLS and first byte.
- **Help that stands on its own.** `netivri help` covers every command, the
  configuration file (`netivri help config`), routing (`netivri help routing`)
  and the fields each command prints (`netivri help output`); `--json` gives
  the same for scripts and agents.
- **Small and self-contained.** One binary with [Mihomo](https://github.com/MetaCubeX/mihomo)
  embedded as a library; no external core process and no native libraries
  next to it.

## Install

Two ways, both install the same prebuilt binary.

### 1. Install script

The script downloads the release archive of your platform, verifies its
SHA256 checksum and puts one binary into a user directory (no `sudo`):

```sh
# macOS / Linux
curl -fsSL https://raw.githubusercontent.com/PHPCraftdream/netivri/main/install.sh | sh
```

```powershell
# Windows PowerShell
iwr -useb https://raw.githubusercontent.com/PHPCraftdream/netivri/main/install.ps1 | iex
```

### 2. npm

With Node.js 18 or newer (no build and no download scripts at install time;
see [`docs/npm.md`](docs/npm.md)):

```sh
npm i -g netivri
```

Upgrade with `npm i -g netivri@latest`, remove with `npm rm -g netivri`.

### Other options

You can also download an archive from [GitHub Releases](https://github.com/PHPCraftdream/netivri/releases)
and check it against `SHA256SUMS`. Options, upgrading while netivri runs and
the supported platforms (Linux needs glibc; Alpine is not supported) are in
[`docs/install.md`](docs/install.md).

`go install` does not work: the module vendors Mihomo through a `replace`
directive.

## Quick start

```sh
# 1. Add a subscription (the URL is a credential; keep it private)
netivri sub add https://provider.example/sub/{TOKEN} --name main

# 2. Create a login that exits through Germany
netivri account add alice --provider main --country DE

# 3. Start the background angel
netivri up --port 7890
netivri status
```

The first client that connects as `alice` sets the password (the `init`
placeholder, see [`docs/config.md`](docs/config.md)). Then point a browser or
a tool at `127.0.0.1:7890`; HTTP and SOCKS5 share the port:

```sh
curl -x socks5h://alice:choose-a-password@127.0.0.1:7890 https://example.org
```

`netivri down` stops the angel. A running angel picks up changes to
accounts and subscriptions by itself within a few seconds, and re-fetches
every subscription once an hour (`fetch.update_interval`).

### SOCKS5 before and after the VPN

Two optional SOCKS5 proxies wrap the VPN node: `pre_socks5` is what netivri
connects through to reach the node, `post_socks5` is where the traffic goes
after the node (client → netivri → pre → VPN node → post → destination). Set
them for everyone in `config.ktav` and override them per account:

```ktav
chain.pre_socks5:: socks5://user:secret@pre.example:1080
chain.post_socks5:: socks5://post.example:1080

accounts: [
    {
        login: alice
        password:: init
        provider: main
        exit_country: DE
        post_socks5:: socks5://alice-exit.example:1080
    }
]
```

```sh
netivri account set-socks5 alice --post none        # alice skips the global post
netivri account set-socks5 alice --post inherit     # back to the global value
netivri account add bob --provider main --country NL --pre-socks5 socks5://pre2.example:1080
netivri account show alice                          # effective values, never the credentials
```

Each side is independent: an account value wins over the global one, an
absent value inherits it, `none` cancels it. See
[`docs/config.md`](docs/config.md) and `netivri help account set-socks5`.

`netivri help` lists every command; `netivri help sub add` (or
`netivri sub add --help`) shows the flags, examples and possible errors of one
command.

## How it works

- **Core:** Mihomo (Clash.Meta) is embedded as a Go library and vendored in
  [`third_party/mihomo`](third_party/README.md) so that its adapters can be
  patched when measurements call for it.
- **Angel and CLI:** `netivri up` starts a background angel; the other
  commands talk to it over a local control channel (a named pipe on Windows, a
  Unix socket elsewhere), never over TCP.
- **Routing:** each account's providers and exit countries select a set of
  nodes, in priority tiers (countries first, then providers inside a
  country); a sticky group keeps one node per site and re-binds when it
  degrades. Design notes are in [`docs/design/`](docs/design/).
- **Configuration:** a Ktav file in the state directory
  ([`docs/config.md`](docs/config.md)); passwords are stored as argon2id
  hashes.

## Documentation

| Topic | Document |
|---|---|
| CLI contract, JSON, exit and error codes | [`docs/cli.md`](docs/cli.md) |
| Configuration file | [`docs/config.md`](docs/config.md) |
| Installation | [`docs/install.md`](docs/install.md) |
| Releasing | [`docs/release.md`](docs/release.md) |
| Dependency licenses | [`docs/licenses.md`](docs/licenses.md) |
| Testing | [`docs/testing.md`](docs/testing.md) |
| Performance measurements | [`docs/perf/`](docs/perf/) |
| Design notes | [`docs/design/`](docs/design/) (the SOCKS5 chain: [`socks5-chain.md`](docs/design/socks5-chain.md)) |
| For AI agents | [`AGENTS.md`](AGENTS.md) |
| History | [`CHANGELOG.md`](CHANGELOG.md) |

## Status

Version 0.x: the CLI contract may still change between minor versions, and
every change is listed in the [changelog](CHANGELOG.md). Command results are
documents only, in Ktav or with `--json` in JSON (`netivri help`,
`netivri logs --follow` and `netivri status --watch` are the text outputs).
`netivri doctor` checks the whole setup in one pass (configuration, angel,
port, subscription snapshots, per-account routes, angel log) and exits 7 when
something failed. Described in [`docs/cli.md`](docs/cli.md) but not
implemented yet: `use`, `node test` and `schema`.

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
