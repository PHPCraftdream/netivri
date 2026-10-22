# Config format: Ktav 0.8.0

## Decision

Local settings are stored in [Ktav](https://github.com/ktav-lang/spec) 0.8.0,
read and written with the official Go binding
[`github.com/ktav-lang/golang`](https://github.com/ktav-lang/golang) v0.8.0,
used as is. No own parser.

License: the binding is `MIT OR Apache-2.0`, its dependencies (`purego`,
`golang.org/x/sys`) are permissive; all are compatible with the project's
`GPL-3.0-only`.

## How the binding works (constraints we design around)

The binding is `purego` over the reference Rust library (`ktav_cabi`). It loads
the native library at the first call, resolving it in this order:

1. `$KTAV_LIB_PATH`
2. user cache `{USER_CACHE_DIR}/ktav-go/v{VERSION}/`
3. download from GitHub Releases (needs network on first call)

Consequences:

- **No hidden downloads.** Step 3 is a direct connection to github.com. It
  would bypass the fetch proxy rule (all outgoing fetches go through the
  configured proxy, see subscriptions). netivri therefore ships the native
  library with every distribution (npm platform packages, standalone installer,
  release archives) and sets `KTAV_LIB_PATH` in-process before the first call.
  If the library is missing, fail with a clear error; never fall through to
  the download.
- **Release plan.** The Go binary itself stays `CGO_ENABLED=0`. Archives and
  packages carry the native library next to the binary:
  `ktav_cabi.dll` / `libktav_cabi.so` / `libktav_cabi.dylib` for the
  platforms the binding supports (linux/darwin/windows × amd64/arm64).
  glibc 2.17+ on Linux; musl (Alpine) is not supported by the binding yet.
- Version pin: the library version must match the Go module version (0.8.0).

## Usage in netivri

- Read: `ktav.LoadsInto(src, &cfg)` into typed structs.
- Write-back (e.g. the password hash after `init`): whole-file
  regeneration via `Dumps` over the config struct (deterministic field
  order) plus a generated header comment, with a mandatory round-trip
  check: the rendered text is parsed back with `LoadsStrict` and
  decoded onto the defaults, and must be deeply equal to the input
  before anything is written. The earlier design — a comment-preserving
  targeted edit via `FormatSource` — was dropped: it required a
  structural editor (scope tracking, key-path matching, raw-marker
  rules) that proved error-prone without the ability to run it
  locally. Comments are not preserved across rewrites; the header
  comment says so.
- Strict parsing (`LoadsStrict`) for the config file, so lossy numeric
  spellings are rejected instead of silently coerced.
- Atomic save: temp file in the same directory + rename; file readable by the
  current user only.

## To verify in CI

- Start with `KTAV_LIB_PATH` set and the network blocked: config loads, no
  outgoing connection is made.
- Library missing → clear error, still no connection.
- Round trip: netivri renders with `Dumps`, parses back with `LoadsStrict`, decodes onto the defaults and requires deep equality before writing; comments are regenerated, not preserved.
