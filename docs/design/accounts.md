# Accounts with an exit country

One local proxy port, several accounts, each account with its own provider
and exit country. A client that authenticates as `alice` leaves through a
node of `alice`'s provider in `alice`'s country; `bob` through his; an
account without either uses the default route (the selected node, or direct
while none is selected).

## Providers

A provider is a subscription record. `netivri sub add <url>` authorises by the
link (the URL carries the token), fetches it and stores the record and the
node snapshot: that is "creating a provider". The list of countries a provider
offers comes from its stored snapshot: `netivri sub countries [NAME]`, or, for
a link that is not stored yet, `netivri sub countries --url URL [--via PROXY]`
(fetch and parse only; nothing is stored). The URL is never printed.

An account can be bound to a provider and to an exit country, together or
separately:

| `provider` | `exit_country` | The account leaves through |
| --- | --- | --- |
| empty | empty | the default route |
| empty | `DE` | a node in `DE` of ANY provider |
| `main` | empty | a node of provider `main`, any country |
| `main` | `DE` | a node in `DE` of provider `main` only |

Nodes of a route are ordered by stable node id; with several providers the
order is by node id across all of them. A provider that is bound to an
account cannot be removed (`sub rm` fails with `SUB_IN_USE`, exit 6).

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
| `login` | Required, unique, 1..64 characters of `A-Za-z0-9._@-`. The restricted alphabet keeps the login safe inside a Mihomo rule (`IN-USER,<login>,<group>`: no commas, slashes or spaces). |
| `password` | Required: an argon2id PHC hash, or the placeholder `init`. |
| `provider` | Optional: the name of a subscription (`subscriptions[].name`, 1..64 characters of `A-Za-z0-9._-`). Empty or missing = every provider. Config validation checks the shape only; `up` and the `account` commands check that the provider exists. |
| `exit_country` | Optional ISO 3166-1 alpha-2 code, stored upper case (`de` is normalised to `DE`). Empty or missing = default route. |

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
- one proxy per node of every route (internal names `netivri-r<R>-<N>` with
  the route's and the node's index, so a provider-supplied name never reaches
  a rule),
- one group per route: `netivri-group-<R>`, type `select`, members = the nodes
  of the route in the given order. `select` makes the first member the default
  and does no network I/O of its own, so the core stays hermetic; failover
  over alive nodes comes with the health checks.
- rules `IN-USER,<login>,netivri-group-<R>` for every account with a route,
  then the final `MATCH` to the default route: `netivri-node` when a default
  node is selected, `DIRECT` when none is (today's direct mode).

Consequences:

- **Fail closed.** An account whose route has no node (its country has none,
  its provider is unknown or holds no snapshot or no node of the country) is
  rejected at `up` with `NO_NODES_FOR_COUNTRY` (exit 5); a node that dies
  later makes the connections of that account fail. There is never a fallback to direct and
  never to another country.
- Accounts without a country, and `auth`'s account, take the default route.
- Applying changes needs a restart of the daemon (`down`, then `up`); there
  is no hot reload.

## CLI

```
netivri account add <login> [--provider NAME] [--country CC]
netivri account list
netivri account set-country <login> <CC>      (or --none to clear)
netivri account set-provider <login> <NAME>   (or --none to clear)
netivri account rm <login>

netivri sub countries [NAME | --url URL [--via PROXY]]
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
	// "<provider>~<CC>", either part may be empty); it never reaches a rule.
	RouteNodes map[string][]map[string]any
	// AccountRoute maps an authenticated login to the route key it uses.
	AccountRoute map[string]string
}
// func (i *Instance) AccountRoutes() map[string]string // login -> route key

// internal/daemon
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
