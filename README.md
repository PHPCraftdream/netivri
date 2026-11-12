# netivri

[![CI](https://github.com/PHPCraftdream/netivri/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/PHPCraftdream/netivri/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/PHPCraftdream/netivri?include_prereleases&sort=semver)](https://github.com/PHPCraftdream/netivri/releases)
[![npm](https://img.shields.io/npm/v/@netivri/proxy)](https://www.npmjs.com/package/@netivri/proxy)
[![npm downloads](https://img.shields.io/npm/dm/@netivri/proxy)](https://www.npmjs.com/package/@netivri/proxy)
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
- **Explicit account exits.** Choose the account's VPN-node route or direct egress (optionally via a post proxy). Direct is opt-in and exposes this machine's address to destinations. Toggles apply in place only while at least one account route has stored nodes both before and after; boundary and direct-only changes restart. Keeping another node-backed account route through a flip preserves connections. Adding an account always restarts. Account writes validate the entire resulting configuration before saving; failures return `CONFIG_INVALID` and persist no changes. Batch account writes are atomic.
- **Servers for your accounts.** Add an inbound endpoint for anytls, hysteria2, mieru, shadowquic, shadowsocks, snell, sudoku, trojan, trusttunnel, tuic, vless-reality or vmess. One share mode lets Shadowsocks 2022 (three ciphers), VMess AEAD (`alterId=0`), and the TLS family (Trojan, AnyTLS, TrustTunnel, VLESS-Reality) share a TCP port; legacy Shadowsocks AEAD, Snell, Sudoku, Mieru and UDP servers remain exclusive. Each connection uses its account's route; generate private client links or Mihomo YAML with `netivri server link` (shared links use one account). See [`docs/servers/`](docs/servers/README.md).
- **Steady exits.** An account keeps one exit node per destination site, nodes
  are probed in the background, and a slow handshake on a VLESS node gets one
  extra attempt against the same node.
- **Pre/post proxy pools.** `pre_socks5` (how netivri reaches a VPN node) and `post_socks5` (where traffic goes after it) accept up to 16 ordered candidates per side using `socks5://`, `http://` (HTTP CONNECT) or `https://` over TLS. Candidates are alternatives, never serial hops; global, provider and account lists use full nearest-level replacement, with `none` clearing inherited candidates. Weighted hop health quarantines repeatedly failing candidates and probes for revival; post hops are bound per login and site. `status`, `doctor` and the Service tab show the pools. See [`docs/config.md`](docs/config.md#socks5-chain).
- **Separate runs.** `--local` makes `./.netivri` its own state directory
  (settings, tokens, subscriptions, statistics, angel), found again without a
  flag from that directory; `--home {DIR}` names any directory.
- **Client address rules.** `listen.allowed_clients` limits which client
  addresses may connect at all (`loopback`, exact addresses, `*` masks, CIDR);
  an account can narrow it with its own rule, checked before its password.
- **Accounts you can copy and inspect.** `netivri account clone` makes a new
  login from an existing one (same password, a new one, or `init`), and
  `netivri account show` prints its route tier by tier with live node state.
- **See what is going on.** `netivri status` is short by default (`--full`
  for everything); `netivri status --watch` is a live view of who is active,
  through which nodes and countries; `netivri logs` reads or follows the
  angel log (a crash of the angel leaves a dump in `run/crash/`); `netivri doctor` checks the whole setup in one pass and exits 7
  when something failed.
- **Numbers, not guesses.** Node statistics are stored in a local SQLite
  database (`netivri stats`, per node or per account), and `netivri diag dial` shows where a connection
  spends its time: DNS, TCP, TLS and first byte.
- **Interactive terminal shell.** A full-screen shell to manage providers,
  accounts and servers, run the service, follow the angel log, read the node statistics, diagnose
  nodes and edit the global settings, with masked secrets. Start it with `netivri -i` (or
  `netivri sub -i`, `netivri account -i`, `netivri server -i`); it requires
  terminal stdin/stdout. See [`interactive shell`](docs/cli.md#interactive-shell).
  The screen has a fixed layout: a header (angel state, listen address,
  uptime), a tab line (Providers, Accounts, Servers, Service), and from 100 columns a
  list pane on the left with the selected item's card on the right; below
  that, a single pane that drills down. Arrows and Tab/Shift-Tab move, Enter
  opens, Esc goes back, `a` adds, `d` removes, `r` refreshes, `R` refreshes
  all, `c` shows a provider's countries, `/` filters a list in place, `m` marks accounts (`A` all, `M` clears), `v` shows an account's route, `b` applies a field to the marked ones, `?`
  opens help and `q` quits; the mouse clicks rows, tabs, lines and buttons
  and scrolls. Adding and editing are short wizard walks with a review step (adding a provider previews the countries its subscription offers, like `sub countries --url`; bulk edits on the Accounts screen use the same operations as the CLI's comma lists and `--all`);
  every list ends with a `+ Add` line, and an empty list says what it is with
  that line focused. Secrets are masked everywhere: the shell never prints a
  secret except the explicit, warned `Show client link` reveal on the Servers
  screen.
- **Browse and diagnose nodes from the shell.** On the Providers screen, `Nodes...` (`n`) lists the stored nodes of a provider (label, country and name — never addresses or credentials) and `Diagnose...` (`g`) runs the dial diagnostics of `netivri diag dial --node` on one node, with per-phase p50/p90/max figures; the same operations the `node list` and `diag dial` commands use.
- **Settings from the command line and the shell.** `netivri config get|set` reads and changes the global settings (for example `netivri config set listen.port 8080`); the shell's Settings tab edits the same keys with the same validation.
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
npm i -g @netivri/proxy
```

Upgrade with `npm i -g @netivri/proxy@latest`, remove with `npm rm -g @netivri/proxy`.

### Other options

You can also download an archive from [GitHub Releases](https://github.com/PHPCraftdream/netivri/releases)
and check it against `SHA256SUMS`. Options, upgrading while netivri runs and
the supported platforms (Linux on glibc and musl, such as Alpine; macOS; Windows) are in
[`docs/install.md`](docs/install.md).

`go install` does not work: the module vendors Mihomo through a `replace`
directive.

## Quick start

```sh
netivri -i
```

The shell starts on Accounts; use `netivri sub -i` for Providers and `netivri server -i` for Servers. It requires terminal stdin and stdout. `?` opens the key help inside the shell; `q` quits.

Direct egress is available when you intentionally want to bypass VPN nodes. It exposes this machine's address and requires empty provider and country. With explicit-direct-only accounts, a server update restarts the listener if the core cannot reconfigure it in place; other server transaction errors leave the running configuration unchanged.

```sh
# Optional: create an account with an explicit direct exit
netivri account add {ACCOUNT} --exit direct
```

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

### Run your own server

After `netivri up`, add two endpoints on the same `{PORT}`: a Shadowsocks 2022 listener and a VMess AEAD listener. TLS-family members also require protocol-owned SNI/ALPN match keys. Shadowsocks 2022 must use one of its three supported ciphers, and VMess must use AEAD (`alterId=0`); both examples use the existing account `alice`.

```sh
netivri server add --protocol shadowsocks --name {NAME} --account alice --port {PORT} --bind 127.0.0.1 --public-host {HOST} --param cipher=2022-blake3-aes-128-gcm
netivri server add --protocol vmess --name {OTHER_NAME} --account alice --port {PORT} --bind 127.0.0.1 --public-host {HOST} --param cipher=auto
netivri server list
netivri server link {NAME} --account alice --host {HOST}
netivri server link {NAME} --account alice --host {HOST} --format mihomo
netivri server update {NAME} --port {NEW_PORT} --public-host {HOST}
netivri server rotate {NAME} --account alice
netivri server remove {NAME}
```

URI and Mihomo YAML links contain credentials; handle them privately. See the
[server protocol index](docs/servers/README.md), [server CLI reference](docs/servers/cli.md),
and [server configuration](docs/config.md#server-listeners).

### A separate copy in this directory

Settings, subscriptions, tokens, statistics and the angel all live in one state
directory. `--local` makes `./.netivri` of the current directory that
directory, and once it exists every command run from there uses it with no flag
(`--home {DIR}` names any directory). The user-level installation is not
touched:

```sh
cd ~/work/experiment
netivri up --local --port 0     # a separate angel; any free port, so it does not clash with port 7890
netivri status                  # home: ./.netivri
netivri down
```

### A proxy before and after the VPN

Two optional proxies wrap the VPN node (`socks5://`, `http://` for an HTTP
CONNECT proxy such as Privoxy or Burp, or `https://` for one reached over TLS;
the names say `socks5` for historical reasons): `pre_socks5` is what netivri
connects through to reach the node, `post_socks5` is where the traffic goes
after the node (client → netivri → pre → VPN node → post → destination). The
type is whatever you write in the URL: netivri never guesses what the proxy
speaks, while its own port detects the protocol of each client. Set them for
everyone in `config.ktav`, override them per provider and override those per
account:

```ktav
chain: {
    pre_socks5: [
        socks5://pre.example:1080
        https://backup-pre.example:443
    ]
    post_socks5: [
        socks5://post.example:1080
        http://backup-post.example:8080
    ]
}

subscriptions: [
    {
        name: main
        url:: https://provider.example/sub/TOKEN
        pre_socks5: [
            http://proxy.example:8080
            socks5://backup.example:1080
        ]
    }
]

accounts: [
    {
        login: alice
        password:: init
        provider: main
        exit_country: DE
        post_socks5: [
            socks5://alice-exit.example:1080
            https://alice-backup.example:443
        ]
    }
]
```

```sh
# Set alternative pre and post proxies; repeat a side's flag to replace its full list.
netivri account add alice --provider main --country DE \
  --pre-socks5 socks5://pre.example:1080 --pre-socks5 https://backup-pre.example:443 \
  --post-socks5 socks5://exit.example:1080 --post-socks5 http://backup-exit.example:8080
netivri account set-socks5 alice --post none        # explicitly clear inherited post candidates
netivri account set-socks5 alice --post inherit     # clear the account list and inherit provider/global
netivri account show alice                          # redacted effective lists; never credentials

netivri sub add https://provider.example/sub/{TOKEN} --name main \
  --pre-socks5 http://proxy.example:8080 --pre-socks5 socks5://backup.example:1080
netivri sub set-socks5 main --post socks5://exit.example:1080 --post https://backup-exit.example:443
netivri sub set-socks5 main --pre none              # main clears inherited global pre candidates
netivri sub set-socks5 main --pre inherit           # back to global candidates

netivri sub rename main work                        # accounts and stored nodes follow
netivri sub set work --url https://provider.example/sub/{TOKEN}   # a new link; then: netivri sub update work
netivri sub set work --via none                     # stop fetching through a proxy
```

## SOCKS5 chain scope

The documented global/provider/account cascade applies to account routes: an
account can inherit global candidates, provider candidates replace global,
and account candidates replace provider. Lists are full replacement and never
merged. These repeated examples set alternative candidates; candidates are not
serial hops:

```sh
netivri sub add https://provider.example/sub/{TOKEN} --name main \
  --pre-socks5 socks5://pre.example:1080 --pre-socks5 https://backup-pre.example:443 \
  --post-socks5 http://exit.example:8080 --post-socks5 socks5://backup-exit.example:1080
netivri sub set-socks5 main --pre socks5://pre.example:1080 --pre https://backup-pre.example:443
netivri account add {ACCOUNT} --provider {SUBSCRIPTION} \
  --pre-socks5 http://pre.example:8080 --pre-socks5 socks5://backup-pre.example:1080 \
  --post-socks5 socks5://exit.example:1080 --post-socks5 https://backup-exit.example:443
netivri account set-socks5 {ACCOUNT} --post none
netivri account set-socks5 {ACCOUNT} --post inherit
```

| Global candidates | Provider candidates | Account candidates | Effective |
| --- | --- | --- | --- |
| *(empty)* | *(absent)* | *(absent)* | no proxy |
| `[socks5://g.example:1080]` | *(absent)* | *(absent)* | global list |
| `[socks5://g.example:1080]` | `[http://p.example:8080, https://p2.example:443]` | *(absent)* | provider list, replacing global |
| `[socks5://g.example:1080]` | `[http://p.example:8080]` | `[socks5://a.example:1081, https://a2.example:443]` | account list, replacing provider and global |
| `[socks5://g.example:1080]` | *(absent)* | `[none]` | no proxy |

These are candidate alternatives: weighted health lowers a hop's selection
weight after soft failures (score half-life 30 seconds); three hard failures
quarantine it for 30 seconds, doubling on re-trip to 600 seconds. Quarantined
hops are probed for revival. All-down pools still try candidates, never
bypassing a post proxy. Post hops bind per login and public-suffix-aware site
domain for the default 24-hour lifetime. Role-separated health files are
`{HOME}/run/hops-pre.health` and `{HOME}/run/hops-post.health`; state accepts
only lowercase 64-hex SHA-256 hop keys, never URLs. Logs contain no URLs:
quarantine/re-trip is WARN with role and the first eight key characters,
recovery is INFO with downtime, and a rate-limited all-down warning says
`all N hops are down, trying them anyway`.

Reading the pools: `netivri status` (short and `--full`, in Ktav or with `--json`)
adds `hop_pools` to the accounts whose route has a role with several hops:
per role the hop count, active and quarantined counts and, per hop, its short
opaque id, state (`active`, `quarantined`, `probing`), trips and the seconds until
the next revival probe. `netivri doctor` warns when every hop of a pool is down
and checks that the two `.health` files are readable; the Service tab of the
shell shows a line such as `post 3 hops: 2 up, 1 down` under the account. Hops
are never shown by URL or credentials.

```
netivri status --json
# "hop_pools": [{"role": "post", "hop_count": 3, "active_count": 2, "quarantined_count": 1,
#   "hops": [{"id": "1a2b3c4d", "state": "active", "trips": 0},
#            {"id": "5e6f7a8b", "state": "quarantined", "trips": 2, "next_revival_seconds": 42}, ...]}]
```

Account routes, including inherited global lists, use pools. A global post list
of several hops also serves connections without an account (the default route
and legacy auth) through the same pool; those connections have no login, so
their site bindings share one anonymous key. The default route has no pre side.

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
  degrades or never answers for that site. Design notes are in [`docs/design/`](docs/design/).
- **Configuration:** a Ktav file in the state directory
  ([`docs/config.md`](docs/config.md)); passwords are stored as argon2id
  hashes.

## Documentation

| Topic | Document |
|---|---|
| CLI contract, JSON, exit and error codes | [`docs/cli.md`](docs/cli.md) |
| Configuration file | [`docs/config.md`](docs/config.md) |
| Servers | [`docs/servers/README.md`](docs/servers/README.md) |
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

SPDX: `GPL-3.0-only`. See [`LICENSE`](LICENSE). The directory [`pkg/hopkit`](pkg/hopkit) (a standard-library-only
proxy-hop pool library) is `MIT OR Apache-2.0`.

The license is inherited from Mihomo and its dependencies (`sing`,
`sing-tun`), which are GPL-3.0 and are linked into the `netivri` binary, so
the program cannot be offered under MIT or Apache-2.0; the reasoning is in
[`docs/licenses.md`](docs/licenses.md), together with the third-party license
policy and the audited exceptions.
