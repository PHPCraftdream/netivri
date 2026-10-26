# Changelog

All notable changes to netivri are recorded here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the project uses
[Semantic Versioning](https://semver.org/). While the major version is 0, the
CLI contract in [`docs/cli.md`](docs/cli.md) may still change between minor
versions; every such change is listed here.

## [Unreleased]

## [0.3.0] - 2026-10-25

Release 0.2.0 was withdrawn before all its npm packages were published,
and npm never accepts a used version number again; everything listed for
it ships in 0.3.0.

### Added

- **Self-contained help.** `netivri help` now teaches configuration and
  operation without the repository: three concept topics (`netivri help
  config`, `netivri help routing`, `netivri help output`) under a new
  "Concepts" group in the overview, and the documentation references in the
  help text are absolute GitHub URLs instead of repository paths.
- **`netivri doctor`.** One-pass diagnosis with an exit code: checks the
  configuration, the angel, the listen address (or the configured port when
  nothing runs), the stored subscription snapshots (age against twice
  `fetch.update_interval`), every account's route (stored nodes; with a
  running angel also live nodes per tier) and the angel log for WARN or
  ERROR entries of the last hour. It prints one document on stdout
  (`checks` with `name`, `status` and `detail`, and a `summary` count) and
  ends with the new exit code 7 and `DOCTOR_FAILED` on stderr when a check
  failed. A check that cannot run is a `warn`, never a crash; subscription
  URLs, passwords and hashes are never printed.
- **Site bindings survive a restart or reload.** The statistics store (schema
  version 4, additive migration; the migration keeps existing data) records
  the site-to-node bindings by node label, so the angel reloads them when it
  starts or applies a configuration change: a site keeps its exit node across
  restarts instead of moving to a fresh pick. The angel writes the live
  bindings every 30 seconds and once more before the core closes; rows idle
  longer than the binding lifetime (24 hours) are deleted on every write and
  load. `status` `affinity` reports `restored` (bindings loaded at the latest
  core start) and `saved_at` (RFC3339 UTC of the last successful write).
  The table holds the visited sites (hostnames) per account; it is never
  printed or logged and expires with the binding.
- **`netivri status --watch`.** A live view of who is active: `netivri status --watch [--interval {DURATION}]` polls the angel every interval (default 2s, between 1s and 60s) and prints one frame per tick until interrupted: one row per account with state, connections, up and down per second, the busiest nodes and the countries, under a header with the listen address, uptime and `active_accounts`; the text frame is ASCII (a display name that is not ASCII shows `?` per non-ASCII rune). On a terminal each frame clears the screen; `--json` prints one JSON object per line instead (rates `null` on the first frame). When the angel stops the watch ends with `ANGEL_NOT_RUNNING` (exit 4) after a final line; a control timeout or any other poll error is a warning and retried (the watch polls without ever removing the state file), three in a row end the watch with `INTERNAL`; a state file that is gone, a foreign pid behind the control channel or a refused control port end the watch with `ANGEL_NOT_RUNNING`. `--watch` cannot be combined with `--full`; `--interval` without `--watch` is a usage error.
- **Node display names.** Subscriptions carry a display name per node
  (`Finland 1`); the statistics store (schema version 3, additive migration)
  records it and the per-node output shows it next to the hash-like label:
  `name` in `stats`, `account show` and `status` `activity[].nodes[]`, and in
  the log line when a route node goes down or comes back
  (`node own/a970e0d9fef1 (Finland 1, provider own, country FI) is down`).
  Names are cleaned of control characters and capped at 64 characters and are
  display only; without a name the field is omitted.
- **`listen.allowed_clients`.** Which client addresses may connect to the proxy:
  a comma-separated list of rules (`*`, `loopback`, an exact address, an IPv4
  whole-octet mask such as `192.168.1.*`, or a CIDR prefix such as
  `10.0.0.0/8`; default `*`, at most 64 rules, 1024 bytes). It is checked
  against the real peer address in the listener's accept loop, before any
  handshake and before authentication, so a refused connection is closed
  without a reply byte; it does not replace proxy authentication. A change is
  applied by the live reload; `netivri status` shows it as `allowed_clients`.
- **Per-account client rules.** An account may carry an optional
  `allowed_clients` with the same rule grammar:
  `netivri account add {ACCOUNT} --clients {RULES}`,
  `netivri account set-clients {ACCOUNT}[,{ACCOUNT}...] {RULES} | --all
  {RULES} | --none`, `account clone` copies the rule and `--clients` replaces
  it, and `account list` and the `status` account records carry it. The
  account rule is enforced during authentication, before the password is
  verified, and can only narrow the global rule, never widen it — so an `init`
  account can never be claimed from a disallowed address.
