# Release process

## Cutting a release

1. Make sure `main` is green in CI.
2. Tag and push: `git tag vX.Y.Z`, then push the tag to `origin`.
3. The `release` workflow builds six archives (windows/linux/darwin x
   amd64/arm64), generates `SHA256SUMS`, and publishes a GitHub Release
   containing all archives and the checksum file.

The version embedded in the binary (`netivri status` shows it) is the tag
name without the leading `v`.

## Dry run

Dispatch the workflow manually (`workflow_dispatch`) to build and upload
artifacts **without** publishing a release or attesting provenance. The
version is `0.0.0-dryrun`. Use it to check packaging before tagging.

## What each archive contains

`netivri_<version>_<goos>_<goarch>.zip` (windows) or `.tar.gz` (others):

- `netivri` / `netivri.exe` — the Go binary, built with `CGO_ENABLED=0`.
- The Ktav native library (`ktav_cabi.dll`, `libktav_cabi.so` or
  `libktav_cabi.dylib`) next to the binary.
- `LICENSE`, `README.md`.
- `THIRD_PARTY_LICENSES/` — license texts of the linked Go modules (see
  `docs/licenses.md`).

The native library ships with every archive because the Ktav Go binding
resolves `$KTAV_LIB_PATH` first, and netivri sets that in-process before the
first config call. If the library were missing, the binding would fall back
to downloading it from GitHub — a hidden, unproxied network fetch that netivri
must never do (see `docs/design/config-ktav.md`). Library version 0.8.0 is
pinned to match the Go module, and verified against the SHA256 list in
`scripts/ktav-lib.sha256` at build time.

## Verifying checksums

```sh
sha256sum -c SHA256SUMS
```

inside the directory holding the archives and the checksum file.

## Consumers

The npm platform packages and the standalone installer (later tasks) consume
these same archives; they repackage, they do not rebuild.

## Known limitation

musl-based Linux distributions such as Alpine are not supported by the
Ktav binding. Linux needs glibc 2.17+.
