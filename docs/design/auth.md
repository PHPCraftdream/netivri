# Proxy authentication design

Package `internal/auth` provides password authentication for the mixed
HTTP/SOCKS5 listener. It is pure Go, holds no network state, and never
imports `internal/config` or `internal/core`: persistence is an
injected callback, and wiring into the core is task #12.

## Semantics

The authenticator satisfies the Mihomo
`component/auth.Authenticator` interface (`Verify(user, pass) bool`,
`Users() []string`) and is guarded by one mutex; the first-use
transition is atomic under concurrency. An empty user or pass is
always rejected (SOCKS4 sends no password, and an empty password must
never match); a password longer than 1024 bytes is rejected before any
hashing (bounds the argon2 input cost).

| Configured login | Configured password | Behavior |
| --- | --- | --- |
| *(both empty)* | *(both empty)* | `New` returns a true nil interface: auth disabled. |
| literal | argon2id hash | `user` must equal the login (constant-time compare); `pass` is verified with argon2id. After the first success the plaintext is cached in memory and later calls compare against the cache only (fast path). A failed attempt is never cached. |
| literal | `init` | The first call with the right user and a non-empty pass is accepted: the password is hashed and `Persist(login, hash)` is called. If `Persist` fails the attempt is **rejected** and the state stays `init` (fail closed) — the same attempt succeeds once persistence recovers. A wrong user is rejected before any hashing and never consumes the init. |
| `init` | argon2id hash | The first call whose password verifies against the hash claims the login: `Persist(user, thatSameHash)` is called, the plaintext is cached, and the row above applies from then on. |
| `init` | `init` | The first call with a non-empty user and pass wins: `Persist(user, argon2id hash of the pass)`; every other distinct pair is rejected afterwards. |

`Users()` returns the login once it is known — including when only the
password is the placeholder (the literal login is already configured).
It returns an empty (non-nil) slice **only while the login itself is
still the `init` placeholder**.

`New` fails early when a placeholder is configured but
`Options.Persist` is nil: such a configuration could never persist a
credential and would silently reject every attempt.

## Several accounts

`NewMulti` combines the optional legacy credential with restricted named
accounts. Account logins are unique, 1–64 ASCII characters from
`A-Za-z0-9._@-` except the reserved `init` placeholder; malformed
configurations fail with a generic `CONFIG_INVALID` error. Account lookup
takes precedence over legacy verification, including while the legacy login
is pending a claim. Account passwords may be argon2id PHC hashes or `init`;
for `init`, the first password is hashed and persisted before that account is
accepted. `Users` reports configured and claimed logins in sorted order, with
a non-nil empty slice when no login is known.

## Rejected claimed logins

A user-supplied login can only become the stored login if it survives
a config rewrite and round-trip: 1..256 bytes, no leading or trailing
whitespace, no control characters or line breaks. Additionally, the
literal `init` is **never accepted as a claimed login** (in the
login-`init` claim path): storing it would make the next start treat
the stored value as the placeholder again and let anyone re-claim it.
Rejected claims return `false` and never persist anything; no error
message echoes the rejected value.

## Hash format

`Hash` produces `$argon2id$v=19$m=19456,t=2,p=1${SALT}${KEY}` with a
fresh 16-byte `crypto/rand` salt and a 32-byte key, unpadded standard
base64 per the PHC string format. Default cost: 19 MiB memory, 2
passes, 1 lane. `VerifyHash` is a strict parser: only argon2id, only
`v=19`, exact field layout (`m=`, `t=`, `p=` in that order, no extra
fields), constant-time key comparison. Malformed hashes produce errors
that never echo the hash or the password.

Cost parameter caps read from a stored hash (a hostile config file
must not trigger a huge allocation): `m <= 262144` KiB (256 MiB;
minimum 8), `t <= 10`, `p <= 16`, salt 8–64 bytes, key 16–64 bytes.
The whole hash string is capped at 512 bytes before it is split, and
the *encoded* salt/key lengths are checked before they are decoded, so a
hostile hash cannot force large intermediate allocations either.

## Concurrency and cost control

Argon2 verification in standalone `New` uses its existing semaphore; standalone
init hashing remains serialized by its mutex. `NewMulti` uses one shared
semaphore for legacy and named-account verification and init hashing. Each
init transition (hash + persist + state update) is serialized under its
per-authenticator or per-account mutex. Multi-account init hashing takes a
semaphore slot while holding only that account's mutex. Legacy init uses its
own mutex and the shared semaphore; verification releases the semaphore
before attempting the claim mutex, so there is no lock-order cycle.

## Threat model and limits

- The init placeholder is accepted only on a loopback bind unless
  `auth.allow_init_remote` is true — enforced by `internal/config`
  validation, not here.
- There is **no rate limiting** beyond the semaphore: a local client
  can try passwords as fast as argon2 allows. The threat model is a
  single-user machine; if the listener is deliberately exposed, the
  owner accepts online guessing.
- The plaintext password is cached **in process memory by design**
  after the first success, so steady-state verification is cheap. A
  memory reader can already read the hash; this trades that margin for
  avoiding an argon2 computation per connection.
- SOCKS4 (no password field) is rejected whenever authentication is
  enabled: the listener only offers SOCKS5 auth and HTTP auth.
- `Persist` is called at most once per init consumption and receives
  the login and the PHC hash; netivri persists it by rewriting the
  whole config file (see docs/config.md).
