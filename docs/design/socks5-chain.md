# Proxies before and after the VPN

Ordered proxy alternatives wrap the VPN node of a route (`socks5://`,
`http://` for an HTTP CONNECT proxy, or `https://` for one reached over TLS;
the settings are still called `pre_socks5` and `post_socks5`):

- `pre_socks5` is the proxy netivri uses to **reach** the VPN node.
- `post_socks5` is the proxy the traffic is handed to **after** the VPN node;
  that proxy connects to the destination.

```text
client -> netivri -> [selected pre proxy, if any] -> VPN node -> [selected post proxy, if any] -> destination
```

Both are set globally (`chain.pre_socks5`, `chain.post_socks5` in
`config.ktav`), per provider (`subscriptions[].pre_socks5` / `post_socks5`)
and per account (`accounts[].pre_socks5` / `post_socks5`). User-facing
description: [../config.md](../config.md), "SOCKS5 chain"; commands:
[../cli.md](../cli.md), `account set-socks5`.

## Goals

- Reach a VPN node that is only reachable through a proxy (pre), and let the
  traffic leave through a proxy chosen by the user instead of the VPN's exit
  (post).
- Global defaults with full nearest-level replacement per provider and account.
- Credentials never leave the process: not in output, logs, errors, route
  keys, statistics labels or proxy names.
- A running angel applies a change without a command, and keeps open
  connections whenever it can.

## Non-goals

- More than one proxy per side in the production core is supported by candidate
  pools; single-hop routes retain the legacy scalar path. Candidates are not
  serial hops. Other schemes (`socks5h://`) and authentication methods beyond
  user and password are out of scope.
- A pre without a VPN node: an account without a route (no provider and no
  exit country) has nothing to reach through a pre, so its pre is ignored.
- Separate health and statistics per chain: they stay per node (see
  "Limitations").

## Configuration and cascade

Each list entry is `{scheme}://[user:password@]host:port`, or the sole entry
`none`. Legacy scalar Ktav URLs load as singleton lists, and singleton lists
render back as scalar values. The scheme is `socks5`, `http` (HTTP CONNECT)
or `https` (HTTP CONNECT over TLS, certificate checked); the setting names keep
`socks5`. Host: name, IPv4 or `[IPv6]`; port: required, 1..65535. User name and
password are percent-encoded. No path, query or fragment. Validation
(`internal/config`) fails with `CONFIG_INVALID` naming the key
(`chain.pre_socks5`, `accounts[N].post_socks5`) and never echoing the value.

The cascade resolves each side independently, nearest level first: account,
then the node's provider, then global. A non-empty list replaces the complete
less-specific value; levels are never merged. Empty or absent inherits;
`[none]` explicitly disables. Up to 16 URLs per side are accepted; entries are
alternatives, not serial hops. A global post list of several hops also serves the
no-account `MATCH` path through the global post pool (`GlobalPostPool`); a
one-hop list keeps the scalar `GlobalPost` mapping. Account routes can inherit
global/provider/account pools; no `GlobalPre` field is defined, the default
route has no pre side.

| Account list | Provider list | Global list | Effective |
| --- | --- | --- | --- |
| `[URL]` | anything | anything | account value (`account`) |
| `[none]` | anything | anything | no proxy (`none`) |
| empty/absent | `[URL]` | anything | provider value (`provider`) |
| empty/absent | `[none]` | anything | no proxy (`none`) |
| empty/absent | empty/absent | `[URL]` | global value (`global`) |

`none` as a global value explicitly means no proxy. The replacement flags are
repeatable. `account set-socks5 ... inherit` clears the account list;
`sub set-socks5 ... inherit` clears the provider list. Both setters also have repeatable `--add-pre {URL}` / `--add-post {URL}`
and `--remove-pre {INDEX}` / `--remove-post {INDEX}` operations. Indexes are
one-based positions in the original list. Repeating an original index rejects
the entire operation without saving the list. Replace, add and remove operations
cannot be combined on the same side; removing the last scoped entry restores
inheritance. `none` and `inherit` cannot be repeated or combined with other
entries on their side. The Accounts and Providers shell screens both support
list replacement, add, remove, inherit and none through the existing picker;
list order is not changed. The shared resolver is used by
`account list`, `account show` and `status`.

## How the chain is built (Mihomo)

Single-candidate routes retain the legacy Mihomo `dialer-proxy` mapping below;
account-route pools use native core pool adapters. A pre adapter carries the
VPN-node dial through a selected pre candidate. A post candidate dials through
the selected node; the post adapter selects and attributes the hop. Direct
accounts use a direct carrier. This requires no helper process or static
pre × node × post matrix. The no-account `MATCH` route uses a post pool for a
global list of several hops and the scalar `GlobalPost` for one hop. See
[hop-pools.md](hop-pools.md) for health and persistence.

```text
pre helper   socks5 to the pre proxy                    (no dialer)
node         the VPN node, dialer-proxy = pre helper    (only with a pre)
post member  socks5 to the post proxy, dialer-proxy = node   (only with a post)
```

