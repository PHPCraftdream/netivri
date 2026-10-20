# Daemon mode and control channel

Design of `netivri up` (background), `netivri down` and `netivri status`.

## Why a loopback HTTP control server

The control channel is an HTTP server bound to `127.0.0.1:0` (random port),
guarded by a bearer token: one code path on all three operating systems, no
named-pipe/Unix-socket abstraction, no new dependency, and testable with
plain `net/http`. The listener is independent of the proxy bind address: the
proxy may be bound wherever the user asked, the control server never leaves
loopback.

Guard rails:

- Every request must carry `Authorization: Bearer <token>`; the comparison
  runs through `crypto/subtle.ConstantTimeCompare`.
- The `Host` header must equal the listener's own `127.0.0.1:<port>`; a
  mismatching host (DNS-rebinding style) gets 403.
- Wrong method: 405. Unknown path: 404. Missing/wrong token: 401, with no
  information about why.
- Request bodies are capped (`http.MaxBytesReader`); the `http.Server` sets
  read/header/write/idle timeouts.
- Endpoints: `GET /v1/status` (live status JSON) and `POST /v1/stop`
  (answers 202 `{"stopping":true}`, then triggers shutdown exactly once via
  `sync.Once`; repeated calls still answer 202). No CORS, nothing else.

## State files

Under the resolved home:

- `run/netivri.json` — state file, written atomically (temp file + rename,
  mode `0600`; the `run` directory is `0700`; on Windows the file inherits
  the profile directory ACL). Content (snake_case): `pid`, `listen`,
  `control`, `token` (32 random bytes, hex, from `crypto/rand`),
  `foreground`, `started_at` (RFC3339 UTC), `version`, `auth`,
  `protocols`, `route`. The token is a secret: it never appears on stdout,
  stderr, errors, logs or test output.
- `run/netivri.log` — daemon log, append-only, `0600`. The detached child's
  stdout/stderr are redirected here; lifecycle lines are logged only by
  that child (foreground mode has no lifecycle stderr output). It is not rotated.

Stale handling: `Probe` requires state and a live control answer from the
same pid; `ProbeLive` also returns the authenticated live status used by
`status` and idempotent `up`. Corrupt-state deletion is best effort: atomic
replacement avoids partial writes, but a narrow race can still remove a
successor's replacement between checking and deleting. Matching pid/token
protects normal stale cleanup and shutdown from deleting a successor.

Status JSON may include additive `accounts`, an omitted-when-empty array of
`{"login":"alice","exit_country":"DE"}` records. It contains neither
password hashes nor other credentials; live status sorts records by login.

## The handshake

The daemon parent spawns the same executable
(`os.Executable()`) with the user's `up` arguments (verbatim, minus the
compatibility flags, with `--home` normalized) plus
`--foreground --handshake <path>`. The child:

1. starts the proxy and the control server,
2. writes the state file,
3. writes the handshake file: `{"ok":true,"state":{...}}`, or
   `{"ok":false,"error":{"code","message","hint","exit"}}` for a startup
   failure (a `PORT_IN_USE` keeps exit 6, and so on),
4. removes nothing that is not its own.

The parent polls the unique file `run/start-<random-hex>.json` (50 ms tick,
15 s timeout). The child writes the success or failure JSON atomically. On
child exit, the parent makes one final read to catch a last-moment handshake.
The child is started detached (`DETACHED_PROCESS | CREATE_NEW_PROCESS_GROUP`,
hidden window on Windows; `Setsid` elsewhere), with stdin closed and
stdout/stderr on the log file.

## Shutdown order

`netivri down` sends `POST /v1/stop`; the 202 response is flushed before the
once-only shutdown callback runs. It then polls `Probe` (up to 15 s) until
the state is gone. The daemon stops the proxy listener, closes the tracked
connections, gracefully shuts the control server down with a two-second
context (forcing Close if shutdown fails), removes its state file only when
still its own, and logs stop only in detached mode. Foreground mode emits no
lifecycle stderr output.

## Security notes

- Control server: loopback only, bearer token, Host pinning, body limit,
  timeouts (see above). The token lives only in `run/netivri.json`
  (`0600` on Unix).
- The log file carries no tokens, credentials or subscription URLs.
- The proxy port itself stays protected by the existing rules
  (loopback default, `AUTH_REQUIRED` for non-loopback binds).

## Known limits

- Two simultaneous starts with DIFFERENT explicit ports in one home can
  race on the state file (last writer wins). The proxy port itself is the
  real mutex for equal ports. `netivri use`-driven reconfiguration will
  make the state file authoritative.
- The daemon log is not rotated.
- Corrupt-state deletion is best-effort and can race an atomic successor
  replacement between the read and remove; callers cannot eliminate this
  pathname TOCTOU without stronger filesystem coordination.
- On Windows the daemon receives no signals: it stops only via
  `netivri down` (or process kill).
- The foreground `--handshake` flag is internal and not part of the CLI
  contract (docs/cli.md).
