# Changelog

All notable changes to netivri are recorded here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the project uses
[Semantic Versioning](https://semver.org/). While the major version is 0, the
CLI contract in [`docs/cli.md`](docs/cli.md) may still change between minor
versions; every such change is listed here.

## [Unreleased]

### Added

- **The build is named in the log and in `netivri version`.** The angel's `start` line ends with
  `version=0.1.0+05f31ce` (version, and after a `+` the commit when the binary knows it; `-dirty` follows
  a commit built from a modified tree), the crash dump header has the same, and `netivri version`
  prints a `commit` field when there is one. Every dry-run build has the same version, so until now
  a running angel could only be told apart by its start time.

- **Crash dump of the angel.** A panic or a fatal runtime error now also writes the
  traceback of all goroutines to `run/crash/` (with the start time, pid and version of
  that angel). A clean stop removes the file; the next start keeps the dump as
  `crash-{TIME}-{PID}.txt` (the 10 newest), logs one `ERROR` line with its name, and says
  in one `INFO` line when the previous angel was killed or lost power without a dump.

- **Preview a subscription before adding it, in the shell.** The provider add wizard gains a
  `Preview countries` step after the Via step: it fetches the URL through the same strict
  path as `netivri sub countries --url` (same figures: nodes, each country with its node
  count, nodes with no country) without storing anything, shows a busy state while it runs
  and Esc cancels the fetch. Back returns to the Via step to change the URL; a failed or
  canceled preview does not block adding. The URL and the via proxy are never shown.
- **Route and bulk edit on the Accounts screen.** `Route...` (`v`) shows the route tiers of
  `netivri account show` with the live node state and connection counts while the angel
  runs. Space or `m` marks accounts, `A` marks all, `M` clears the marks, and
  `Apply to marked...` (`b`) applies provider, countries, exit, allowed clients or hops to
  every marked account through the same operations as the CLI's comma lists and `--all`
  (one invalid account changes nothing). With these the shell covers every CLI command
  except the reasoned exemptions listed in docs/design/parity.md.
- **Logs tab in the shell.** The tail of the angel log with follow on by default (new
  lines appear as the angel writes them; scrolling up reads in the past, `f` or End
  returns to the live tail), a level filter (`l`), in-pane search (`/`), and since (`s`)
  and tail (`n`) prompts — the shell side of `netivri logs`, through the same reader.

- **Stats tab in the shell.** The recorded node health, delay and throughput of
  `netivri stats`, per node or per account (`b` toggles), with the window
  (`since`) and the subscription, country, node and account filters as one-line
  prompts (an empty answer clears a filter) and `r` to reload. The screen and
  the command share one operation, so the figures are identical; the view is
  read-only and shows no secrets.

- **`config get` / `config set` and the Settings tab.** Read and change
  the global settings of `config.ktav` from the command line
  (`netivri config get [{KEY}]`, `netivri config set {KEY} {VALUE}`),
  with full-file validation before anything is written, secrets stored
  hashed and never echoed, and a `restart_needed` flag in the answer.
  The shell gains a Settings tab editing the same keys with the same
  validation; the commands and the tab are two faces of the same
  operations.

- **Nodes and dial diagnostics in the shell.** The Providers screen gains `Nodes...` (`n`), a read-only dialog of the provider's stored nodes (label, country and name; `/` filter, `c` country cycle, no addresses, ports or credentials), and `Diagnose...` (`g`), which runs the dial diagnostics of `netivri diag dial --node` for one node through the same operation: label choice, samples count (default 5, 1..20), background run with Esc cancel, and per-phase p50/p90/max in ms with failure counts (no idle test, as with `--idle off`). Both actions are disabled while the provider has no stored snapshot; CLI behavior is unchanged.

- **Service tab in the shell.** Start, stop and restart the angel (stop and restart ask first and name the open
  connections that will drop), run the checks of `netivri doctor`, and see the live activity of the accounts, with
  the same operations the `up`, `down`, `status` and `doctor` commands use.

- **CLI for what only the shell could do.** `netivri sub rename {OLD} {NEW}` (accounts and stored nodes
  follow), `netivri sub set {NAME} [--url] [--via]` (new subscription URL or fetch proxy, never echoed) and
  `netivri server update {NAME} [--public-host] [--bind] [--port] [--account] [--param]`. First step of the
  CLI/shell parity work ([`docs/design/parity.md`](docs/design/parity.md)); a test now fails when a command, flag,
  screen or card line has no counterpart on the other side and no reasoned exemption.

- **Proxy hops per provider.** A provider (`subscriptions[]`) can set `pre_socks5` and
  `post_socks5` (`socks5://`, `http://` or `https://`, or `none`), via `netivri sub add
  --pre-socks5/--post-socks5`, the new `netivri sub set-socks5`, and the Pre-hop/Post-hop fields of the
  Providers screen in the shell. The hops now cascade global, then provider, then account; the more
  specific level replaces the one above, nothing is stacked, and `none` overrides a hop set above.
  Nodes of different providers on one account route take their own provider's hops. `account show`
  and `status` report the source `provider` (and the value `varies` when an account's providers
  differ). `sub list` shows the redacted provider hops when set. Existing configs behave as before.

- **Interactive shell v2.** The screen of `netivri -i` is rebuilt: a fixed
  frame with a header, a tab bar and one-line toast/hint slots, list and card
  panes side by side from 100 columns (a drill-down single pane below that),
  wizards with review steps, buttons, mouse support and an in-pane list filter
  (`/`). See [`docs/cli.md`](docs/cli.md#interactive-shell).
- **Servers screen with full management.** Servers are no longer read-only in
  the shell: an add wizard driven by the protocol's parameter schema (with a
  review step), field editing, `Show client link` (the one explicit, warned
  reveal of a secret), rotate keys and remove.
- **Provider rename in the shell.** Renaming a provider updates the accounts
  that use it; the stored snapshot is moved first and the config is saved
  after, so a failure leaves the old state.
- **Account clone in the shell.** `Clone...` on the Accounts screen copies
  provider, countries, exit and clients and gives the copy a fresh placeholder
  password.
- **Interactive terminal shell: `netivri -i`.** The flag `-i` / `--interactive`
  (alone, or with `sub`, `account` or `server`) opens a full-screen shell with a
  status header (angel running or stopped, listen address, uptime) and three
  tabs. Providers: list, add (the URL is a masked field and is never shown),
  refresh one or all, remove (the confirmation names the accounts that use
  it), countries with node counts. Accounts: list with live rates, route
  tiers, add, edit (only the changed fields are sent; socks5 hops are masked),
  clone, remove; a password is never shown or set there. Servers: full
  management as above. It works with the angel stopped, the
  running angel picks the edits up on its own, and without a terminal (or with
  `--json`) the mode fails with `NOT_A_TERMINAL` (new, exit 2) or a usage
  error. New dependency: Bubble Tea v2 (MIT). See `netivri help interactive`
  and [`docs/cli.md`](docs/cli.md#interactive-shell).
- **A node that does not reach one site no longer holds it.** A node can pass its probes and still never reach one destination (Telegram data centers through a US exit, for example), so the connections to it waited for an answer that never came while the route never left the node. Now, when 3 connections of an account to one site through a node sent data, waited at least 5 seconds and got nothing back within 10 minutes, with none answered, while the node itself still answered probes and dials, the node is blocked for that account and site: the site's next connection takes the next node of the route (a worse tier included) and other sites stay put. The block lasts 30 minutes and doubles on every repeat up to 8 hours; an answer through the node ends it. The log says it at `INFO` (`account alice via NODE: 2 sites got no answer through this node ...`, and `moved from NODE to NODE: no answer`) and names the destinations as `host:port` at `DEBUG`, including an hourly `destinations without an answer` line; `INFO` lines still name no site. Blocks are kept in memory. See `netivri help routing` and `netivri help logs`.
- **Dial timeouts also move an unreachable site.**
  A dial through a node that times out now counts like a silent connection
  towards blocking that node for the site (3 within 10 minutes, none answered),
  so a destination blackholed at connect level also moves to the next node.
- **Never-answered nodes no longer fill the angel log.**
  In each 30-second window, the first two unique servers are `INFO`; the third starts
  one `INFO` summary and is also detailed at `DEBUG`, with later unique servers at
  `DEBUG`. Twins keep their annotation and the server’s chosen level (`DEBUG` when
  collapsed), without counting again. Expiry flushes a changed summary once.
- **Quieter log in a long network failure.** A network that fails slowly (nodes go down minutes apart, so there is no burst) now raises one `node outage` warning when at least half of the servers that have answered (6 or more) are down; their down and back lines are `DEBUG` until fewer than a quarter are still down, and one `node outage over` line closes it. A subscription whose refresh keeps failing writes one `WARN` an hour instead of one per try, and the refresh that ends the failures says how many there were. The hourly line of an account names the two nodes most of its connections without an answer went through (`via NODE x300`). See [`docs/cli.md`](docs/cli.md).
- **Angel log on stderr for a foreground run.** `NETIVRI_LOG_STDERR=1 netivri up --foreground` writes the angel log (debug level included) to stderr; without the variable a foreground run stays silent. The e2e tests set it, so a failed run shows what the angel did.
- **Protocol servers and account routing.** Add and manage anytls, hysteria2,
  mieru, shadowquic, shadowsocks, snell, sudoku, trojan, trusttunnel, tuic,
  vless-reality and vmess listeners with `netivri server` CLI commands. Each
  authenticated connection uses its account's route. Generate client links or
  Mihomo YAML; server allowlists filter clients. UDP-bound listeners have a
  restart lifecycle, and UDP relay payloads are dropped and counted. See
  [`docs/servers/README.md`](docs/servers/README.md).
- **Shared TCP ports add authenticated trial protocols.** TLS-family listeners, Shadowsocks 2022 (three ciphers) and VMess AEAD (`alterId=0`) may share under one eligibility rule; legacy Shadowsocks AEAD, Snell, Sudoku, Mieru and UDP remain exclusive. Trials are bounded, precomputed per lease and silently drained on rejection. Shared Shadowsocks uses one PSK/listener/account; raw PSKs and VMess UUIDs must be distinct. Status includes nonzero `replayed` and `ambiguous` trial counters, omitting zeros; `PORT_IN_USE` distinguishes non-shareable conflicts from `SERVER_PORT_AMBIGUOUS`. See [`docs/servers/README.md`](docs/servers/README.md).

- **Explicit direct exits per account.** `accounts[].exit` opts an account into direct egress, optionally via the effective post proxy. Toggles apply in place only while at least one account route has stored nodes both before and after; boundary and direct-only changes restart. Server-only changes that a direct-only core cannot apply in place now restart too. Keeping another node-backed account route through a flip preserves connections. Adding an account always restarts. Direct requires empty provider/country; inherited pre-proxy is ignored but an explicitly configured account pre-proxy is rejected. This is never a fallback and exposes this machine's address to destinations. See [`docs/design/exit-direct.md`](docs/design/exit-direct.md).

### Changed

- The interactive screens use the new frame and layout of shell v2 (see Added).

- Account writes now validate the entire resulting configuration before saving. Validation failures return `CONFIG_INVALID` and persist no changes; batch account writes are atomic.

- **A new access rule for one account no longer drops everyone.** `account set-clients` (an
  `allowed_clients` change of accounts only) is applied in place to the servers and
  the mixed port: open connections stay, the new rule holds for new ones. Adding or
  removing an account, new credentials, bind or port still restart the listener.

- **The angel survives a core that will not start.** When a replacement core and the
  rollback to the previous one both fail to start (a port that is not free yet), the
  angel no longer exits: `status` shows `core: "down"`, the previous good
  configuration is retried after 1 s, 2 s, 4 s ... up to 30 s, a newer valid
  configuration is tried at once, and `down` still stops it.

- **Fewer, faster node probes.** A round probes up to 24 nodes at once (was 8),
  so a round over three dozen nodes in a dead network ends inside one probe
  interval instead of after 75 s. A fallback node that carries no traffic (a
  worse tier, with a better node up) is probed every 3 minutes while it
  answers, which also quiets its flapping lines; a failed probe, traffic, or
  the better tier going down puts it back on 45 s. A server that two providers
  list with identical settings, or one node of two accounts, is probed once and
  the result is recorded for each.

- **The log says which stage of a route failed.** For a route with a `pre_socks5`
  or `post_socks5` hop, the failed dials of a minute are one line per stage
  (`account alice via {NODE}, post hop: 3 dials failed (proxy auth failed x3)`,
  with `pre hop`, `node` or `unknown hop`), and the reason a node went down
  carries it too (`3 dial failures (timeout, post hop)`). The stage is found
  from the address Mihomo names in the error, or from the one stage that speaks
  the protocol of an address-less refusal; the address itself is never printed.
  A route that is only a node reads as before.
- **A separate state directory for a separate run.** `--local` makes `.netivri`
  in the current directory the state directory (created, owner-only, with a
  `.gitignore` that ignores everything in it), and once that directory exists
  every command run from there uses it with no flag; the current directory
  only, not its parents. `--home {DIR}` still names any directory, and the two
  cannot be combined. Order: `--home`, `--local`, an existing `./.netivri`,
  `$NETIVRI_HOME`, the user config directory. Settings, tokens, subscriptions,
  statistics and the angel of each directory are independent, so a second
  angel runs next to the first (give it `--port`, a new directory has the
  default port 7890). `status` and `doctor` print `home` and `home_source`.

- **The log says how slow the answers are.** The hourly summary of an account
  ends with how long the node's side took to start answering (median and 95th
  percentile, from the first bytes the client sent to the first byte back), and
  a minute in which at least 10 connections got an answer with a median of 2
  seconds or more gets an `INFO` line `slow answers`. The summary of moved sites
  names a side that is a single node.
- **HTTP and HTTPS proxies as hops.** `pre_socks5` and `post_socks5` (global
  and per account, `account set-socks5`, `--pre-socks5`, `--post-socks5`) take
  `http://` (an HTTP CONNECT proxy, such as Privoxy or Burp) and `https://` (the
  same over TLS, certificate checked) as well as `socks5://`; the port stays
  required and the setting names are unchanged. The listening port already
  speaks SOCKS5, SOCKS4 and HTTP on one socket. A hop that does not take the
  credentials or will not connect shows in the log as `proxy auth failed` or
  `proxy refused`, not as an unknown `error`.
- **The log tells when the computer slept.** The angel is frozen while the
  computer sleeps, so hours of sleep looked like hours of silence and the
  nodes seemed to fail all at once afterwards. The angel now notices the jump
  of the clock, writes one `INFO` line with the length of the sleep, probes
  every node at once and treats failures in the next 2 minutes as expected:
  `INFO` lines, no burst, no flapping, no `WARN` in the per-minute connection
  lines.
- **Moved sites are one line a minute.** Sites of one account that moved
  between several pairs of nodes for one reason (a node went down) are one line
  with the counts instead of one line per pair; the pairs are `DEBUG`.
- **A failed subscription refresh is quiet while it does not matter.** A fetch
  that fails on the way is tried once more after 5 seconds. A failure is an
  `INFO` line saying what stays and when the next try is while the stored list
  is recent, and a `WARN` only with the third failure in a row, a list older
  than twice the interval or no list, so `doctor` no longer warns for a
  timeout that the next try fixes. A successful refresh says how long it took.
- **CI tests what the diff touches.** A push or pull request runs the tests of
  the packages the diff changes and of every package that imports them; a
  change that is not a file of one package (`go.mod`, workflows, scripts,
  `third_party`, npm, packaging) runs everything, and documents alone run no
  test. A line `ci: full` in the head commit message, or a manual run of the
  `CI` workflow (inputs `packages`, `tags`, `run`, `os`, `race`), runs
  everything. Coverage floors and the packaging jobs run only on a full run.

### Fixed

- **Two accounts that reach the same node through different proxy hops no longer share one health
  record.** A node used directly by one account and through a dead pre-hop by another had one record
  keyed by its label, so the first account's successful dials cleared the second one's failures and
  it never moved to its fallback; one failed probe of a node present in two routes also counted
  twice and marked it down at once. Health is now kept per node and hop chain, and each physical
  probe counts once per record.
- **A site's recent answer is remembered per node.** A late answer on an old connection overwrote the
  answer of the node the site had moved to, so three silent connections could block a node that was
  answering. The nodes that answer are now remembered one by one.
- **A failed reload no longer leaves a probing loop behind, and the evidence of silent sites is pruned
  at most once a minute.** The replacement warmer was never closed when the listener change failed, and
  a full table was scanned under the routing lock for every new silent destination. A panic in the
  plain read or write of a connection is now contained and reported like one in the buffered path.
- **The statistics database recovers from an interrupted upgrade and no longer grows without bound.**
  An upgrade that stopped after adding the node-name column but before saving the schema version failed
  every later start with "duplicate column" and switched statistics off; the schema steps and the version
  are now one transaction. The nodes and accounts that no longer have any sample or binding are removed
  with the old samples, and the writer's id caches are cleared with them.
- **`netivri stats --since 30d` counts the recent samples.** A window longer than the raw retention read
  only the hourly table, so a fresh database showed no nodes at all and an older one lacked its last
  seven days; it now combines both. The node figures are read in one transaction, so a probe written
  in between can no longer give a delay with no probes, and `--node` filters in the query instead of
  loading every node's samples.
- **`netivri up --foreground` no longer starts a second angel while the first one is busy.** A control
  answer later than two seconds counted as "no angel", so a second proxy started in the same home and
  overwrote the state file, and `down` could no longer reach the first one. Both start paths now stop
  with "angel not responding" and refuse any probe error that does not prove the angel is gone.
- **A control response over 64 KiB no longer makes `down` say "stopped" for a running angel.** The
  response limit was the state-file limit, a large status was cut, and the state file was deleted.
  Responses now have their own 4 MiB limit, and a malformed or oversize answer keeps the state file.
- **`netivri doctor` fails when the angel runs with its core down.** After a failed reload and a failed
  rollback the angel stays up with no listener, and `angel` and `listen` still reported `ok`; both now
  fail and the angel line carries the (redacted) reload error.
- **An `allowed_clients` rule written as an IPv4-mapped IPv6 CIDR now matches.** `::ffff:192.168.1.0/120`
  was accepted but denied every peer; it is now read as `192.168.1.0/24`, and a mapped prefix shorter than
  `/96` is rejected.
- **A first login on the legacy single `auth` can no longer overwrite a password set in the meantime.**
  The claim was refused only when neither field was `init`; it is now refused when either field differs
  from what the angel loaded.
- **A subscription URL no longer leaks through the `Referer` header on a redirect.** Go's HTTP client
  copies the previous URL, path and query included, into `Referer` when a subscription redirects to
  another host; subscription URLs are credentials, so the header is now dropped on every hop.
- **Two `netivri` processes can no longer overwrite each other's server credentials.** The server
  secrets file was guarded by a lock that exists only inside one process, so two rotations running
  at once could bring back a revoked password and both report success. Every change now holds a file
  lock across its read and write.
- **`server update` with new `cert_file` / `key_file` paths now uses them.** The old certificate paths
  were kept, so the listener and the client links stayed on the old pair; a stored certificate with
  a missing key is repaired as one pair instead of getting a key that does not match.
- **A subscription snapshot that cannot replace its old `.ktav` file reports the failure.** The refresh
  said it succeeded while the old snapshot kept being loaded.
- **`sub rename` refuses a name that differs only in letter case.** On Windows and macOS both names
  are one snapshot file, and the rename deleted it.
- **Shell screens no longer lose their timers, input or results when you switch tabs or act quickly.**
  Typing `q` or `?` in the Logs search quit the shell or opened the help instead of going into the
  filter; the Service and Logs polling stopped for good after you left the tab; a slow Stats read
  could overwrite a newer grouping or filter; and "Clear pre-hop" or "Clear post-hop" applied to the
  provider selected when the save ran instead of the one you chose. Each now keeps its own target.
- **A provider named `any` is no longer mistaken for "Any provider" in the account wizard.**
  Choosing it, or typing it in the provider edit, stored an empty provider list, so the account could
  use the nodes of every provider. The wizard now keeps the literal name, and the country hint follows it.
- **Long client links in the server wizard can be scrolled to the end.** The scroll limit counted the
  source lines instead of the wrapped rows, so a long VMess link could not be scrolled with Down, PgDn
  or the wheel.
- **The server wizard picks a free port for an IPv6 bind address.** `::1` produced the invalid
  address `::1:0` and the creation failed.
- **`netivri doctor` checks the transport the server really binds.** A Hysteria2 or TUIC server on a
  UDP port taken by another program was reported free because only TCP was tried, and an unrelated
  TCP listener gave a false warning.
- **A bare `--` ends flag parsing for good.** `netivri account clone -- alice -bob` rejected `-bob`
  as an unknown flag after taking `alice`.
- **A node whose connections mostly got no answer is a `WARN` in the one-minute line too, not only
  in the hourly summary.** `138 connections ended, 1 broken, 127 without an answer` was an `INFO` line,
  and a minute with no broken connection at all produced no line for the node. Now at least 20
  connections that ended with half or more broken or without an answer make a `WARN` line for the
  node (still `INFO` right after the computer woke up).
- **The log line of a block by the share rule no longer says "the rest were answered" when none was.**
  Without any answer in the window it says that connections heard nothing back within 10 minutes
  though a connection of the site was still being answered.

- **A lossy path no longer keeps a site on a node for hours.** A node was blocked for a site only
  when none of the site's connections was answered, so a destination that answered a few connections
  (slowly) and left most silent was never blocked: one hour of the log had 1632 of 1760
  connections to one Telegram address without an answer. Within 10 minutes, 20 or more silent
  connections that are at least 90 percent of those that were silent or answered at first bytes now
  block the node for that site, and an answer does not lift such a block.
- **The hourly account summary is a `WARN` when most connections failed** (at least 20 ended and half
  or more broken or without an answer); it was an `INFO` line in the hour above. The log line of a
  block by the new rule says `got mostly no answer (22 of 24 connections ...)`.

- **A node that came back after the computer slept no longer "was down" for the whole sleep.**
  `is back after 31h57m37s down` counted 25 hours of sleep, during which the angel is frozen;
  the line now gives the time awake and names the sleep (`after 6h10m45s down, not counting
  25h46m52s asleep`), as do the end of a node burst and of a node outage.
- **A panic on one connection no longer takes the angel down.** The angel crashed once
  (`buffer overflow: capacity 32768,start 0, need 2` in the VLESS Vision padding): a buffer
  that came without room for the padding header panicked in the goroutine of a single
  connection. Vision now copies such a buffer into one that has the room (patch 8 of the
  vendored Mihomo), and a panic below a connection closes only that connection and writes a
  `WARN` line with the panic and its two top frames.
- **Installing the `.rpm` on a current Fedora.** The `.rpm` is unsigned and recent rpm
  refuses such a package (`does not verify: no signature`), which also failed the Fedora
  install check of the release workflow. The check and `docs/install.md` now use
  `rpm -i --nosignature` (verify the file against `SHA256SUMS`).

- **Direct-only server reloads fall back safely.** Adding servers when explicit direct accounts leave no in-place route table now restarts the listener on `ErrReconfigureUnsupported`; server transaction errors still remain visible without a restart.

- Account commands no longer write a configuration that the next load refuses (account add init).

- **A busy angel is no longer taken for a dead one.** `status`, `down` and `up`
  asked the control channel once and, when the answer took longer than 2 s (an
  angel busy restarting its core), deleted the state file of the live angel:
  from then on `status` said "not running", `down` could not find it and `up`
  started a second angel. A slow answer now ends in `ANGEL_NOT_RESPONDING`
  (exit 1, retry in a moment) and changes nothing; only a refused connection or
  another pid answering still clears a stale state file.

- **A TCP and a UDP server may share a port number.** The port conflict check of
  `server add` and of the configuration is now per transport: Trojan on TCP 443 and
  Hysteria2 on UDP 443 can coexist, and a UDP server may use the proxy port; two
  servers of the same transport on overlapping binds are still refused
  (`PORT_IN_USE`).

## [0.1.0] - 2026-10-28

First release under this number, and the first one published to the `@netivri`
npm organization. The earlier releases 0.1.0 (22 October) and 0.2.0 were
withdrawn before all their npm packages were published, a planned 0.3.0 was
never tagged, and npm never accepts a used version number again; in the new
organization 0.1.0 is free. Everything that was listed for those releases
ships here. The sections below describe the
first public release as a whole; "Changed" lists what differs from the
withdrawn pre-releases.

### Added

- **SOCKS5 proxies before and after the VPN.** An optional `pre_socks5` is the
  SOCKS5 proxy netivri connects through to reach the VPN node, and an optional
  `post_socks5` is the proxy the traffic is handed to after the VPN node, which
  then connects to the destination. Both are set globally (`chain.pre_socks5`,
  `chain.post_socks5` in `config.ktav`) and per account (`pre_socks5`,
  `post_socks5`); an account value overrides the global one for that side, an
  absent value inherits it, and `none` cancels the global value. New commands
  and flags: `account add --pre-socks5/--post-socks5` and `account set-socks5
  {ACCOUNT} [--pre ...] [--post ...]` (URL, `none` or `inherit`);
  `account clone` copies both. A URL is `socks5://[user:password@]host:port`
  with a required port; credentials are never printed: `account list`,
  `account show` and `status` show `socks5://host:port` with `source`
  (`account`, `global` or `none`) and `auth`. The global post also applies to
  traffic of accounts without a route and to unauthenticated traffic; an
  account without a route gets its post (and a pre never applies, there is no
  VPN node). Probes test the whole chain once per node. A change of pre, of an
  account's post and of nodes is applied in place and keeps open connections;
  a change of the global post, or a route that appears or disappears
  entirely, restarts the listener. See
  [`docs/design/socks5-chain.md`](docs/design/socks5-chain.md).
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
- **Automatic subscription refresh.** A running angel re-fetches every
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
- **Angel.** `up`, `down` and `status`, and `up --foreground`. The control
  channel is a named pipe on Windows and a Unix socket elsewhere, never TCP.
- **Live apply of changes.** The angel watches `config.ktav` and the stored
  subscriptions and applies a change to accounts, passwords, nodes or the port
  within a few seconds, with no `down` and `up`. The listener restarts on the
  same port, so open connections end; nothing restarts when the nodes are the
  same, only renamed, or when the angel itself stores a claimed password. An
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
  place even while the angel runs, install from a local directory and can
  add the install directory to the user `PATH` without changing its registry
  type.
- **npm packages.** `npm i -g @netivri/proxy` with six platform packages
  (`@netivri/proxy-{linux,darwin,w32}-{x64,arm64}`), no postinstall scripts and
  no downloads at install time.
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

### Changed

- **Sites go back to the preferred tier.** A binding that sat in a fallback tier
  (an exit country or provider later in the account's list) used to stay there
  for as long as the site was used, even after the first tier was back: on a
  real setup 11 of an account's connections were on NL, FR and PL exits while
  its US node was up and idle. Now, when a node of a better tier has been up for
  5 minutes, the next connection of the site goes back to it (open connections
  stay; a node that flaps keeps restarting the five minutes). `status --full`
  counts it in `affinity.returns`, `account show` marks the later tiers
  `fallback` and gives `fallback_connections`, and `doctor` notes the
  connections that are still on fallback tiers. In `internal/core`,
  `Options.TierReturnAfter` (0 is 5 minutes, negative turns it off).
- **The Linux binary is really static now.** It was dynamically linked against
  glibc (`/lib64/ld-linux-x86-64.so.2`) even with `CGO_ENABLED=0`, because the
  Ktav binding's root package links the native-library loader (`purego`) that
  netivri never used; it now imports only the binding's pure-Go subpackage, and a
  test and the release workflow guard it.
- **The Linux binary runs on musl (Alpine) as well.** It is a static Go build, so
  one Linux package serves glibc and musl: the Linux npm packages no longer
  declare `libc: ["glibc"]`, the launcher no longer detects musl, and
  `install.sh` no longer refuses Alpine. The release workflow checks that the
  Linux binary is static and runs the release archive, the installer and the
  npm packages in Alpine containers (amd64, and arm64 under QEMU) before it
  publishes anything; this replaces the "Alpine is not supported" limitation.

- **The npm packages move to the `@netivri` organization.** Install with
  `npm i -g @netivri/proxy` (was `netivri`); the six platform packages
  are `@netivri/proxy-{linux,darwin,w32}-{x64,arm64}` (`w32` is Windows, `os: win32`; npm refused
  the name `win32` as spam). The platform
  packages carry no keywords and a description that sends the reader to the
  main package, so they stay out of `npm search`. The release tarballs are
  named `netivri-proxy-*.tgz`. Nothing of this release was published under the
  old names.

- **A failed probe is confirmed before it counts.** The warm-up probe asks
  `www.gstatic.com`, and one slow or unreachable target made working nodes look
  down (on one setup real dials succeeded 100% while the probes gave the
  busiest exit 93.6% uptime, which moved its traffic to a fallback tier). A
  failed probe is now tried once more at once, against
  `cp.cloudflare.com/generate_204` and with twice the timeout; only a failure
  of both counts as a failed probe, makes a node count toward going down and
  shows in `stats` uptime. In `internal/core`, `WarmOptions` has `ConfirmProbe`,
  `ConfirmURL` and `ConfirmTimeout`; the angel turns it on.
- **The log says why a node went down and for how long.** A down line now
  carries the reason (`2 probe failures (timeout)`, `3 dial failures`) and the
  time since the node last answered, and a back line the downtime:
  `node own/a970e0d9fef1 (Finland 1, provider own, country FI) is down: 2 probe
  failures (timeout), last ok 3m12s ago`, `... is back after 41s down`. When 3
  or more nodes go down within 10 seconds (a lost network, a shared outage, the
  first probes after a start) one `WARN` line, `node burst: ...`, sums them up
  and the nodes of the burst are logged at `INFO`; `node burst over: ...`
  closes it. `netivri doctor` counts the summary with the node-down reports.
  The reason is a few words, never an address. In `internal/core`,
  `Options.OnNodeHealth` now takes a `core.NodeEvent` and `WarmOptions` has
  `OnProbeError`.
- **A node that never answers is not a burst.** A node that has not answered a
  single probe or dial since it was added (a dead entry of the subscription)
  is logged once as `INFO`, `node ... has never answered: ...`, and counts
  toward neither a `node burst` nor flapping. Before, the dead nodes of one
  subscription raised a misleading "likely a lost network" burst at every
  start. In `internal/core`, `NodeEvent` has `NeverOK`.
- **A flapping node is one warning.** A node that goes down 3 times within an
  hour gets one `WARN` (`node ... is flapping: 3 downs within the last hour
  (...)`); its further down and back lines are `INFO` until it has been quiet
  for an hour, and it does not count toward a burst. A probe error the log
  could not name now reads `error: ` and a short excerpt with every word that
  could be an address, host, port or credential replaced by `<addr>`, and
  the Windows wording of a timeout and of a reset is recognised.
- **A node that moves data is not marked down by its probes.** A busy node can
  answer a probe slowly: on a real setup the only first-tier node was marked
  down by its probes several times an hour (once for over 7 minutes) and the
  account's new connections went to a fallback country meanwhile. Now data coming back through a node on an open connection
  clears its failed probes and, for 60 seconds, a failed probe does not count
  toward the down rule; failed dials still do. A node that the probes alone
  declared down is back as soon as data flows through it.
- **One unreachable site no longer takes a node down.** Three failed dials in a
  row marked a node down even when they were all to the same dead destination
  (a browser opening three connections to it): a node that had answered a second
  earlier went down, a burst of four nodes was reported, and the sites bound to
  them moved to a fallback country. Failed dials now count toward the down rule
  only when they reached at least 2 different sites, or when 10 in a row went
  to one site (a dead node used for one site still fails over). The reason of such a down
  names the kind of the last error from a fixed list (`3 dial failures
  (timeout)`), never its text, and `refresh failed` of a subscription names
  the kind too (`request failed: timeout`).
- **Only first-tier nodes raise warnings.** `node ... is down` and `... is
  flapping` are `WARN` for the nodes of the first tier of some account's route
  and `INFO` for the nodes of later tiers, which no account uses while its first
  tier is up; the burst summary stays a `WARN`.
- **The log tells a client that left from a node that never answered.** The
  summary and the broken-connection lines counted every connection that
  ended without data from the node as one number; now `closed before any data`
  (the client sent nothing) is apart from `without an answer` (the client sent
  data, nothing came back). A node's back line is not repeated for the same
  server under a second provider: the twin's line says `same server as ...` and
  is `DEBUG` for fallback nodes.
- **The hourly summary says where the connections without an answer went.**
  How many different sites (counted by a hash, never named), the ports and how
  long they waited (median and longest), so one dead site that is retried from
  many connections can be told from a node that answers nothing.
- **The hourly summary carries the bytes** of the connections that ended
  (`412.0 MiB down, 38.1 MiB up`), so a count of connections without an
  answer can be checked against what really moved.
- **`doctor` and `account show` follow the routing state.** After a single
  failed probe `doctor` warned "no live node in tier 0" while the node was in
  use (the routing takes a node out after two failed probes and keeps it while
  data flows). The route check now counts the nodes the routing still uses;
  `account show` and `status --full` carry `down` next to `alive` (the last
  probe).
- **An hourly summary per account.** `account alice: last hour: 412
  connections ended, 0 broken, 3 closed without any data, 0 dials failed`, so
  that a log without a report of broken connections says "none".
- **The log reports what happens to connections.** It was silent about the
  things that break a user's connections. Once a minute, one line per account
  and node and kind, only when something went wrong: failed dials with the
  kind of the error (`3 dials failed (timeout x2, ...)`), connections that the
  node's side broke (`98 connections ended, 5 broken (connection reset x4,
  ...)`, plus how many closed without any data), sites moved to another node
  (`3 sites moved from {NODE} to {NODE}: node down`, `INFO` for a return to a
  better tier) and failed proxy logins (`4 proxy logins failed (alice x2,
  unknown logins x2)`, only configured accounts are named). No line holds a
  site, an address, an error text or a password. `WARN` from 3 failures. In
  `internal/core`, `Options.OnConnEvent` and `ConnEvent`; connections are
  wrapped only when it is set.
- **Fallback nodes no longer fill the log.** The repeated reports of a node no
  account uses (its downs and backs after its `flapping` line, the down of a
  twin of the same server) are written at the new `DEBUG` level, and `netivri
  logs` shows `info` and above unless `--level debug` is given. On a real setup
  four unused nodes wrote about 45 lines in 2.5 hours.
- **One server under two providers is one node in the log.** The same node id
  in two subscriptions (`own/a970e0d9fef1` and `main/a970e0d9fef1`) is one
  server: when its twin goes down within 10 seconds, the second line is `INFO`
  and ends with `, same server as {LABEL}`, and the burst and flapping counts
  treat the pair as one node, so the pair no longer doubles every warning. The
  same note ends the `has never answered` lines of such a pair.

- **The background process is called the angel.** Every place that said
  daemon now says angel: the `angel` field of `up`, `down` and `status`
  (was `daemon`), the error codes `ANGEL_NOT_RUNNING`,
  `ANGEL_START_FAILED`, `ANGEL_START_TIMEOUT` and `ANGEL_STOP_TIMEOUT` (were
  `DAEMON_*`), the `doctor` check `angel`, help and documents. Exit codes and
  the files in `run/` are unchanged, and log lines of older versions are still
  read. `schema_version` stays 1: 0.1.0 is the first release after the
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

### Security

- Subscription URLs and `--via` proxies are never printed or logged.
- The proxy binds to `127.0.0.1` unless told otherwise.
- Releases are gated on a dependency license check; the project is
  `GPL-3.0-only` (see [`docs/licenses.md`](docs/licenses.md)).

### Known limitations

- `use`, `node test` and `schema` are described in
  [`docs/cli.md`](docs/cli.md) but not implemented yet.
- Command results are documents only (Ktav by default, JSON with `--json`);
  there is no table view for people yet. Help (`netivri help`) is the one text
  output.
- No TUN mode and no change of the system proxy settings; these are non-goals
  for the 0.x line.
- `go install` does not work, because `go.mod` uses a `replace` directive for
  the vendored Mihomo. Use a release archive, the installers or npm.