The member the sticky group dials is the outermost one: the post proxy when
the route has a post, else the node. A connection to it therefore goes
netivri -> pre -> node -> post -> destination.

- **Direct accounts.** An account without a route and with an effective post
  gets a route whose only hop is a post member with `dialer-proxy: DIRECT`: no
  VPN node is looked up and no subscription is read. An account without a
  route whose effective post is `none` while a global post exists gets an
  explicit plain DIRECT route; without it its authenticated traffic would
  fall to `MATCH` and use the global post, which contradicts `none`.
- **`MATCH`.** Unauthenticated traffic and accounts without own settings that
  have no route use `MATCH`; with a global post the rule points at a post
  proxy (`netivri-global-post`, behind the default node when there is one; for a
  list of several hops the pool adapter of that name, without a default node),
  else at DIRECT or the default node as before.
- **Names.** Helpers are named from the member name of their generation
  (`netivri-r{R}-{I}` with `-pre` / `-post`, `netivri-g{N}-r{R}-{I}-post`, ...,
  see [live-reload.md](live-reload.md) for generations); the names are
  classified in one place (`generatedMemberName`, `isRoutePostName`,
  `isChainHelperName`) and used the same way by probing, activity and the
  throughput poller. No name contains a credential.
- **Route key.** Accounts with different effective chains must not share a
  route, so `routeKey` gets a short SHA-256 digest of the canonical
  provider list, country list, pre and post (`credentialRouteKey`). The digest
  hides the credentials; the key does not contain them. Node labels stay
  `{PROVIDER}/{NODE_ID}`.

## Probes and hedging

The background probe of a node (`internal/core/warm.go`) runs once per route
member, on the outermost proxy of the chain, so the result measures the whole
path including the pre and post proxies. The helper proxies (the pre helper
and the bare node behind a post) are never probed on their own
(`warmLeaves`/`generatedMemberName`). Traffic is attributed to a node label by
the member in the connection's chain, so a chained connection is counted for
the right node. A failing pre or post proxy fails the probe, so the node
goes down in the health table like a dead node, and the sticky group fails
over inside the account's route. Hedging (`hedge.go`) decides by the adapter
type of the member: a post member is a SOCKS5 adapter and is treated like any
other member of that type.

Without account routes the probe of the default route measures the global
post (behind the default node, if any) when there is one, because that is
where `MATCH` traffic leaves.

## Live reload

The chain settings are part of what the angel builds the core from
(`routesFingerprint` in `cmd/netivri/live.go`), so a change is seen by the
file watcher like any other. How it is applied:

| Change | Applied |
| --- | --- |
| new nodes, a new account pre/post list for a routed account or a `chain.pre_socks5` list change | in place (`core.Instance.Reconfigure`): the listener stays, open connections go on, health, probe schedule and site bindings of nodes that stay are kept |
| `chain.post_socks5` changed | in place while the core routes in rule mode (account routes or a global post) before and after; otherwise (the first global post of a core without routes, removing the last) restart of the listener on the same port, open connections end |
| a route appears or disappears entirely (the first account gets a provider or country, the last one loses it) | restart |
| an account is added or removed, a password or `allowed_clients` changes, bind or port changes | restart (as before) |

Reconfigure refuses an instance that runs in direct mode (no account routes and
no global post) or has a default node (`ErrReconfigureUnsupported`), and new
options that would need a node-backed route on an instance without one. An
instance in rule mode without node-backed routes (direct routes, direct-post
pools, the global post pool) is replaced in place without a node table. If the
restart fails, the previous configuration is started again. See
[live-reload.md](live-reload.md).

## Secrets

The URLs are credentials like subscription URLs. Output shows
`socks5://host:port` arrays and a boolean `auth`; the user name is dropped too.
Account JSON carries effective `value` and configured `own` arrays, while
`source` and `auth` metadata remain. The JSON array shape is an intentional
breaking change in the unreleased contract; `schema_version` is intentionally
not bumped. `status` reports the chain of the **running core**, not of the file on disk.
Errors name the key or the flag, never the value. The tests scan the whole
output of the commands for the credentials they feed in.

## Limitations

- Health, probes and statistics are per node label. Two accounts that use the
  same node through different proxies share its health: a dead post proxy
  of one account can mark the node down for the other.
- A pre has no effect for an account without a route.
- A default node together with a global post of several hops is refused.

## Tests

The JSON account-chain shape intentionally changes from scalar values to redacted arrays in the unreleased contract; `schema_version` is not bumped.

Unit: URL validation and cascade (`internal/config`), the route pool and route
keys, `account` commands and their redaction, status of the applied state, the
core mapping of pre, post, both and direct, the helper-name classifier and
probe selection, the restart policy of the angel on a fake core. Integration:
fake pre and post SOCKS5 servers record the path of real connections, an
account with `none` bypasses the global proxies, a direct account goes through
the post, and an open connection survives a `Reconfigure` that adds a chain.
End to end: `account set-socks5 --post none` and `--post {URL}` on a running
angel.
