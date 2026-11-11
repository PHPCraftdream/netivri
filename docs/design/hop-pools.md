# Pools of pre and post hops

Today an account route has at most one `pre_socks5` hop (before the node) and
one `post_socks5` hop (after it). This note fixes how both become **lists**
(pools): the hops of a pool are sorted by health, dead ones wait in a separate
list until a probe sees them alive, and a site keeps leaving through the same
post hop. The model follows `resocks5` (sand rating, quarantine with doubling
backoff, one leased probe per due hop); the places where netivri differs are
named below.

## Decisions

- **Configuration:** each of the six fields (global `chain`, provider,
  account; pre and post) is an ordered list of hop URLs. A scalar reads as a
  one-element list; one element is written back as a scalar. The nearest level
  that sets a list replaces the lower ones entirely (no merging); `none`
  stays an explicit empty chain.
- **Order of choice:** as in `resocks5`. Every hop has *sand*: +1 per soft
  failure (cap 8), half-life 30 s, a success halves it. The weight of a hop is
  `exp(-k * sand)` (floor 0.05) and the choice is weighted random: a failing
  hop is drawn less often, never excluded by the weights alone.
- **Quarantine:** 3 hard failures in a row take a hop out of the draw for 30 s,
  doubling on every re-trip up to 600 s. After the backoff the hop is on
  probation: one probe at a time (a lease of 25 s) tests it; a success returns
  it to the common list, a failure re-trips it with the doubled period. While
  at least one hop is healthy, quarantined and probing hops are not drawn;
  when none is healthy the quarantine is ignored, so a pool never runs dry.
- **Dead hops are persisted** in a separate state file and are probed at once
  after a restart (task Pool 5).

## How a pool plugs into Mihomo (the spike)

Mihomo resolves `dialer-proxy` **by name at every dial**
(`third_party/mihomo/component/proxydialer/byname.go`: `tunnel.Proxies()[name]`,
any `C.Proxy`). netivri already registers its own adapters under group names
(`installStickyGroups` replaces the select group of a route with a
`stickyGroup` through `tunnel.UpdateProxies`). So a pool is one more custom
adapter, and **no Mihomo patch is needed**:

- **Pre pool.** One adapter `pre-pool` per route (or per distinct pool). The
  node entries get `dialer-proxy: <pre-pool name>`. Mihomo calls the adapter's
  `DialContext` with an inner metadata whose address is the node's
  `host:port`; the adapter draws a live pre hop, dials the node address
  through it (the pre hop is a plain `socks5`/`http` outbound entry) and
  reports the outcome to that hop's health. The site is not visible to a pre
  hop, so nothing is bound.
- **Post pool.** A post hop must carry its traffic through the node that the
  sticky group chose, so an entry exists per pair: for node *i* and post hop
  *k* a small outbound entry with `dialer-proxy: node-i` (N x K cheap config
  objects; 37 nodes and 3 posts are 111 entries, built once per generation).
  The member of the sticky group for node *i* is a `post-pool-i` adapter: its
  `DialContext` receives the connection's metadata (login, host), picks the
  hop *k* (bound for this login and site if healthy, otherwise drawn) and dials
  through entry `(i, k)`. Health is kept per hop *k*, not per entry, so the
  verdict on a post hop is shared by all nodes.
- **Hot reload** keeps working: adapters are swapped in `tunnel.Proxies()`
  with the groups; health is carried by hop identity (a hash of the URL
  without userinfo), not by entry name.

The alternative, building every `pre x node x post` combination statically and
letting `stickyGroup` choose among them, was rejected: hundreds of members,
health per combination instead of per hop, and no place for the per-hop
quarantine.

## Blaming the right hop

`hopSet.attribute` guesses the failing stage from the error text and can answer
`unknown hop`. `resocks5` instead knows the stage (`Connect`, `Hop(i)`,
`Target`). The pool adapters know it too, because they dial one hop at a time,
so the rule is:

| Failure | Counts against |
| --- | --- |
| the hop itself cannot be reached (connect, timeout, reset before its handshake) | that hop, **hard** |
| the hop says `host unreachable` for the next address (SOCKS5 REP 3-6, HTTP 502/503/504) | the **next** hop, hard |
| ambiguous handshake failure between two hops | both neighbours, **soft** |
| auth or policy refusal (407, 403, SOCKS5 REP 2) | inconclusive for the next hop; soft for the refusing hop |
| the failure cannot be put on a stage (`unknown hop`) | **nobody** |

A post hop is probed only through a live node, and its failure counts only when
the carrying node answered a probe a moment ago; otherwise the verdict would
land on the wrong hop. Capacity refusals of our own limits never count.

## Differences from resocks5

- **The site binding is to the post hop**, keyed by the second-level domain
  (`siteKey`, public-suffix aware), not to the whole chain keyed by
  `host:port`. The site sees only the post hop's IP, so while the bound post is
  healthy the site keeps it even when the node or the pre hop below it changes.
  A bound post that is quarantined is replaced without spending an attempt, and
  the binding does not flap back before its TTL (task Pool 4).
- **No bypass fallback.** `resocks5` falls back to the ordinary exits around
  the gates. An account with a post pool never leaves without a post hop (the
  site would see the node's IP): when every post is quarantined the quarantined
  ones are tried, the draw is never empty, and a failure is reported instead of
  a bypass.
- Tiers, node health, the hedged dials and the per-account affinity of
  `stickyGroup` stay as they are; the pools add one level under them.

## Task order

1. Configuration: lists and the cascade, limited to one hop until the pool
   lands (a list of several hops is rejected, never ignored silently).
2. Per-hop health model (sand, quarantine, leased probes, blame rule).
3. Route building and the dial path with the two adapters; the limit of one hop
   is lifted.
4. Site to post binding.
5. The persisted dead-hop file.
6. Logs, `status`, TUI, `doctor`, documentation.
