# Direct exit per account (design, not implemented)

Question: a server (or the plain proxy port) can hand a client's traffic to the
VPN node route of its account, or let it leave from this machine directly. Where
does that choice live?

## What exists today

An account is routed by its `provider` and `exit_country`, plus the optional
`pre_socks5` and `post_socks5` hops ([accounts.md](accounts.md)). A server only
authenticates a client as one of its accounts; the connection then follows that
account's route (the core rule is `IN-USER,{login},{route group}`), so a server
has no routing of its own. The core can already build three exits for an
account:

| Account | Leaves through |
| --- | --- |
| a provider and/or a country with nodes | the VPN node route (fail closed when it has no node) |
| no node, `post_socks5` set | directly to the post proxy (`directPost`) |
| no node, `pre_socks5` and `post_socks5` both `none` | plain `DIRECT` (`directRoute`) |

The last two are reachable only by writing the chain keys in a specific way, and
an account with neither route nor chain silently takes the default route (the
selected node, or `DIRECT` while none is selected). Nothing says "direct" out
loud, so it is easy to land on it by accident or to miss it.

## Recommendation: bind it to the account, as one explicit field

`accounts[].exit` with two values:

- `route` (default, empty): as today, the route of `provider` and
  `exit_country`, fail closed.
- `direct`: the account leaves from this machine. `provider` and `exit_country`
  must be empty (validation error otherwise); `post_socks5` may be set and then
  the traffic goes to that proxy directly (the existing `directPost`); `pre_socks5`
  is meaningless without a node and is rejected.

Why the account and not the server:

- The account already owns the route and a server has no route of its own: a
  per-server flag would need a precedence rule against the account's route.
- A multi-account server (shadowsocks, vmess, trojan, hysteria2, ...) must be able
  to serve `alice` through DE and `bob` directly on one port; a server-wide flag
  cannot say that. Single-user protocols already have one server per account, so
  the account flag covers them as well.
- The client filter (`allowed_clients`), statistics and secrets are per account
  already; the flag sits next to them.
- A server stays a pure listener; `server add` does not change.

Why not a separate `exits:` object now: it adds a second table, references and
lifecycle for a choice of two values. If shared profiles are wanted later, an
`exit: {NAME}` reference can be added without breaking `route`/`direct`.

## Behavior

- Fail closed is unchanged: an account in `route` mode without nodes is still
  refused (`NO_NODES_FOR_COUNTRY`). `direct` is an explicit request, never a
  fallback; a dying node never turns a `route` account into a direct one.
- A `direct` account has no node health: it does not count toward probes,
  tiers or stats by node; its traffic is recorded with exit `direct`.
- Security: a direct exit shows this machine's address to every destination and
  ignores the VPN. The CLI says so on `account add --exit direct` and
  `account set-exit ... direct`, `account show`, `status` and `doctor` mark the
  account (`exit: direct`), and a direct account on a non-loopback server is a
  `doctor` warning, not an error.
- Applying a change is a route change: the angel applies it in place without
  ending open connections, like `set-country`.

## CLI and config

```
netivri account add {ACCOUNT} [--exit {route|direct}] ...
netivri account set-exit {ACCOUNT}[,{ACCOUNT}...] {route|direct} | --all {route|direct}
```

`config.ktav`: `accounts[].exit: direct`. Help topics, `docs/cli.md`,
`docs/config.md`, `docs/servers/README.md` (a section: which exit a server
client gets) and the CHANGELOG follow the code.

## Tests

Unit: config validation (`direct` with a provider or country, with `pre_socks5`,
with `post_socks5`), route construction (`IN-USER,{login},DIRECT` and the post
proxy variant), `set-exit` fingerprints and in-place reload. Integration: two
accounts on one server, one `route` and one `direct`, each reaching a loopback
backend through its own exit. e2e: real binaries, angel B as the client, the
backend sees the direct account from A's address and the route account from the
node recorder.

## Non-goals

A per-server or per-protocol override, named exit profiles, load balancing
between direct and node routes, automatic fallback in either direction.
