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
  `<home>/config.ktav`.
- `<home>` is `$NETIVRI_HOME` if set, otherwise
  `%APPDATA%\netivri` (Windows) or `$XDG_CONFIG_HOME/netivri`
  (`os.UserConfigDir()`).
- A missing file is not an error: safe defaults apply
  (`netivri config get` shows the effective values).

## Fields

| Field | Type | Default | Meaning |
| --- | --- | --- | --- |
| `listen.bind` | string | `127.0.0.1` | Address the mixed HTTP/SOCKS5 proxy binds. An IP address or `localhost`. Keep it on loopback unless you understand the exposure. |
| `listen.port` | integer | `7890` | Proxy port, 1–65535. HTTP and SOCKS5 are served on the same port. |
| `auth.login` | string | *(empty)* | Proxy username. A literal, or the placeholder `init` (see below). Empty together with `auth.password` means: no authentication. |
| `auth.password` | string | *(empty)* | Proxy password: `init`, or an argon2id PHC hash (starts with `$argon2id$`). Plaintext passwords are rejected by validation. |
| `auth.allow_init_remote` | boolean | `false` | Permit the `init` placeholder on a non-loopback bind. |
| `fetch.proxy` | string | *(empty)* | Proxy used for all outgoing subscription fetches. One of `socks5://`, `socks5h://`, `http://`, `https://`, optionally with `user:password@` credentials. |
| `subscriptions` | array | `[]` | Provider subscriptions: objects with `name`, `url` (secret!) and optional `via` (a per-subscription fetch proxy, same scheme rules as `fetch.proxy`). Names must be unique and non-empty. |
| `accounts` | array | `[]` | Proxy accounts: objects with required unique `login` (1–64 ASCII letters, digits, `.`, `_`, `@`, `-`), required `password` (`init` or argon2id PHC hash), and optional `exit_country` (two ASCII letters, normalized uppercase). |
| `selection.node` | string | *(empty)* | Preferred exit node name (set by `netivri use <node>`). |
| `selection.country` | string | *(empty)* | Preferred exit country code (set by `netivri use --country CC`). |
| `selection.mode` | string | `manual` | How the exit is chosen: `manual` (explicit node), `fastest` (lowest latency), `auto` (re-check periodically). |

`accounts` is separate from `auth`; either or both may be configured. Account logins are unique, and cannot collide with a non-`init` `auth.login`. The reserved login `init` cannot be used for an account. `exit_country` may be omitted or empty to use the default route; codes are normalized to uppercase when loaded. Shape validation checks two ASCII letters, not whether that code represents a real country.

## The `init` placeholder

Writing `auth.login: init` / `auth.password: init` means "the first
local client that authenticates fills the field in": the daemon hashes
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

auth.login:: init
auth.password:: init
auth.allow_init_remote: false

## All subscription fetches go through this proxy.
## fetch.proxy:: socks5://user:secret@proxy.example:1080

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
        exit_country: DE
    }
]

selection.node::
selection.country::
selection.mode: manual
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

## Native library

Reading and writing the file uses the Ktav reference parser, a native
library (`ktav_cabi.dll` / `libktav_cabi.so` / `libktav_cabi.dylib`)
shipped next to the `netivri` binary. Before the first parse, netivri
checks `$KTAV_LIB_PATH` or the canonical name next to the executable
and pins it; if it is missing, netivri fails with the error code
`KTAV_LIB_MISSING` instead of downloading anything behind your back.

## Redaction

Subscription URLs contain secrets and are never printed in full. Logs,
errors and status output show them redacted: scheme + host + `/****`
(path and query dropped). Proxy URLs with credentials keep only the
username with the password masked: `socks5://user:****@host:port`.
Parse errors are reported only from the parser's structured fields
(error class, reason, line number, spec section, key path) and never
contain document text, so passwords and URLs cannot leak through them.
