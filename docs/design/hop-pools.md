# Pools of pre and post hops

An account route can configure up to 16 `pre_socks5` candidates (before the VPN
node) and 16 `post_socks5` candidates (after it). They are alternatives, not
serial hops. The core uses weighted selection, quarantine and recovery probes;
a post candidate can be bound per login and site. This note describes the
current implementation: sibling core clients integrate with the native route
adapters, while Mihomo retains responsibility for the VPN node protocols.

## Configuration and cascade

Each global, provider and account side is a full candidate list. The global
list is inherited by default, a provider list replaces global for that
provider's nodes, and an account list replaces its provider/global candidates.
Lists are not merged. `none` explicitly clears inherited candidates;
empty/absent scoped values inherit. Legacy scalar Ktav URLs continue to load
as singleton lists and render back as scalars. More than 16 URLs per side and
duplicate entries are rejected.

Account routes (including direct accounts with post candidates) use the pool
fields carried into core options: `RoutePrePool`, `RoutePostPool`, `NodePrePool`
and `NodePostPool`, with old scalar fields kept for singleton legacy routes.
`GlobalPost` remains the scalar of connections without an account when the
global list has one hop. A global list of several hops is carried by
`GlobalPostPool` and serves those connections (the `MATCH` rule: the default
route and legacy auth) through the same pool adapter and health as account
routes; a one-hop `GlobalPostPool` builds exactly the scalar mapping. A global
pre field is not defined: the default route has no pre side. A global pool of
several distinct hops is refused with a default `Node`. The pool identity of the
global pool is the route `MATCH` (`core.GlobalPoolRoute`).

## Weighted selection and health

Soft failures add one to a sand score capped at 8; the score halves every 30
seconds, and success halves it. The weight is `exp(-ln(20) * sand / 8)`, with
a floor of 0.05. Candidates are selected as a weighted random permutation;
quarantined candidates are excluded while any candidate is healthy.

Three consecutive hard failures quarantine a candidate for 30 seconds, and so
do five soft failures without a success in between (a soft failure interrupts
the hard streak but not the soft one); soft failures while quarantined do not
extend the backoff. Each
re-trip doubles its backoff to the 600-second cap. A due candidate gets one
25-second probe lease. Successful probing revives it; hard probe failure
re-trips with doubled backoff. If every candidate is quarantined, pool order
includes quarantined candidates and they are tried; in particular a post pool
never falls back to a node-only/direct path. Pre dialing tries candidates in
weighted order until one succeeds, all have been attempted, or two hops have
reported the next address unreachable (the node is then likely down for all).
Post dialing retries another post hop only while nothing can have reached the
site: a Connect, Handshake or Refusal failure before the target request was
sent; it never bypasses the post hop.

Each attempt has a 3-second negotiation budget up to the moment the target
request is sent (connect, TLS, SOCKS5 method and authentication). A silent hop
then fails with a typed stage instead of a context timeout: a connect or TLS
timeout is a hard Connect failure, a silent SOCKS5 negotiation an ambiguous
Handshake failure. The rest of the connection's deadline remains for another
hop. A hop URL the native client cannot use is a hard Connect failure and is
never probed, so it stays quarantined. A probe refused by the hop is a hard
probe failure.

Soft/hard blame is stage-aware. Transport failure connecting to the current
hop is hard against that hop; an explicit downstream-unreachable response is
hard against the next hop; ambiguous handshake failures are soft against both
neighbors; authentication/policy refusal is soft against the refusing hop;
unattributable errors and local capacity/cancellation do not poison hop health.
Post-hop probes attribute a failure only with recent evidence that the carrying
VPN node answered.

## Site binding

Post pools bind on `(login, site)`. `site` is a lowercase registrable domain
using Public Suffix List rules, or an IP literal; a healthy binding stays with
its hop, even if the node below it changes. If its hop becomes unhealthy or
quarantined, the binding is removed and a replacement is selected before
trying the dial. The default sliding binding lifetime is 24 hours. Connections
without a login (the global default route) bind under one fixed anonymous login,
so their sites are keyed by site alone; that namespace never collides with an
account login.

## Persistence and logs

Pre and post quarantine state are stored separately in
`{HOME}/run/hops-pre.health` and `{HOME}/run/hops-post.health`. Records include
only lowercase 64-character hexadecimal SHA-256 hop keys, trip counts and
probe timestamps. The hop key is a canonical `scheme://host:port` digest
without userinfo; unsupported roles, malformed keys and unknown pool entries
are dropped. This state is quarantine-only: sand/weights and active leases are
not persisted, and restored quarantines become probe-eligible immediately.

Logs never contain proxy URLs or credentials. A hop quarantine/re-trip is
WARN with role, the first eight characters of its opaque key, trip count and
backoff. Revival is INFO with the downtime. A pool with all candidates down
emits a rate-limited WARN: `all N hops are down, trying them anyway`.

## Implementation boundaries

A pool is installed for account-route node hops by the native core lifecycle.
Pre dialing carries the selected proxy to the VPN node; post dialing carries
traffic through the already-selected VPN node (or directly for an explicit
direct account). Each post candidate is a proxy entry behind that selected
node; pool adapters own selection, health attribution, site binding and recovery.
Single-candidate legacy chains retain the scalar adapter path. This is not a
helper process or a statically expanded pre × node × post route matrix.
