# Direct exit per account (implemented)

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

The last two were reachable only by writing the chain keys in a specific way; an
account with neither route nor chain silently took the default route (the
selected node, or `DIRECT` while none was selected). Nothing said "direct" out
loud, so it was easy to land on it by accident or to miss it.

## Recommendation: bind it to the account, as one explicit field

`accounts[].exit` with two values:

- `route` (default, empty): as today, the route of `provider` and
  `exit_country`, fail closed.
- `direct`: the account leaves from this machine. `provider` and `exit_country`
  must be empty (validation error otherwise); `post_socks5` may be set and then
  the traffic goes to that proxy directly (the existing `directPost`); an explicitly
  configured `pre_socks5` is rejected (`none` is allowed), an inherited global
  pre proxy is ignored, because a direct account has no node to put it before. `account show` reports `pre_socks5` as value `none`, source `direct`,
  auth `false`, own `null`; `tiers` is empty for direct accounts.

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
- **Security:** a direct exit shows this machine's address to each destination and bypasses VPN nodes. The CLI warns on `account add --exit direct` and `account set-exit ... direct`; `account show`, `status` and `doctor` report the mode. `doctor` warns, but does not fail, when a direct account uses a non-loopback server bind.
- Applying an account route-to-direct or direct-to-route change in place keeps
  open connections only when at least one account route has stored nodes both
  before and after. If the first node-backed route appears or the last
  disappears, including the first explicit direct route on a default-only
  instance, the listener restarts. Direct-only changes and changes after the
  last node-backed route restart. Keeping another account's node-backed route
  through a flip preserves connections. Adding an account always restarts, as
  with `account add`; defaults, default-node and global-post changes retain
  their existing restart behavior.
  Clone inherits the source exit unless `--exit` overrides it.

For server updates with explicit-direct-only accounts, the listener restarts if the core reports that the update is unsupported in place; other server transaction errors leave the current configuration running and appear under `status.reload`.

## CLI and config

```text
netivri account add {ACCOUNT} [--exit {route|direct}] ...
netivri account clone {SOURCE} {ACCOUNT} [--exit {route|direct}] ...
netivri account set-exit {ACCOUNT}[,{ACCOUNT}...] {route|direct}
netivri account set-exit --all {route|direct}
```

`config.ktav` stores `accounts[].exit: direct`; empty/absent means `route`.
`set-exit` has no `--none` and does not clear provider/country implicitly.

## Tests

Config validation, route construction, set-exit fingerprints and in-place route
updates are covered; tests run only in CI.

## Non-goals

A per-server or per-protocol override, named exit profiles, load balancing
between direct and node routes, automatic fallback in either direction.
