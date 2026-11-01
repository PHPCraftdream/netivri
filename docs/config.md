# Configuration file reference

netivri stores its settings in [Ktav](https://github.com/ktav-lang/spec)
0.8.0, a plain-text key/value format with `##` whole-line comments and
dotted keys (`docs/design/config-ktav.md` records why Ktav was chosen).
The file is read with the official Go binding
(`github.com/ktav-lang/golang`) and is safe to edit by hand. When
netivri needs to persist something (the first `init`, `sub add`,
`use`, ...), it rewrites the file as a whole: hand-written comments are
not preserved, so keep personal notes elsewhere.

## Location

- `netivri config path` prints it; it is always
  `{HOME}/config.ktav`.
- `{HOME}` is, first match wins: `--home {DIR}`; `./.netivri` with `--local`
  (created) or when that directory exists in the current directory; `$NETIVRI_HOME`;
  otherwise `%APPDATA%\netivri` (Windows) or `$XDG_CONFIG_HOME/netivri`
  (`os.UserConfigDir()`). A separate directory is a separate netivri, see
  [cli.md](cli.md), "Separate state directories".
- A missing file is not an error: safe defaults apply
  (`netivri config get` shows the effective values).

## Fields

| Field | Type | Default | Meaning |
| --- | --- | --- | --- |
| `listen.bind` | string | `127.0.0.1` | Address the mixed HTTP/SOCKS5 proxy binds. An IP address or `localhost`. Keep it on loopback unless you understand the exposure. |
| `listen.port` | integer | `7890` | Proxy port, 1–65535. HTTP and SOCKS5 are served on the same port. |
| `listen.allowed_clients` | string | `*` | Client addresses that may connect to the proxy: comma-separated rules (`*`, `loopback`, an exact address, an IPv4 whole-octet mask such as `192.168.1.*`, or a CIDR prefix such as `10.0.0.0/8`); see below. |
| `auth.login` | string | *(empty)* | Proxy username. A literal, or the placeholder `init` (see below). Empty together with `auth.password` means: no authentication. |
| `auth.password` | string | *(empty)* | Proxy password: `init`, or an argon2id PHC hash (starts with `$argon2id$`). Plaintext passwords are rejected by validation. |
| `auth.allow_init_remote` | boolean | `false` | Permit the `init` placeholder on a non-loopback bind. |
| `fetch.proxy` | string | *(empty)* | Proxy used for all outgoing subscription fetches. One of `socks5://`, `socks5h://`, `http://`, `https://`, optionally with `user:password@` credentials. |
| `fetch.update_interval` | string | *(empty)* = `1h` | How often the running angel re-fetches every subscription. A duration from `5m` to `168h` (`30m`, `1h`, `12h`), or `off`. See [`docs/design/live-reload.md`](design/live-reload.md). |
| `chain.pre_socks5` | string | *(empty)* | Default proxy netivri connects through to reach a VPN node: `socks5://`, `http://` or `https://` `[user:password@]host:port`, or `none`. An account's own `pre_socks5` overrides it. See "SOCKS5 chain" below. |
| `chain.post_socks5` | string | *(empty)* | Default proxy the traffic goes to after the VPN node (or directly, for traffic without a route): same format (`socks5://`, `http://` or `https://`). An account's own `post_socks5` overrides it. |
| `subscriptions` | array | `[]` | Provider subscriptions: objects with `name`, `url` (secret!) and optional `via` (a per-subscription fetch proxy, same scheme rules as `fetch.proxy`). Names must be unique and non-empty. |
| `accounts` | array | `[]` | Proxy accounts: objects with required unique `login` (1–64 ASCII letters, digits, `.`, `_`, `@`, `-`), required `password` (`init` or argon2id PHC hash), optional `provider` (the name of a subscription: 1–64 characters of `A-Za-z0-9._-`; or up to 16 names separated by commas, in priority order), and optional `exit_country` (two ASCII letters, normalized uppercase; or up to 32 codes separated by commas, in priority order, with `*` for "any other country"), and optional `allowed_clients` (the same rule list as `listen.allowed_clients`, restricting the addresses this account may authenticate from; enforced before the password is verified, or an `init` account cannot be claimed from a disallowed address), and optional `pre_socks5` and `post_socks5` (the same URLs as `chain.*`, or `none`, overriding the global value of that side for this account). |
| `servers` | array | `[]` | Inbound server endpoint objects; see [Server listeners](#server-listeners). Each has `name`, `protocol`, optional `bind`, `port`, `accounts`, optional `public_host`, and flattened protocol-specific option keys. Credentials/material are kept separately in `{HOME}/server-secrets.ktav`. |
| `selection.node` | string | *(empty)* | Reserved for the planned `netivri use {NODE}`: validated, but nothing reads it yet. |
| `selection.country` | string | *(empty)* | Reserved for the planned `netivri use --country {COUNTRY}`: validated, but nothing reads it yet. |
| `selection.mode` | string | `manual` | Reserved for the planned `netivri use`: `manual`, `fastest` or `auto`; validated, but nothing reads it yet. Routing today comes from the accounts. |

`accounts` is separate from `auth`; either or both may be configured. Account logins are unique, and cannot collide with a non-`init` `auth.login`. The reserved login `init` cannot be used for an account. `exit_country` may be omitted or empty to use the default route; codes are normalized to uppercase when loaded. Shape validation checks two ASCII letters per code, not whether that code represents a real country. Config validation checks the provider's shape only; `up` and the `account` commands check that the named subscriptions exist.

`provider` and `exit_country` may each hold a list: items separated by commas, blanks around a comma ignored, a repeated item kept once. In `exit_country` a `*` item stands for every country the list does not name, nodes of an unknown country included, at its place in the order (`DE,*` is DE first, then everything else; `*` alone is any country). When the file is loaded the lists are rewritten in the canonical form (`DE,NL`, `main,backup`), and the commands store them that way. The order is the priority: the countries first, then, inside a country, the providers (`provider: main,backup` with `exit_country: DE,NL` tries `main` in DE, `backup` in DE, `main` in NL, `backup` in NL). See [`docs/design/accounts.md`](design/accounts.md).

## Server listeners

Each server's protocol-specific options are written as flattened keys in the endpoint object, alongside `name`, `protocol`, and other server fields. The Go config model collects these keys into `Server.Params` (`map[string]string`); that internal map is not a nested `params` object in Ktav. Parameter values are configuration, not credentials, and are hidden by `server list`/`show`. Passwords, UUIDs, keys, certificates, and generated protocol material are stored separately in `{HOME}/server-secrets.ktav`, not in `config.ktav`.

| Key | Type | Default | Validation and meaning |
| --- | --- | --- | --- |
| `servers[].name` | string | required | 1-32 ASCII letters, digits, `.`, `_`, `-`; unique. |
| `servers[].protocol` | string | required | Must match one of the registered names in [the server index](servers/README.md). |
| `servers[].bind` | string (IP address) | `0.0.0.0` | Must parse as an IP address. Empty means `0.0.0.0`. |
| `servers[].port` | integer | required | 1-65535. Same-transport overlap is rejected (`PORT_IN_USE`) except eligible shared TLS TCP endpoints with distinct protocol-owned explicit match keys; overlaps return `SERVER_PORT_AMBIGUOUS`. TCP and UDP-bound servers may share a port number. |
| `servers[].accounts` | string | required | `*` expands to all configured accounts for multi-user protocols only, or a comma-separated list of unique, existing account logins. Logins are 1-64 ASCII letters, digits, `.`, `_`, `@`, `-`; `init` is reserved. Single-user protocols require exactly one resolved account. |
| `servers[].public_host` | string | empty | Optional link host. Config accepts a scalar string; `server link` strictly validates the host as a hostname or IP without a port or credentials; `--host` overrides it. |
| `servers[].default` | boolean | `false` | Marks the single TLS fallback on a shared TCP endpoint for a valid unmatched ClientHello. |
| `servers[].{PARAMETER}` | string, boolean, integer | protocol-dependent | Protocol-specific option as a flattened Ktav key. Arbitrary unrecognized keys are collected into the Go `Server.Params` map; scalar booleans and integers are converted to strings. Maps, sequences, and floats are rejected. Reserved server field names are not parameters. Values must also pass protocol validation. See each protocol page under [Server protocols](servers/README.md). |

Shared servers need protocol-declared explicit match keys matching exported links. Trojan and AnyTLS require `sni` unless the server is the default; VLESS-Reality uses `sni` or a destination-derived servername. TrustTunnel without explicit `sni` matches h2 at any SNI; with explicit `sni`, it matches exactly. An absent-SNI key otherwise matches only absent SNI. Explicit `sni` is recommended for certificate-TLS shared members. A sole TLS server may be the default; a shared server without an explicit usable key must itself be the default. A default link without `sni` may use host-derived SNI, remain unmatched, and fall back. `SERVER_PORT_AMBIGUOUS` for a missing explicit key hints `set sni on {NAME} or mark it as the default`; for overlapping keys, change `sni` (a default cannot resolve overlap).
Listener startup failures make `up` fail closed. A failed live restart leaves the angel/control process running; if both replacement and rollback cores fail, status can show `core: "down"` and `reload.last_error`, and the angel retries the prior good configuration with backoff. `allowed_clients`-only changes on an existing account apply hot to server policy without restarting listeners; existing connections survive. This applies for mixed authentication and server endpoints, including when no account route is configured. Global `listen.allowed_clients`, server credentials, account membership, bind and port changes require restart/reconfiguration. A failed server reconfiguration keeps prior servers where possible; UDP restoration after a failed restart is best-effort.

`allowed_clients` for a server connection is the intersection of global `listen.allowed_clients` and the authenticated account's optional rule; either may deny the peer. See [Allowed clients](#allowed-clients) for the shared rule syntax.

`server add`, `list`, `link`, `rotate` and `remove` examples and parameters are in the [server CLI reference](servers/cli.md). Shared endpoints require distinct SNI/ALPN matches and allow one `default` fallback; ambiguity is not fixed by declaring a default. Link output is credential-bearing; it is the only intended output of a generated server secret. The secrets store is written atomically with owner-only permissions. Do not copy links or secret-bearing Mihomo YAML into public logs.

This shared configuration example defines its account inline and uses identical bind/port values, distinct valid SNI values, and exactly one default fallback. Alternatively, a shared member without an explicit usable key must itself be the sole default; a lone exclusive server may omit SNI entirely.

```ktav
accounts: [
    {
        login: alice
        password:: init
    }
]

servers: [
    {
        name: trojan-edge
        protocol: trojan
        bind: 127.0.0.1
        port: 8443
        accounts: alice
        default: true
        sni: trojan.example.org
    }
    {
        name: anytls-edge
        protocol: anytls
        bind: 127.0.0.1
        port: 8443
        accounts: alice
        sni: anytls.example.org
    }
]
```

## SOCKS5 chain

`chain.pre_socks5` and `chain.post_socks5` (global) and `pre_socks5` and
`post_socks5` of an account put proxies around the VPN node (the names say
SOCKS5, the value may also be an HTTP or HTTPS proxy):

```text
client -> netivri -> [pre] -> VPN node -> [post] -> destination
```

- `pre_socks5` is how netivri reaches the VPN node: the node's own connection
  goes through this proxy. It applies to accounts with a route (a provider
  and/or an exit country). An account without a route has no VPN node, so a
  pre never applies to it.
- `post_socks5` is where the traffic goes after the VPN node: the node hands
  the connection to the post proxy, which connects to the destination. It
  applies to accounts with a route, to accounts without one (their traffic
  goes to the post proxy directly) and to unauthenticated traffic (the global
  value).

A value is `{SCHEME}://[user:password@]host:port` with the scheme `socks5`,
`http` (an HTTP CONNECT proxy) or `https` (an HTTP CONNECT proxy reached over
TLS, certificate checked), case-insensitive, a host (name, IPv4 or `[IPv6]`), a **required** port
1..65535, optional percent-encoded user name and password, no path, query or
fragment. `none` is also accepted. Anything else is `CONFIG_INVALID`; the
message names the key (`chain.pre_socks5`, `accounts[2].post_socks5`) and
never contains the value. Write the value with the raw marker:
`chain.pre_socks5:: socks5://user:secret@pre.example:1080`.

**Cascade.** Each side is resolved on its own, account over global:

| Global | Account | Effective |
| --- | --- | --- |
| *(empty)* | *(absent)* | no proxy |
| `socks5://g.example:1080` | *(absent)* | `socks5://g.example:1080` (source `global`) |
| `socks5://g.example:1080` | `socks5://a.example:1081` | `socks5://a.example:1081` (source `account`) |
| `socks5://g.example:1080` | `none` | no proxy (source `none`) |
| `none` | *(absent)* | no proxy |

`netivri account set-socks5 {ACCOUNT} --pre {URL|none|inherit}` and `--post`
change the account values; `inherit` removes the account key so the global
value applies again.

Credentials are secrets, like subscription URLs: no output, log or error
shows them. `account list`, `account show` and `status` print the URL as
`socks5://host:port` with `"auth": true` when it had a user name, and the
`source` of the value (`account`, `global` or `none`).

Routes of accounts with different effective chains are separate, but the
health and statistics of a node are kept per node, so two accounts that use
the same node through different proxies share its health. A change of
`chain.pre_socks5` or of an account's `pre_socks5` or `post_socks5` is applied
by the running angel in place and keeps the open connections; a change of
`chain.post_socks5` restarts the listener (see
[`design/socks5-chain.md`](design/socks5-chain.md)).

## Allowed clients

`listen.allowed_clients` says which client addresses may connect to the
proxy at all. The value is a comma-separated list of rules; spaces around
a comma are ignored:

- `*` — every address. It may appear anywhere in the list and allows
  everything.
- `loopback` — `127.0.0.0/8` and `::1`.
- an exact IPv4 or IPv6 address — `192.168.1.5`, `fd00::1`.
- an IPv4 whole-octet mask — trailing `*` octets, whole octets only:
  `192.168.1.*` is `/24`, `192.168.*` and `192.168.*.*` are `/16`,
  `10.*` is `/8`. A `*` anywhere else (`192.*.1.5`, `19*`) is an error;
  use a CIDR prefix instead.
- a CIDR prefix — `10.0.0.0/8`, `fd00::/8` (IPv4 or IPv6).

Limits: at most 64 rules, at most 1024 bytes in total. An absent key
means `*` (every address may connect); the key is then not rendered to
the file, and the loaded value of an absent key is the effective `*`.
Any present value that is empty or whitespace-only — including the
raw-string spelling `listen.allowed_clients::` and a nested-table
form — is `CONFIG_INVALID` naming `listen.allowed_clients`: omit the
key or set it to `*`. The value is stored as written,
after trimming the spaces around the commas; `netivri status` shows it
as `allowed_clients` (`*` when unset). An empty item or an unparsable
item is `CONFIG_INVALID` naming the key and the bad item.

The rule is checked against the real TCP peer address (an IPv4-mapped
IPv6 peer is unmapped first) in the listener's accept loop, before any
SOCKS or HTTP handshake and before authentication: a refused connection
is closed without a single reply byte. It is an additional gate, not a
replacement for authentication — a non-loopback `listen.bind` still
requires proxy authentication (`AUTH_REQUIRED` is unchanged). A change
takes effect through the live reload, which restarts the listener.

Each account may carry its own optional `allowed_clients` with the same
grammar and limits. Unlike the global key, an empty account value is not
an error — it means the account has no rule of its own — but a value
that is present and unparsable is `CONFIG_INVALID`
(`accounts[i].allowed_clients: ...`). The two rules cascade: the peer
must match the global rule and, when the account has one, the account
rule, so an account rule can only narrow what the global rule already
allows, never widen it. To let one account in from a wider set than the
others, keep the global rule `*` and restrict the other accounts
individually. The account rule is enforced during authentication,
before the password is verified and before an `init` placeholder can be
claimed: a client from a disallowed address sees the same failure as a
wrong password and can never claim the init password. The legacy
`auth.login` / `auth.password` credential has no per-account rule; only
the global one applies. Account allowlist-only changes apply hot to proxy and
server policy without restarting listeners; existing connections survive.

## The `init` placeholder

Writing `auth.login: init` / `auth.password: init` means "the first
local client that authenticates fills the field in": the angel hashes
the supplied password with argon2id and replaces the placeholder in the
file (the file is rewritten as a whole — comments are not preserved). An
account object's `password: init` similarly accepts a first password for
that fixed login and stores its argon2id hash. Defaults are safe: `init`
is only accepted when `listen.bind` is a loopback address, unless
`auth.allow_init_remote: true`. Once a real credential is stored, later
connections must match it. Any configured account enables authentication
for a non-loopback bind.

## Example

```ktav
## netivri configuration — Ktav 0.8.0
## Values that would otherwise parse as numbers/booleans/null but are
## meant as strings use the raw marker `::` (spec § 5.4).

listen.bind: 127.0.0.1
listen.port: 7890
## Who may connect: * (anyone), loopback, addresses, masks, CIDR.
listen.allowed_clients: loopback

auth.login:: init
auth.password:: init
auth.allow_init_remote: false

## All subscription fetches go through this proxy.
## fetch.proxy:: socks5://user:secret@proxy.example:1080
## Re-fetch every subscription this often (5m to 168h, or off).
fetch.update_interval: 1h

subscriptions: [
    {
        name: main
        url:: https://provider.example/sub/SECRET
    }
]

accounts: [
    {
        login: alice
        password:: init
        provider: main
        exit_country: DE
    }
    {
        login: bob
        password:: init
        provider: main,backup
        exit_country: DE,NL,*
        allowed_clients: 10.0.0.0/8
    }
]

selection.node::
selection.country::
selection.mode: manual
```

Chain settings in a file (global in dotted form, per account inside the
object):

```ktav
chain.pre_socks5:: socks5://user:secret@pre.example:1080
chain.post_socks5:: socks5://post.example:1080

accounts: [
    {
        login: carol
        password:: init
        provider: main
        exit_country: NL
        pre_socks5:: none
        post_socks5:: socks5://carol-exit.example:1080
    }
]
```

## Value types and the `::` marker

Ktav infers scalar types from spelling: `7890` is an integer, `true` a
boolean, `null` null. Where a string is intended (URLs, names,
passwords), netivri writes the value with the raw marker `key:: value`,
which forces the String type. The strict parser (`LoadsStrict`) rejects
lossy numeric spellings such as `0x1ED2` or `1_000` for real numbers,
so nothing is silently coerced.

## How netivri rewrites the file

netivri never edits single lines. When it persists a change, it renders
the whole configuration from its typed state (`Dumps` over the config
struct, so the field order is deterministic), prepends a generated
header comment — "managed by netivri, rewritten on change, comments
are not preserved" — and verifies the result before writing: the
rendered text must parse back (strict parser) and decode to exactly
the in-memory configuration, or netivri refuses to write it.

Consequences:

- Comments and their order are **not** preserved across rewrites; the
  only comments in the file are the generated header. Keep notes in a
  separate file.
- Values that would otherwise be misread (a password like `123456`)
  are written with Ktav's raw String marker `key:: value` by the
  renderer itself.
- Fields holding their zero value are omitted where that keeps the
  file tidy (`omitempty`); identity fields such as `listen.bind`,
  `listen.port` and `selection.mode` are always written, everything
  else only when set.
- The write is atomic: a 0600 temporary file in the same directory,
  fsynced, renamed over the target; the parent directory is created
  0700.

Error messages from the config package never echo user-supplied values
(names, URLs, passwords, proxy credentials) or document text: parse
errors are reported from the binding's structured fields only (error
class, reason, line number, spec section, key path), e.g.
`subscriptions[1] duplicates subscriptions[0]`.

## Parser

Reading and writing the file uses the pure-Go backend of the official Ktav
binding, built into the `netivri` binary: no native library is loaded,
downloaded or shipped, and nothing can be fetched behind your back.

## Redaction

Subscription URLs contain secrets and are never printed in full. Logs,
errors and status output show them redacted: scheme + host + `/****`
(path and query dropped). Proxy URLs with credentials keep only the
username with the password masked: `socks5://user:****@host:port`.
Chain hop URLs are redacted harder: the whole user name and password are
dropped, `scheme://host:port` (`socks5`, `http` or `https`) is all that is shown.
Parse errors are reported only from the parser's structured fields
(error class, reason, line number, spec section, key path) and never
contain document text, so passwords and URLs cannot leak through them.