- **`netivri account clone {SOURCE} {ACCOUNT}`.** A new account from an existing
  one: the provider, the exit country and, by default, the password (the hash is
  copied) are taken over. `--password {PASSWORD}` sets a password of its own,
  `--password init` leaves it to the first client that connects, and
  `--provider` / `--country` replace the copied route.
- **`netivri account show {ACCOUNT}`.** Shows the account's configured provider
  and country lists and its route as ordered tiers: per node the label,
  provider, country and tier index and, while the angel runs, the last probe
  result (alive, delay) and the account's live connections through the node;
  pairs without nodes are listed as skipped tiers with a reason. Without a
  angel the static part is shown with `live` false. Passwords, hashes and
  subscription URLs are never shown.
- **Subscription quota in `sub list` and `sub update`.** Both report the
  `quota` parsed from the provider's `subscription-userinfo` header: `used`
  (upload plus download bytes), `total` and `expire` (RFC3339 UTC), all
  `null` when the provider sends no such header.
- **Per-account statistics.** The statistics store (schema version 2; the
  migration keeps existing data) records the dials and the traffic (bytes up
  and down per 5-second window) per account and node. `netivri stats --by
  account` reports per login: traffic, dial success rate, node count,
  countries and providers, sorted by login; `--account {ACCOUNT}` selects one
  login and its filters apply in every mode; with the node, country and
  provider views the rows come from that login's own samples as a dial/bytes
  subset.
- **Who is active, in `netivri status`.** The new `activity` list has one record
  per account: whether it is active (has open connections), the number of
  connections and bytes, and the nodes, providers and countries they use right
  now, the busiest first; `active_accounts` counts the active ones.
- **`netivri logs`.** Shows the angel log: `--level`, `--since`, `--tail`, and
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

- **The background process is called the angel.** Every place that said
  daemon now says angel: the `angel` field of `up`, `down` and `status`
  (was `daemon`), the error codes `ANGEL_NOT_RUNNING`,
  `ANGEL_START_FAILED`, `ANGEL_START_TIMEOUT` and `ANGEL_STOP_TIMEOUT` (were
  `DAEMON_*`), the `doctor` check `angel`, help and documents. Exit codes and
  the files in `run/` are unchanged, and log lines of older versions are still
  read. `schema_version` stays 1: 0.3.0 is the first release after the
  withdrawn ones.
- **New nodes no longer cut connections.** When a subscription refresh brings
  a changed node list, or an account gets a new country or provider list, the
  running angel takes the new routes in place: the listener stays, open
  connections finish on their node, and every node that stays keeps its
  health, probe schedule and site bindings (an account whose country or
  provider lists changed picks its nodes anew), so a node that was down is not
  reported down again. The log says `configuration applied, connections kept`.
  Adding or removing an account, a new password, `allowed_clients` or the port
  still restart the listener.
- `netivri doctor` no longer warns about the log for node-down reports alone:
  they are counted in the `log` detail ("besides N node-down reports"), and
  the `account` checks show whether the routes have live nodes. Other `WARN`
  and every `ERROR` entry still make the check warn.
- **`netivri status` is short by default.** The default answer keeps the
  angel, route, accounts, activity, refresh and reload fields and a reduced
  `warm` summary (`alive`, `total`, `probes`, `probe_failures`). The complete
  warm node list, the warm pool counters (`pool_hits`, `pool_misses`,
  `pool_discards`), `stats` and `affinity` moved behind `netivri status
  --full`; scripts that read those keys must pass `--full`. `warm` gained the
  `alive` and `total` counts, and each `warm.nodes[]` entry (full view) gained
  the additive `label` (`{PROVIDER}/{NODE_ID}`, omitted without account
  routes), `provider`, `country` and `node_name` next to the existing `name`,
  `alive` and `delay_ms`.
- Background probes back off while a node stays down: the probe interval
  of a down node grows 45 s, 90 s, 3 m to a 10 minute cap (45 s for nodes
  that are not down); a successful probe or dial resets it. Down
  detection, selection and hedging are unchanged.
- `scripts/npm-publish.sh` treats the answer "cannot publish over a previously
  staged version" (npm's registry view can lag a minute) as a version that is
  already published, so a re-run of a failed release goes on to the packages
  that are still missing.
- **npm: the Windows packages are `netivri-w32-x64` and `netivri-w32-arm64`.**
  npm refused the name `netivri-win32-x64` as spam on every publish, so the two
  Windows platform packages are renamed; the launcher still picks them by
  `process.platform` (`win32`), and the packages keep `os: win32`. Release
  archives, the installers and the CLI are not affected.

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
