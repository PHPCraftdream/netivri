# Accounts with an exit country

One local proxy port, several accounts, each account with its own provider
and exit country. A client that authenticates as `alice` leaves through a
node of `alice`'s provider in `alice`'s country; `bob` through his; an
account without either uses the default route (the selected node, or direct
while none is selected).

## Providers

A provider is a subscription record. `netivri sub add {URL}` authorises by the
link (the URL carries the token), fetches it and stores the record and the
node snapshot: that is "creating a provider". The list of countries a provider
offers comes from its stored snapshot: `netivri sub countries [{SUBSCRIPTION}]`, or, for
a link that is not stored yet, `netivri sub countries --url {URL} [--via {PROXY}]`
(fetch and parse only; nothing is stored). The URL is never printed.

An account can be bound to a provider and to an exit country, together or
separately:

| `provider` | `exit_country` | The account leaves through |
| --- | --- | --- |
| empty | empty | the default route |
| empty | `DE` | a node in `DE` of ANY provider |
| `main` | empty | a node of provider `main`, any country |
| `main` | `DE` | a node in `DE` of provider `main` only |

`provider` and `exit_country` may each be a **list**, items separated by
commas (`main,backup`, `DE,NL`). The order is the priority, see
[Priority](#priority). A provider that is in the list of any account cannot be
removed (`sub rm` fails with `SUB_IN_USE`, exit 6).

## Priority

A route is a list of **tiers**. The countries come first, in the order
listed; inside a country the providers follow, in the order listed. A tier is
the nodes of one (country, provider) pair, ordered by node id:

| `provider` | `exit_country` | Tiers, first to last |
| --- | --- | --- |
| `main,backup` | empty | `main`, `backup` |
| empty | `DE,NL` | DE (all providers, one tier), NL (all providers, one tier) |
| `main,backup` | `DE,NL` | `main` in DE, `backup` in DE, `main` in NL, `backup` in NL |

Rules:

- A `*` item in the country list is the tier of every country the list does not
  name (nodes of an unknown country included), at its place: `DE,*` is DE, then
  the rest; `*,DE` the rest, then DE; `*` alone is any country, and with no
  provider list a route through every node of every provider. It is the same
  tier rule as for a named country: one tier per provider.
- Without a provider list all providers share a tier (nodes ordered by node id,
  ties by provider name), as before lists existed. Without a country list the
  tiers are the providers alone.
- A pair without a stored node is skipped and takes no tier. A route fails
  (`NO_NODES_FOR_COUNTRY`) only when it has no node at all; one listed
  provider or country without nodes is not an error while another has some.
- Selection: a new binding goes to a node of the **first tier that has a node
  not marked down**; inside the tier the usual choice applies (the lowest
  score, near-equal nodes shared by hash). When every node of a tier is down
  the next tier is used; when every node of the route is down the first tier
  is tried again. A later tier is a fallback, never a load-sharing partner.
- A binding of a site to a node (the sticky table) is kept while the node is
  up, with one exception: when the binding sits in a worse tier and a node of
  a better tier has been up (not down) for 5 minutes, the site's next
  connection is picked again and goes back to the better tier (counted in
  `affinity.returns`). The five minutes keep a node that flaps from pulling
  sites back and forth; open connections are never moved. Without this a short
  outage of the first tier would leave a site on a fallback exit for as long
  as it is used. `account show` marks the later tiers `fallback` and counts the
  connections on them; `doctor` notes them.
- Lists never reach beyond themselves: no fallback to a provider or country
  that is not listed, and none to a direct connection.
- The canonical stored form drops blanks and repeats (the first occurrence
  keeps its place) and upper-cases codes; the route key is built from it, so
  two spellings of one list are one route. Limits: 16 providers, 32 countries.

## Config shape

A separate top-level object, not a field of `auth`:

```ktav
accounts: [
    {
        login: alice
        password:: init
        provider: main
        exit_country: DE
    }
    {
        login: bob
        password:: $argon2id$v=19$m=19456,t=2,p=1$...
        exit_country: NL
    }
]
```

(The exact Ktav spelling follows what `subscriptions` already uses; see
docs/config.md.)

| Field | Meaning |
| --- | --- |
| `login` | Required, unique, 1..64 characters of `A-Za-z0-9._@-`. The restricted alphabet keeps the login safe inside a Mihomo rule (`IN-USER,{LOGIN},{GROUP}`: no commas, slashes or spaces). |
| `password` | Required: an argon2id PHC hash, or the placeholder `init`. |
| `provider` | Optional: the name of a subscription (`subscriptions[].name`, 1..64 characters of `A-Za-z0-9._-`), or a comma-separated list of names in priority order (at most 16). Empty or missing = every provider. Config validation checks the shape only; `up` and the `account` commands check that the provider exists. |
| `exit_country` | Optional ISO 3166-1 alpha-2 code, stored upper case (`de` is normalised to `DE`), or a comma-separated list of codes in priority order (at most 32). Empty or missing = default route. |

Accounts also take optional `pre_socks5` and `post_socks5` (proxy URLs, `socks5://`, `http://` or `https://`,
or `none`), which override the global `chain.*` values side by side; see
[socks5-chain.md](socks5-chain.md).

Rules that follow from the choice of a separate object:

- The existing `auth.login` / `auth.password` keep working unchanged and stay
  a single account **without** an exit country (the default route). A config
  may carry `auth`, `accounts`, or both. A login may appear only once across
  `auth.login` and `accounts[].login` (an `auth.login` of `init` is exempt
  until it is claimed; a claim that collides with an account login is
  rejected).
- `init` in an `accounts` entry means: the login is fixed by the entry, the
  first password presented for that login is hashed with argon2id, stored in
  the entry and from then on required. (Unlike `auth.login: init`, an entry's
  login is never claimed.) The same safety rule as for `auth` applies: a
  placeholder is accepted only on a loopback bind unless
  `auth.allow_init_remote` is true.
- Authentication is enabled as soon as `auth` or any account is configured,
  so a non-loopback bind is accepted under the same conditions as today.
- Config validation checks the shape only (two ASCII letters for the
  country). Whether a code is a real country, and whether any node exits
  there, is checked where the node pool is known (`up`, `account` commands).

## Routing

Mihomo v1.19.32 sets `metadata.InUser` to the authenticated user for SOCKS5
(`listener/socks/tcp.go`) and HTTP (`listener/http/proxy.go`) and has the rule
type `IN-USER` (`rules/common/in_user.go`; several users are separated by `/`,
hence the login alphabet). The core therefore builds, in rule mode:

- one route per distinct (provider, country) pair that an account uses; the
  caller names a route with an opaque key and hands the core the nodes of each
  route, in preference order (the core knows nothing about providers or
  countries),
- one proxy per node of every route (internal names `netivri-r{R}-{N}` with
  the route's and the node's index, so a provider-supplied name never reaches
  a rule),
- one group per route: `netivri-group-{R}`, type `select`, members = the nodes
  of the route in the given order. `select` makes the first member the default
  and does no network I/O of its own, so the core stays hermetic; failover
  over alive nodes comes with the health checks.
- rules `IN-USER,{LOGIN},netivri-group-{R}` for every account with a route,
  then the final `MATCH` to the default route: `netivri-node` when a default
  node is selected, `DIRECT` when none is (today's direct mode).

Consequences:

- **Fail closed.** An account whose route has no node (its country has none,
  its provider is unknown or holds no snapshot or no node of the country) is
  rejected at `up` with `NO_NODES_FOR_COUNTRY` (exit 5); a node that dies
  later makes the connections of that account fail, unless a later tier of its
  route has a node. There is never a fallback to direct and never to a country
  or provider outside the account's lists.
- Accounts without a country, and `auth`'s account, take the default route.
- A running angel applies changes by itself (see [live-reload.md](live-reload.md)).
  A new country or provider list keeps the open connections; adding or
  removing an account or a new password restarts the listener, so open
  connections end.
- `set-country` and `set-provider` ask the question `up` asks before they
  write: would every account addressed still have a stored node on its route?
  If not, nothing is written for any account (`NO_NODES_FOR_COUNTRY`). Whether
  a node is alive is not part of the question; that changes by the minute and
  is what the probes and the sticky group are for.

## CLI

```
netivri account add {ACCOUNT} [--provider {SUBSCRIPTION}] [--country {COUNTRY}]
netivri account list
netivri account set-country {ACCOUNT}[,{ACCOUNT}...] {COUNTRY}      (or --all, or --none to clear)
netivri account set-provider {ACCOUNT}[,{ACCOUNT}...] {SUBSCRIPTION}   (or --all, or --none to clear)
netivri account rm {ACCOUNT}

netivri sub countries [{SUBSCRIPTION} | --url {URL} [--via {PROXY}]]
```

All JSON, idempotent (`add` of an existing login with the same country is a
success, with another country `ACCOUNT_EXISTS`), and never printing a
password or hash. `account add` writes the password as `init`: the first
client sets it. `status` lists each account's `login`, `provider` and
`exit_country`. `account add` and `set-provider` check that the provider
exists (`SUB_NOT_FOUND`, exit 5).

## Go contract between the packages

Agreed up front so the parts can be built in parallel:

```go
// internal/config
type Account struct {
	Login       string `json:"login"`
	Password    string `json:"password"`
	Provider    string `json:"provider,omitempty"`
	ExitCountry string `json:"exit_country,omitempty"`
}
// Config gains: Accounts []Account `json:"accounts,omitempty"`
// func (c *Config) Account(login string) (*Account, int)  // nil, -1 when absent
// func ValidAccountLogin(login string) bool
// func ValidProviderName(name string) bool                // 1..64 of A-Za-z0-9._-
// func NormalizeCountry(code string) (string, bool)       // upper-cases, checks two ASCII letters

// internal/auth
type AccountSpec struct{ Login, Password string } // Password: hash or config.InitPlaceholder
type MultiOptions struct {
	Legacy         Options                                   // the single `auth` account; zero Login and Password = none
	Accounts       []AccountSpec
	PersistAccount func(login, passwordHash string) error    // stores a claimed account password
}
func NewMulti(o MultiOptions) (mihauth.Authenticator, error) // nil authenticator when nothing is configured

// internal/core (Options gains)
type Options struct {
	// ...existing fields...
	// RouteNodes holds, per route key, the Mihomo proxy mappings of the nodes
	// of that route, in preference order. A route key is an opaque string of
	// 1..128 printable ASCII characters chosen by the caller (the CLI uses
	// "{PROVIDER}~{COUNTRY}", either part may be empty); it never reaches a rule.
	RouteNodes map[string][]map[string]any
	// AccountRoute maps an authenticated login to the route key it uses.
	AccountRoute map[string]string
	// NodeTiers gives, per route key, a priority tier for every node of
	// RouteNodes[key] (same order, never decreasing); nil = one tier. A new
	// binding goes to the first tier that has a node not down.
	NodeTiers map[string][]int
}
// func (i *Instance) AccountRoutes() map[string]string // login -> route key

// internal/angel
type AccountInfo struct { // Status.Accounts element
	Login       string `json:"login"`
	Provider    string `json:"provider"`
	ExitCountry string `json:"exit_country"`
}
```

## Non-goals

No per-account limits or quotas, no hot reload, no per-account node (only a
country), no account that is both unauthenticated and routed (a country needs
a login).
