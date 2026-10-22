# Licenses

netivri is `GPL-3.0-only`, inherited from Mihomo and `sing`, which are linked
into the binary (see `README.md`).

## Why GPL-3.0-only and not MIT or Apache-2.0

The netivri binary is one program that contains Mihomo, the `sing*` modules
and `go-lzo`. Mihomo and `sing*` are GPL-3.0 and `go-lzo` is
GPL-2.0-or-later. A program built from GPL code may be distributed only under
the GPL, so the binary, the release archives and the npm packages are
`GPL-3.0-only`. Nothing the author of netivri decides can change that while
the program links this code.

The compatibility only works in one direction. MIT, BSD, ISC and Apache-2.0
code may be included in a GPL-3.0 program (that is why the dependencies below
are allowed), but a GPL-3.0 program cannot be handed out as MIT or Apache-2.0.
Apache-2.0 is compatible with GPL-3.0 (not with GPL-2.0-only).

Splitting the repository (our own files under MIT, `third_party/` under GPL)
is not done. Our code is written against Mihomo's API and is useless without
it, so the split would only suggest that the binary is permissive when it is
not.

The way to a permissive license is to stop linking GPL code: run Mihomo as a
separate process (or replace the core) and talk to it over a protocol. That is
a different architecture from the one in `AGENTS.md` (Mihomo as a library, one
binary), so it is not planned. If it ever happens, every module in the table
below and the whole dependency list must be audited again first.

## CI check

`scripts/check-licenses.sh` holds the whole policy. The `licenses` job of
`ci.yml` runs it on every push and pull request, and `release.yml` runs it
before anything is published.

1. `go-licenses check` with an allow-list: `GPL-3.0`, `LGPL-3.0`, `MIT`,
   `Apache-2.0`, `BSD-2-Clause`, `BSD-3-Clause`, `ISC`, `MPL-2.0`,
   `Unlicense`, `CC0-1.0`, `BlueOak-1.0.0`. All of them can be combined into a
   `GPL-3.0` work. A new failure needs human review; never extend the list or
   the ignores just to make the job green.
2. The places that declare netivri's own license must agree: `LICENSE` and
   `third_party/mihomo/LICENSE` are the GPL v3 text, the npm `package.json`
   and the package build script say `GPL-3.0-only`, and the README states it.

Dependabot (`.github/dependabot.yml`) opens weekly pull requests for Go
modules and GitHub Actions; the license check runs on them like on any change.

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

## Vendored Mihomo

Mihomo `v1.19.32` is vendored in `third_party/mihomo` (GPL-3.0, its
`LICENSE` is part of the tree) so that netivri can patch its adapters; see
`third_party/README.md` for the rules and the list of patches. Modified files
carry a notice. The license check, the notices in the release archives and
the audited exceptions above are computed from the vendored module as before.

## Notices in release archives

MIT, BSD and Apache-2.0 require the license text to travel with binaries.
`scripts/collect-licenses.sh` copies the `LICENSE`/`NOTICE`/`COPYING` files of
every third-party Go module linked into `./cmd/netivri` (per target
GOOS/GOARCH) into `THIRD_PARTY_LICENSES/{MODULE_PATH}/`; a module without a
root license file contributes the license files found in its subdirectories. The release workflow
puts that directory into every archive. The script fails when a module has no
license file, and the `licenses` CI job runs it on linux/amd64 on every push.

The Ktav native library (`ktav_cabi`) is `MIT OR Apache-2.0`. It statically
links Rust crates (`ktav`, `serde`, `serde_json`, `indexmap`, all permissive);
their notices are not enumerated by the Go tooling and are not shipped yet.
