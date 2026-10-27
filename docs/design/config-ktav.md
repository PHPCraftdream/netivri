# Config format: Ktav 0.8

## Decision

Local settings are stored in [Ktav](https://github.com/ktav-lang/spec) 0.8,
read and written with the official Go binding
[`github.com/ktav-lang/golang`](https://github.com/ktav-lang/golang) v0.8.1,
used as is. No own parser.

License: the binding is `MIT OR Apache-2.0`, its dependencies are permissive;
all are compatible with the project's `GPL-3.0-only`.

## How the binding works (constraints we design around)

The binding has two backends: the reference Rust library (`ktav_cabi`, loaded
through `purego`, downloaded from GitHub Releases on first use when it is not
found) and a self-contained pure-Go one. netivri uses the pure-Go backend only,
and imports only its subpackage `github.com/ktav-lang/golang/pure`, never the
root package `github.com/ktav-lang/golang`: the root package links the
native-library loader (`purego`, which brings `cgo_import_dynamic`), and that
alone turns the Linux binary, even with `CGO_ENABLED=0`, into a dynamically
linked one with the interpreter `/lib64/ld-linux-x86-64.so.2`, which does not
exist on musl (Alpine). `TestNoPackageImportsTheKtavRootPackage` fails on such an
import, and the release workflow checks that the Linux binary is "statically
linked"; the binding's `purego` is no longer in `go.mod`.

Consequences:

- **Nothing to load, download or ship.** The pure backend never loads a shared
  library, never downloads anything and never spawns a process, so the binding
  cannot make a connection of its own and cannot bypass the fetch proxy rule
  (all outgoing fetches go through the configured proxy, see subscriptions).
  The Go binary stays `CGO_ENABLED=0` and is the only file of a release; there
  is no `ktav_cabi`, no `KTAV_LIB_PATH`, and no way for a missing library to
  break a command.
- **The same results.** The pure backend passes the whole conformance corpus of
  the pinned spec (valid, invalid, unrepresentable, parseable-unrepresentable,
  strict-lossy). On the configuration and the stored subscription snapshots of
  a real installation it gave the same parsed values, the same rendered text
  and the same round trip as the native library.
- Only the typed decode (`LoadsInto`) and the rendering of arbitrary structs
  go through `encoding/json` on the Go side, as before.
- Version pin: the Go module version (0.8.1) selects the spec the binding
  implements.

## Usage in netivri

- Read: `pure.LoadsInto(src, &cfg)` into typed structs.
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

- Config and subscription snapshots load and save with `KTAV_LIB_PATH` pointing
  at nothing: no library is involved.
- Round trip: netivri renders with `Dumps`, parses back with `LoadsStrict`, decodes onto the defaults and requires deep equality before writing; comments are regenerated, not preserved.
