# Licenses

netivri is `GPL-3.0-only`, inherited from Mihomo and `sing`, which are linked
into the binary (see `README.md`).

## CI check

The `licenses` job runs `go-licenses check` on every push with an allow-list:
`GPL-3.0`, `LGPL-3.0`, `MIT`, `Apache-2.0`, `BSD-2-Clause`, `BSD-3-Clause`,
`ISC`, `MPL-2.0`, `Unlicense`, `CC0-1.0`, `BlueOak-1.0.0`. All of them can be
combined into a `GPL-3.0` work. A new failure needs human review; never
extend the list or the ignores just to make the job green.

## Audited exceptions

Checked on the first CI run against Mihomo v1.19.32 (module versions as pinned
in `go.mod`). Re-check an entry whenever the module version changes.

| Module | Why `go-licenses` objects | Actual terms | Decision |
|---|---|---|---|
| `github.com/Yawning/aez` | `CC0-1.0` not in the original list | CC0 1.0 public-domain dedication, GPL-compatible | added `CC0-1.0` to the allow-list |
| `github.com/easytier/easytier/easytier-go` | `LGPL-3.0` not in the original list | LGPL-3.0, may be combined into a GPL-3.0 work | added `LGPL-3.0` to the allow-list |
| `github.com/metacubex/chacha` | license not classified (empty) | at the pinned v0.1.5 there is no root license file; `chachapoly1305/LICENSE` and `poly1305/LICENSE` are MIT (Andreas Auernhammer) and the `chacha` package is his MIT-licensed code (its header points to a `LICENSE` file); later versions add a root MIT `LICENSE` | `--ignore`; the subdirectory MIT texts are shipped in the notices |
| `github.com/rasky/go-lzo` | `GPL-2.0` | `LICENSE.gpl` is the GPLv2 text, the README states `GPL-2.0-or-later` (it is a port of LZO, which is GPL-2.0-or-later); no per-file headers | `--ignore`; `or-later` allows combining into GPL-3.0. Blanket `GPL-2.0` is deliberately not allowed, it could be `-only` elsewhere |

| `modernc.org/mathutil` | license not classified (a dependency of the SQLite driver `modernc.org/sqlite`) | the root `LICENSE` of v1.7.1 is the standard BSD-3-Clause text ("The mathutil Authors", three conditions plus the usual disclaimer); the classifier does not match its wording | `--ignore`; the `LICENSE` is shipped in the notices |

Mihomo itself bundles `go-lzo` under `GPL-3.0`, so netivri inherits the same
position. If the author of `go-lzo` ever confirms `GPL-2.0-only`, drop the
dependency path (or the feature that pulls it) instead of widening the list.

## Notices in release archives

MIT, BSD and Apache-2.0 require the license text to travel with binaries.
`scripts/collect-licenses.sh` copies the `LICENSE`/`NOTICE`/`COPYING` files of
every third-party Go module linked into `./cmd/netivri` (per target
GOOS/GOARCH) into `THIRD_PARTY_LICENSES/<module path>/`; a module without a
root license file contributes the license files found in its subdirectories. The release workflow
puts that directory into every archive. The script fails when a module has no
license file, and the `licenses` CI job runs it on linux/amd64 on every push.

The Ktav native library (`ktav_cabi`) is `MIT OR Apache-2.0`. It statically
links Rust crates (`ktav`, `serde`, `serde_json`, `indexmap`, all permissive);
their notices are not enumerated by the Go tooling and are not shipped yet.
