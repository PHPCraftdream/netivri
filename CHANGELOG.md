# Changelog

All notable changes to netivri are recorded here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the project uses
[Semantic Versioning](https://semver.org/). While the major version is 0, the
CLI contract in [`docs/cli.md`](docs/cli.md) may still change between minor
versions; every such change is listed here.

## [Unreleased]

### Added

- **Automatic subscription refresh.** A running daemon re-fetches every
  subscription once an hour; `fetch.update_interval` changes the period
  (`5m` to `168h`) or turns it off (`off`). A fetched list that would leave an
  account without nodes is rejected and the old one stays. The result is in
  `status` under `refresh`.
- **Live apply of changes.** The daemon watches `config.ktav` and the stored
  subscriptions and applies a change to accounts, passwords, nodes or the port
  within a few seconds, with no `down` and `up`. The listener restarts on the
  same port, so open connections end; nothing restarts when the nodes are the
  same, only renamed, or when the daemon itself stores a claimed password. An
  invalid file or an empty route leaves the running configuration in place and
  is reported in `status` under `reload`. See `docs/design/live-reload.md`.

- **Several accounts at once.** `account set-provider` and `account
  set-country` take a comma-separated list of logins (`alice,bob`) or `--all`
  in place of a single login; `--none` works with both. A single login prints
  `{"account": ...}` as before, several print `{"accounts": [...]}`.

### Changed

- **Node check before a switch.** `account set-provider` and `account
  set-country` now check, before writing anything, that every account they
  change still has a stored node on its route (its subscription, or all of
  them when it has none, and its country). If one does not, nothing is
  changed for any account and the command ends with `NO_NODES_FOR_COUNTRY`
  (exit 5), naming the account. A value that does not change is not checked.
- **Placeholders in braces.** The help of the program, the README and the
  documents write every placeholder in curly braces (`{ACCOUNT}`,
  `{SUBSCRIPTION}`, `{COUNTRY}`, `{URL}`), and `{ACCOUNT}` (the login of an
  account) replaces the confusing `LOGIN NAME` of `set-provider`.
- **Ktav is the default output format.** Results and errors are written as
  Ktav, the format of the configuration file, with the same keys as before
  (`schema_version`, `ok`, `data` or `error`). `--json`, or `NETIVRI_OUTPUT=json`
  in the environment, writes JSON as before; scripts and agents should ask for
  it. If the Ktav library cannot be loaded the output falls back to JSON. Help
  is still text, and JSON with `--json`.
- A password claimed by the first client is written into the configuration
  file as it is on disk, not from the daemon's copy loaded at start, so an
  `account add` made meanwhile is no longer overwritten. A claim against a file
  that no longer holds the placeholder is refused and can be repeated.

## [0.1.0] - 2026-10-21

First public release.

### Added

- **Local proxy.** One port serves SOCKS5, SOCKS4 and HTTP; the protocol is
  detected from the first byte. It listens on loopback by default.
- **Mihomo core as a library.** [Mihomo](https://github.com/MetaCubeX/mihomo)
  v1.19.32 is embedded and vendored in `third_party/mihomo`, so the program
  is one binary with no external core process.
- **Subscriptions.** `sub add`, `sub list`, `sub update`, `sub rm` and
  `sub countries`. Clash/Mihomo YAML and base64 lists of share links are
  accepted; quota and expiry come from the `subscription-userinfo` header.
  A subscription is always fetched through a strict proxy and never silently
  directly. Nodes get stable ids.
- **Nodes.** `node list`, with filters by subscription and country.
- **Accounts.** `account add`, `account list`, `account set-country`,
  `account set-provider` and `account rm`: several named logins on one port,
  each with its own provider and exit country, routed independently.
- **Proxy authentication.** Passwords are stored as argon2id hashes in the
  Ktav configuration file. A password written as `init` is claimed on first
  use: the first password a client presents is hashed and stored.
- **Daemon.** `up`, `down` and `status`, and `up --foreground`. The control
  channel is a named pipe on Windows and a Unix socket elsewhere, never TCP.
- **Warm-up.** Background probes of every node and a pool of pre-dialled
  connections; the state is part of `status`.
- **Statistics.** Probe, connection and throughput samples are kept in a
  local SQLite database; `stats` reports them by node, country or provider.
- **Sticky group.** An account keeps one exit node per destination site, with
  health tracking and re-binding when the node degrades.
- **Hedged dial.** A VLESS handshake that is slower than usual gets a second
  attempt to the same node, within a small budget; the counters are in
  `status`.
- **`diag dial`.** Per-phase latency (DNS, TCP, TLS, first byte) with
  percentiles, failure classes and idle-connection survival for one account or
  node.
- **Installers.** `install.sh` and `install.ps1` verify checksums, upgrade in
  place even while the daemon runs, install from a local directory and can
  add the install directory to the user `PATH` without changing its registry
  type.
- **npm packages.** `npm i -g netivri` with six platform packages, no
  postinstall scripts and no downloads at install time.
- **Release archives** for Linux, macOS and Windows (amd64 and arm64) with
  `SHA256SUMS`, third-party license notices and a build provenance
  attestation.
- **Machine-readable CLI.** `--json` output, documented exit and error codes,
  and [`AGENTS.md`](AGENTS.md) for AI agents.
- **Built-in help.** `netivri`, `netivri help`, `--help` and
  `netivri {COMMAND} --help` print a readable overview or the usage, flags,
  detailed examples and possible errors of one command; `--json` gives the
  same as one JSON object for scripts and agents.

### Security

- Subscription URLs and `--via` proxies are never printed or logged.
- The proxy binds to `127.0.0.1` unless told otherwise.
- Releases are gated on a dependency license check; the project is
  `GPL-3.0-only` (see [`docs/licenses.md`](docs/licenses.md)).

### Known limitations

- `use`, `node test`, `logs`, `doctor` and `schema` are described in
  [`docs/cli.md`](docs/cli.md) but not implemented yet.
- Command results are documents only (Ktav by default, JSON with `--json`);
  there is no table view for people yet. Help (`netivri help`) is the one text output.
- No TUN mode and no change of the system proxy settings; these are non-goals
  for the 0.x line.
- Alpine and other musl-based Linux systems are not supported (the Ktav
  native library needs glibc 2.17 or newer).
- `go install` does not work, because `go.mod` uses a `replace` directive for
  the vendored Mihomo. Use a release archive, the installers or npm.
