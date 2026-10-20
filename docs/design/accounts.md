# Accounts with an exit country

One local proxy port, several accounts, each account with its own exit
country. A client that authenticates as `alice` leaves through a node in
`alice`'s country; `bob` through his; an account without a country uses the
default route (the selected node, or direct while none is selected).

## Config shape

A separate top-level object, not a field of `auth`:

```ktav
accounts: [
    {
        login: alice
        password:: init
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

- one proxy per node of every country that an account uses (internal names
  `netivri-<CC>-<index>`, so a provider-supplied name never reaches a rule),
- one group per such country: `netivri-group-<CC>`, type `select`, members =
  the nodes of that country in preference order (deterministic: ascending
  stable node id). `select` makes the first member the default and does no
  network I/O of its own, so the core stays hermetic; failover over alive
  nodes comes with the health checks (task #15).
- rules `IN-USER,<login>,netivri-group-<CC>` for every account with a
  country, then the final `MATCH` to the default route: `netivri-node` when a
  default node is selected, `DIRECT` when none is (today's direct mode).

Consequences:

- **Fail closed.** An account whose country has no node is rejected at
  `up` with `NO_NODES_FOR_COUNTRY` (exit 5); a node that dies later makes the
  connections of that account fail. There is never a fallback to direct and
  never to another country.
- Accounts without a country, and `auth`'s account, take the default route.
- Applying changes needs a restart of the daemon (`down`, then `up`); there
  is no hot reload.

## CLI

```
netivri account add <login> [--country CC]
netivri account list
netivri account set-country <login> <CC>      (or --none to clear)
netivri account rm <login>
```

All JSON, idempotent (`add` of an existing login with the same country is a
success, with another country `ACCOUNT_EXISTS`), and never printing a
password or hash. `account add` writes the password as `init`: the first
client sets it. `status` lists each account's `login` and `exit_country`.

## Go contract between the packages

Agreed up front so the parts can be built in parallel:

```go
// internal/config
type Account struct {
	Login       string `json:"login"`
	Password    string `json:"password"`
	ExitCountry string `json:"exit_country,omitempty"`
}
// Config gains: Accounts []Account `json:"accounts,omitempty"`
// func (c *Config) Account(login string) (*Account, int)  // nil, -1 when absent
// func ValidAccountLogin(login string) bool
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
	// Countries holds, per upper-case ISO code, the Mihomo proxy mappings of
	// the nodes that exit there, in preference order.
	Countries map[string][]map[string]any
	// AccountCountry maps an authenticated login to its exit country code.
	AccountCountry map[string]string
}
```

## Non-goals

No per-account limits or quotas, no hot reload, no per-account node (only a
country), no account that is both unauthenticated and routed (a country needs
a login).
