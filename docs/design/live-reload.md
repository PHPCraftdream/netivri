# Live reload and automatic subscription refresh

A running angel keeps itself in step with its files. Two things feed it:

1. **Changes made to the files** by the other commands (`account add`,
   `sub add`, `sub update`, ...) or by hand (`config.ktav`). Before this, they
   took effect only after `netivri down` and `netivri up`.
2. **A timer** that re-fetches every subscription once per
   `fetch.update_interval` (one hour by default). A provider that retires or
   replaces a node no longer leaves an account on a dead route until someone
   restarts the angel.

Both end in the same place: the angel rebuilds what the core is made of and,
if it differs from what runs, applies it. New nodes and routes go into the
running core, which keeps its listener and its open connections; a change of
the listener or the accounts replaces the core.

## Goals

- A change to accounts, passwords, nodes or the port is applied within a few
  seconds, without a command and without the user noticing the mechanism.
- Nothing is restarted for nothing: the angel's own writes, unchanged node
  lists and cosmetic renames cost no connection.
- An unattended change can never leave the angel worse off than it was: an
  invalid file or an empty route is refused and reported, and the running
  configuration stays.

## Non-goals

- **No hot swap of the listener.** The address, the allowed clients and the
  credentials are taken when the core starts (`core.Start`). Changing one of
  them closes the listener and starts a new one on the same port, so the open
  connections end.
- No notification channel from the CLI to the angel: see "Why polling".
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

The fingerprint has two halves: `listenerFingerprint` (address, allowed
clients, credentials) and `routesFingerprint` (nodes, labels, tiers, routes).
The angel compares the fingerprint of what runs with the fingerprint of the
files. Equal: nothing happens. Only the routes half differs: the change is
applied in place. The listener half differs: the core is restarted. That also covers the angel's own write when a
client claims an `init` password: the in-memory configuration already holds
the claimed hash.

## Watching the files

Every 2 seconds the angel takes a signature (size and modification time) of
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

## Applying routes in place

A subscription refresh that brings a changed node list, or a changed
`exit_country` or `provider` of an account, only touches the routes half. The
angel tells the statistics store about the nodes and calls
`Reconfigure` on the running core (`internal/core/reconfigure.go`):

1. The new member proxies are parsed and the new rules built; anything that
   fails here leaves the core as it was.
2. The probes of the old routes stop. The health of every node label that
   stays (measurements, down state, hedge budget) and its probe schedule are
   copied to the new routes; nothing is announced for it, so a node that was
   down is not logged as down a second time and keeps its lengthened probe
   interval, and a node that is up is not probed again before its interval
   is up. The site bindings are moved by label to the node's new
   place in the route; a binding to a node that left the route is dropped,
   and so are all bindings of an account whose country or provider lists
   changed (its route key is new), so its sites follow the new preference.
   The affinity counters continue.
3. The new proxies and sticky groups get names of their own generation
   (`netivri-g{N}-r{R}-{I}`, `netivri-g{N}-group-{R}`) and are added to
   Mihomo's proxy map, then the new rules replace the old ones. Mihomo's
   configuration is not re-applied and the listener is not touched. The
   previous generation stays in the map until the next change, so a connection
   that matched the old rules a moment before the switch still finds its
   group.
4. Open connections finish on the node they use; `status` keeps showing them
   under that node, and their traffic still counts for it.

The core refuses a change it cannot take in place (an instance that was
started without account routes, or routing through a single default node)
with `core.ErrReconfigureUnsupported`, and the angel restarts it instead. Any
other `Reconfigure` error is logged (`routes not updated in place, restarting
the listener: ...`) and also ends in a restart.

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
   fails too, the angel stops: an angel without a listener would only look
   alive.

Writers of the configuration file now merge instead of overwrite. A client
that claims an `init` password makes the angel write the hash; it does so into
the copy on disk, not from its own possibly stale in-memory copy, so an
`account add` made a moment earlier is not lost. If the file no longer holds
the placeholder that was claimed, the claim is refused and the client retries
against the newly applied configuration.

## Subscription refresh

One loop wakes every 30 seconds and asks which subscription is due:

- A subscription seen for the first time is due one interval after the
  `fetched_at` of its stored snapshot, or at once when it has none, but never
  earlier than 30 seconds after the angel started. A snapshot that was already
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
| the core cannot take routes in place | the core is restarted |
| the new core does not start | previous core is started again |
| neither core starts | the angel stops |
| `fetch.update_interval: off` | no refresh; files are still watched |

## Observability

`status` carries `refresh` (`interval_seconds`, `ok`, `failed`, `last_at`,
`last_error`) and `reload` (`count`, `last_at`, `last_error`); see
`docs/cli.md`. The angel log (`run/netivri.log`) gets one line per refresh
outcome and per applied or refused configuration, never a URL or a credential.

## Verification

Unit tests start the angel logic on a fake core (`coreStart` is a variable,
the core is the small `coreInstance` interface) and cover: the fingerprint
(what changes it and what must not), the file signature, restart only on a
real change, routes applied in place without a restart and the restart when
that fails, a random port that stays, the rollback to the previous core, the
angel stopping when no core starts, a refused file or empty route keeping the
core, the statistics store opened late, the port retry, the watcher loop, the
refresh loop (due, off, failure and retry wait, stop with its context), the
refresh schedule, the rejection of a refresh that would empty a route, the
merge of a claimed password into the file, and the `status` output. Core
tests cover `Reconfigure`: an open connection that survives two changes and
keeps its node label, bindings and health carried by label, a down node that
stays down without a second report, and the changes it refuses.
End-to-end tests start a real angel (`up --foreground`), add and remove an
account with the CLI, replace a stored snapshot, and break and repair
`config.ktav`, then check the routing through fake upstream nodes and the
`status` counters, including that a password claim or a node rename does not
restart the listener and that a removed account's open connection ends. All of
it runs in CI only.

Not covered: the detached angel process (the e2e tests use the foreground
one, which runs the same code), a change of `listen.port` or `listen.bind` in
the file against a real process (an explicit `--port` wins over the file; the
fingerprint is unit-tested), and the race detector.
