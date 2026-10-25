# Changelog

All notable changes to netivri are recorded here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the project uses
[Semantic Versioning](https://semver.org/). While the major version is 0, the
CLI contract in [`docs/cli.md`](docs/cli.md) may still change between minor
versions; every such change is listed here.

## [Unreleased]

### Added

- **`netivri account clone {SOURCE} {ACCOUNT}`.** A new account from an existing
  one: the provider, the exit country and, by default, the password (the hash is
  copied) are taken over. `--password {PASSWORD}` sets a password of its own,
  `--password init` leaves it to the first client that connects, and
  `--provider` / `--country` replace the copied route.
- **Who is active, in `netivri status`.** The new `activity` list has one record
  per account: whether it is active (has open connections), the number of
  connections and bytes, and the nodes, providers and countries they use right
  now, the busiest first; `active_accounts` counts the active ones.
- **`netivri logs`.** Shows the daemon log: `--level`, `--since`, `--tail`, and
  `--follow` (with `--json`, one JSON object per line). The log lines now carry
  a UTC time and a level (`INFO`, `WARN`, `ERROR`); lines of the older format
  are read as `INFO`. The log is moved to `netivri.log.1` at a start when it is
  bigger than 8 MiB. It also records a route node going down (`WARN`) and coming
  back (`INFO`), with its label, provider and country, so that "why can't I
  connect" has an answer in `netivri logs --level warn`.
- **Several providers and countries per account, in priority order.** `provider`
  and `exit_country` of an account (and `--provider`, `--country`,
  `account set-provider`, `account set-country`) take a comma-separated list:
  `--provider main,backup --country DE,NL`. The countries come first, and inside
  a country the providers; a new connection goes to a node of the first tier
  that has a node not marked down, so the later ones are fallbacks. A pair
  without nodes is skipped, a route fails only when it has none at all. The
  lists are stored in a canonical form, and a provider in any account's list
  cannot be removed. A `*` in the country list means every country not named
  in it (`DE,*`: Germany first, then anywhere; `*` alone: any country), at its
  place in the order. Single values work as before. See
  `docs/design/accounts.md`.
- **More on the release page.** Every release also carries `install.sh` and
  `install.ps1` of the tag, Linux packages (`.deb` and `.rpm`, amd64 and
  arm64), a Scoop manifest (`netivri.json`), a Homebrew formula
  (`netivri.rb`) and the seven npm tarballs, all listed in `SHA256SUMS` and
  covered by the build provenance. The amd64 `.deb` and `.rpm` are installed
  and removed again by the release workflow before it publishes anything.

### Changed

- Background probes back off while a node stays down: the probe interval
  of a down node grows 45 s, 90 s, 3 m to a 10 minute cap (45 s for nodes
  that are not down); a successful probe or dial resets it. Down
  detection, selection and hedging are unchanged.
- `scripts/npm-publish.sh` treats the answer "cannot publish over a previously
  staged version" (npm's registry view can lag a minute) as a version that is
  already published, so a re-run of a failed release goes on to the packages
  that are still missing.

## [0.1.0] - 2026-10-22

First public release.

### Added

- **Local proxy.** One port serves SOCKS5, SOCKS4 and HTTP; the protocol is
  detected from the first byte. It listens on loopback by default.
- **One self-contained binary.** [Mihomo](https://github.com/MetaCubeX/mihomo)
  v1.19.32 is embedded as a library and vendored in `third_party/mihomo`, and
  the Ktav configuration format is read and written by the pure-Go backend of
  `ktav-lang/golang` v0.8.1: no external core process and no native library
  next to the binary.
- **Subscriptions.** `sub add`, `sub list`, `sub update`, `sub rm` and
  `sub countries`. Clash/Mihomo YAML and base64 lists of share links are
  accepted; quota and expiry come from the `subscription-userinfo` header.
  A subscription is always fetched through a strict proxy and never silently
  directly. Nodes get stable ids. The nodes of a subscription are stored in
  `subscriptions/{NAME}.ktav`.
- **Automatic subscription refresh.** A running daemon re-fetches every
  subscription once an hour; `fetch.update_interval` changes the period
  (`5m` to `168h`) or turns it off (`off`). A fetched list that would leave an
  account without nodes is rejected and the old one stays. The result is in
  `status` under `refresh`.
- **Nodes.** `node list`, with filters by subscription and country.
- **Accounts.** `account add`, `account list`, `account set-country`,
  `account set-provider` and `account rm`: several named logins on one port,
  each with its own provider and exit country, routed independently.
  `set-country` and `set-provider` take one login, a comma-separated list
  (`alice,bob`) or `--all`, and `--none` clears the value. Before writing
  anything they check that every account they change still has a stored node
  on its route; if one does not, nothing is changed and the command ends with
  `NO_NODES_FOR_COUNTRY` (exit 5), naming the account.
- **Proxy authentication.** Passwords are stored as argon2id hashes in the
  Ktav configuration file. A password written as `init` is claimed on first
  use: the first password a client presents is hashed and written into the
  file as it is on disk, so an `account add` made meanwhile is kept.
- **Daemon.** `up`, `down` and `status`, and `up --foreground`. The control
  channel is a named pipe on Windows and a Unix socket elsewhere, never TCP.
- **Live apply of changes.** The daemon watches `config.ktav` and the stored
  subscriptions and applies a change to accounts, passwords, nodes or the port
  within a few seconds, with no `down` and `up`. The listener restarts on the
  same port, so open connections end; nothing restarts when the nodes are the
  same, only renamed, or when the daemon itself stores a claimed password. An
  invalid file or an empty route leaves the running configuration in place and
  is reported in `status` under `reload`. See `docs/design/live-reload.md`.
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
- **Machine-readable CLI.** Results and errors are documents with the keys
  `schema_version`, `ok` and `data` or `error`, written as Ktav by default;
  `--json` after the command, or `NETIVRI_OUTPUT=json`, writes JSON. Exit and
  error codes are documented, and [`AGENTS.md`](AGENTS.md) explains the
  contract to AI agents.
- **Built-in help.** `netivri`, `netivri help`, `--help` and
  `netivri {COMMAND} --help` print a readable overview or the usage, flags,
  detailed examples and possible errors of one command, with every placeholder
  in braces (`{ACCOUNT}`, `{SUBSCRIPTION}`); `--json` gives the same as one
  JSON object for scripts and agents.

### Security

- Subscription URLs and `--via` proxies are never printed or logged.
- The proxy binds to `127.0.0.1` unless told otherwise.
- Releases are gated on a dependency license check; the project is
  `GPL-3.0-only` (see [`docs/licenses.md`](docs/licenses.md)).

### Known limitations

- `use`, `node test`, `logs`, `doctor` and `schema` are described in
  [`docs/cli.md`](docs/cli.md) but not implemented yet.
- Command results are documents only (Ktav by default, JSON with `--json`);
  there is no table view for people yet. Help (`netivri help`) is the one text
  output.
- No TUN mode and no change of the system proxy settings; these are non-goals
  for the 0.x line.
- Alpine and other musl-based Linux systems are not supported: the Linux
  release is built and tested for glibc only.
- `go install` does not work, because `go.mod` uses a `replace` directive for
  the vendored Mihomo. Use a release archive, the installers or npm.
