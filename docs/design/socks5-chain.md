# Proxies before and after the VPN

Two optional proxies wrap the VPN node of a route (`socks5://`, `http://` for an
HTTP CONNECT proxy, or `https://` for one reached over TLS; the settings are
still called `pre_socks5` and `post_socks5`):

- `pre_socks5` is the proxy netivri uses to **reach** the VPN node.
- `post_socks5` is the proxy the traffic is handed to **after** the VPN node;
  it connects to the destination.

```text
client -> netivri -> [pre] -> VPN node -> [post] -> destination
```

Both are set globally (`chain.pre_socks5`, `chain.post_socks5` in
`config.ktav`) and per account (`pre_socks5`, `post_socks5`). User-facing
description: [../config.md](../config.md), "SOCKS5 chain"; commands:
[../cli.md](../cli.md), `account set-socks5`.

## Goals

- Reach a VPN node that is only reachable through a proxy (pre), and let the
  traffic leave through a proxy chosen by the user instead of the VPN's exit
  (post).
- Global defaults with a per-account override, side by side, one value each.
- Credentials never leave the process: not in output, logs, errors, route
  keys, statistics labels or proxy names.
- A running angel applies a change without a command, and keeps open
  connections whenever it can.

## Non-goals

- More than one proxy per side, other schemes (`socks5h://`, HTTP), an
  authentication method other than user and password.
- A pre without a VPN node: an account without a route (no provider and no
  exit country) has nothing to reach through a pre, so its pre is ignored.
- Separate health and statistics per chain: they stay per node (see
  "Limitations").

## Configuration and cascade

A value is `{scheme}://[user:password@]host:port`, or `none`. The scheme is
`socks5`, `http` (an HTTP CONNECT proxy) or `https` (the same over TLS, with the
certificate checked), any case, and is rendered as a Mihomo `socks5` or `http`
outbound (`tls: true` for `https`); the setting names keep `socks5`. The host a name, an IPv4 address or `[IPv6]`, the port is
**required** (1..65535), the user name and password are percent-encoded, no
path, query or fragment. Validation (`internal/config`) fails with
`CONFIG_INVALID` naming the key (`chain.pre_socks5`, `accounts[N].post_socks5`)
and never echoing the value.

The cascade is resolved per side and independently:

| Account value | Effective |
| --- | --- |
| absent or empty | the global value (source `global`), or no proxy |
| a URL | that URL (source `account`) |
| `none` | no proxy (source `none`), whatever the global value is |

`none` as a global value is the same as no value. `account set-socks5 ...
inherit` deletes the account key. The shared resolver in
`cmd/netivri/accounts_pool.go` (`resolveChain`) is used by `account list`,
`account show` and `status`, so they agree.

## How the chain is built (Mihomo)

netivri builds one Mihomo proxy per element and links them with Mihomo's
`dialer-proxy` option: a proxy with `dialer-proxy: X` makes its own connection
to its server through the proxy `X`. For a route member (`internal/core/accounts.go`):

```text
pre helper   socks5 to the pre proxy                    (no dialer)
node         the VPN node, dialer-proxy = pre helper    (only with a pre)
post member  socks5 to the post proxy, dialer-proxy = node   (only with a post)
```

The member the sticky group dials is the outermost one: the post member when
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
  proxy (`netivri-global-post`, behind the default node when there is one),
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
| new nodes, a new `pre_socks5` or `post_socks5` of an account that has a route, a `chain.pre_socks5` change | in place (`core.Instance.Reconfigure`): the listener stays, open connections go on, health, probe schedule and site bindings of nodes that stay are kept |
| `chain.post_socks5` changed | restart of the listener on the same port, open connections end |
| a route appears or disappears entirely (the first account gets a provider or country, the last one loses it) | restart |
| an account is added or removed, a password or `allowed_clients` changes, bind or port changes | restart (as before) |

Reconfigure refuses an instance that was started without account routes or
with a default node (`ErrReconfigureUnsupported`); the global post is built
into the default route when the core starts, which is why a change of it
restarts the core instead of replacing it in place. If the restart fails, the
previous configuration is started again. See [live-reload.md](live-reload.md).

## Secrets

The URLs are credentials like subscription URLs. Output shows
`socks5://host:port` and a boolean `auth`; the user name is dropped too.
`status` reports the chain of the **running core**, not of the file on disk.
Errors name the key or the flag, never the value. The tests scan the whole
output of the commands for the credentials they feed in.

## Limitations

- Health, probes and statistics are per node label. Two accounts that use the
  same node through different proxies share its health: a dead post proxy
  of one account can mark the node down for the other.
- A pre has no effect for an account without a route.
- A change of the global post restarts the listener.

## Tests

Unit: URL validation and cascade (`internal/config`), the route pool and route
keys, `account` commands and their redaction, status of the applied state, the
core mapping of pre, post, both and direct, the helper-name classifier and
probe selection, the restart policy of the angel on a fake core. Integration:
fake pre and post SOCKS5 servers record the path of real connections, an
account with `none` bypasses the global proxies, a direct account goes through
the post, and an open connection survives a `Reconfigure` that adds a chain.
End to end: `account set-socks5 --post none` and `--post {URL}` on a running
angel.
