# Live reload and automatic subscription refresh

A running daemon keeps itself in step with its files. Two things feed it:

1. **Changes made to the files** by the other commands (`account add`,
   `sub add`, `sub update`, ...) or by hand (`config.ktav`). Before this, they
   took effect only after `netivri down` and `netivri up`.
2. **A timer** that re-fetches every subscription once per
   `fetch.update_interval` (one hour by default). A provider that retires or
   replaces a node no longer leaves an account on a dead route until someone
   restarts the daemon.

Both end in the same place: the daemon rebuilds what the core is made of and,
if it differs from what runs, replaces the core.

## Goals

- A change to accounts, passwords, nodes or the port is applied within a few
  seconds, without a command and without the user noticing the mechanism.
- Nothing is restarted for nothing: the daemon's own writes, unchanged node
  lists and cosmetic renames cost no connection.
- An unattended change can never leave the daemon worse off than it was: an
  invalid file or an empty route is refused and reported, and the running
  configuration stays.

## Non-goals

- **No hot swap.** The core takes its routes and accounts when it starts
  (`core.Start`; see the header of `internal/core/core.go`). Replacing a route
  means closing the listener and starting a new one on the same port, so the
  open connections end and the affinity counters and warm-up counters start
  from zero. The site bindings themselves are persisted in the statistics
  store and restored on the new core (see [node-stats.md](node-stats.md)). Swapping the members of a sticky group in place is a separate,
  larger change.
- No notification channel from the CLI to the daemon: see "Why polling".
- No per-subscription interval, no refresh at an exact wall-clock time.

## What counts as a change

`fingerprint` (`cmd/netivri/live.go`) is a SHA-256 over what a core is built
from: the bind address and port, `auth.login` and `auth.password`, every
account's login and password hash, the global `listen.allowed_clients` rule
and every account's own `allowed_clients` rule, and the nodes, labels and
routes of the account pool. Left out on purpose: the subscription URLs and `fetch.*` (the
core does not read them) and the display name of each node (the core renames
every node itself, and providers put changing text such as traffic left into
it).

The daemon compares the fingerprint of what runs with the fingerprint of the
files. Equal: nothing happens. That also covers the daemon's own write when a
client claims an `init` password: the in-memory configuration already holds
the claimed hash.

## Watching the files

Every 2 seconds the daemon takes a signature (size and modification time) of
`config.ktav` and of every `subscriptions/*.ktav` (and legacy `*.json`). A new signature has to be
seen twice in a row before it is applied, so a hand edit that is still being
written is not read half done. A reload also runs once when the watcher
starts, to catch what changed between loading and watching.

A reload does `loadConfig`, `mergeUp` (the same checks as `up`, including
"a non-loopback bind needs authentication") and `accountRoutePool`. If any of
them fails, the running core is untouched and the reason is kept in
`status` as `reload.last_error`; it is cleared when the files on disk match
what runs again. Explicit `--port` and `--bind` of `up` keep winning over the
file, as they do at start; a random port (`--port 0`) stays the port clients
know.

### Why polling

Polling needs no change in the CLI, adds nothing to the output of the other
commands, and also covers hand edits of `config.ktav`, which the documentation
asks for in places. The cost is a latency of at most about 4 seconds and a
stat of a handful of files every 2 seconds. A `POST /v1/reload` on the control
channel, called by the commands after they write, would be immediate and could
report the outcome to the caller; it can be added later without changing the
rest.

## Restarting the core

1. A new authenticator is built from the new configuration.
2. The statistics store is opened if routes appeared and there was none, and
   told about the new nodes (`SetNodes` adds them; old history stays).
3. The old instance's site bindings are written to the store (a last snapshot
   while it still runs) and it is closed (this ends its connections); a new
   one starts on the same port with the stored bindings as
   `Options.InitialBindings`, so a site keeps its exit node across the reload
   (see [node-stats.md](node-stats.md)). A port that is still being released
   is retried for about a second.
4. If the new core does not start, the previous options start again. If that
   fails too, the daemon stops: a daemon without a listener would only look
   alive.

Writers of the configuration file now merge instead of overwrite. A client
that claims an `init` password makes the daemon write the hash; it does so into
the copy on disk, not from its own possibly stale in-memory copy, so an
`account add` made a moment earlier is not lost. If the file no longer holds
the placeholder that was claimed, the claim is refused and the client retries
against the newly applied configuration.

## Subscription refresh

One loop wakes every 30 seconds and asks which subscription is due:

- A subscription seen for the first time is due one interval after the
  `fetched_at` of its stored snapshot, or at once when it has none, but never
  earlier than 30 seconds after the daemon started. A snapshot that was already
  stale at start is therefore refreshed half a minute later.
- After a success the next refresh is one interval away; after a failure it is
  10 minutes away (or the interval, when that is shorter).
- The subscription list and the interval come from the latest valid
  configuration, so `sub add` and `sub rm` are followed without a restart, and
  a shortened interval applies at once.

A refresh uses the same fetch as `sub update` (`fetchSnapshot`: the same
proxy rules, never silently direct, never printing the URL). It stores the new
snapshot only if the routes can still be built with it
(`accountRoutePoolWith` replaces the stored snapshot of that provider by the
fetched one without touching the disk). A list in which an account's country
disappeared is rejected, logged and shown as `refresh.last_error`; the old
list stays and the next attempt comes after the retry wait. A stored snapshot
is then applied by the file watcher like any other change.

## Failure modes

| Situation | What happens |
| --- | --- |
| `config.ktav` does not parse or validate | running core stays, `reload.last_error` |
| a route has no nodes after a change | running core stays, `reload.last_error` |
| a refresh fails (network, HTTP status, parse) | old snapshot stays, `refresh.last_error`, retry in 10 minutes |
| a refresh would empty a route | refresh rejected, old snapshot stays, `refresh.last_error` |
| the new core does not start | previous core is started again |
| neither core starts | the daemon stops |
| `fetch.update_interval: off` | no refresh; files are still watched |

## Observability

`status` carries `refresh` (`interval_seconds`, `ok`, `failed`, `last_at`,
`last_error`) and `reload` (`count`, `last_at`, `last_error`); see
`docs/cli.md`. The daemon log (`run/netivri.log`) gets one line per refresh
outcome and per applied or refused configuration, never a URL or a credential.

## Verification

Unit tests start the daemon logic on a fake core (`coreStart` is a variable,
the core is the small `coreInstance` interface) and cover: the fingerprint
(what changes it and what must not), the file signature, restart only on a
real change, a random port that stays, the rollback to the previous core, the
daemon stopping when no core starts, a refused file or empty route keeping the
core, the statistics store opened late, the port retry, the watcher loop, the
refresh loop (due, off, failure and retry wait, stop with its context), the
refresh schedule, the rejection of a refresh that would empty a route, the
merge of a claimed password into the file, and the `status` output.
End-to-end tests start a real daemon (`up --foreground`), add and remove an
account with the CLI, replace a stored snapshot, and break and repair
`config.ktav`, then check the routing through fake upstream nodes and the
`status` counters, including that a password claim or a node rename does not
restart the listener and that a removed account's open connection ends. All of
it runs in CI only.

Not covered: the detached daemon process (the e2e tests use the foreground
one, which runs the same code), a change of `listen.port` or `listen.bind` in
the file against a real process (an explicit `--port` wins over the file; the
fingerprint is unit-tested), and the race detector.
