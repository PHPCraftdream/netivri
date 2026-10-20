# Vendored third-party sources

## mihomo

- Upstream: <https://github.com/MetaCubeX/mihomo>, module
  `github.com/metacubex/mihomo`, version `v1.19.32`.
- Wired in by `replace github.com/metacubex/mihomo => ./third_party/mihomo`
  in `go.mod`; the `require` line keeps the upstream version.
- License: GPL-3.0 (`third_party/mihomo/LICENSE`), the same as netivri.
- Why vendored: netivri needs changes inside Mihomo's outbound adapters that
  the public API does not offer (see `docs/design/latency.md`).

### Rules

- The import commit is pristine: the tree equals the module `v1.19.32` as
  published (`diff -r` against the Go module cache is empty). It is stored
  byte for byte (`.gitattributes` disables line-ending conversion).
- Every change to an upstream file is its own commit, listed under "Patches"
  below with its reason. New code goes into new files named `netivri_*.go`
  in the same package; edits to upstream files stay minimal.
- Each modified upstream file carries a short notice at the top, as GPL-3.0
  section 5(a) asks: `// Modified by the netivri authors: <what and why>.`
- `gofmt` is not run on this directory and Mihomo's own tests are not run.

### Updating to a new upstream version

1. Import the new tag pristine over the directory in one commit.
2. Re-apply the patch commits listed below (cherry-pick, resolve conflicts).
3. Change the version here and in `go.mod`, run `go mod tidy`, then run the
   full CI and the release dry run.

### Patches

None yet.
