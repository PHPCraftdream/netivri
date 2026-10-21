# npm distribution

`npm i -g netivri` installs a prebuilt netivri binary. The packages are
repackaged release artifacts, not rebuilds; no compiler runs on the user's
machine, and there are **no postinstall scripts and no downloads at install
time** — npm's own registry connection (including any proxy configured for
npm) is the only network path.

## Layout

- `netivri` — the main package: a small dependency-free CommonJS launcher
  (`bin/netivri.js`). It maps `process.platform` / `process.arch` (plus a
  glibc/musl check on Linux via `process.report`) to a platform package,
  resolves the binary with `require.resolve`, and spawns it with
  `stdio: 'inherit'`, forwarding SIGINT/SIGTERM/SIGHUP and the exit code.
- `netivri-<os>-<cpu>` — six platform packages (`linux-x64`,
  `linux-arm64`, `darwin-x64`, `darwin-arm64`, `win32-x64`,
  `win32-arm64`) installed as `optionalDependencies` of the main package.
  Each declares `os`, `cpu` and (on Linux) `libc: ["glibc"]`, so npm
  downloads exactly one. Each contains `bin/netivri[.exe]`, the Ktav native
  library under its canonical name next to the binary (required by
  `internal/config/ktavlib.go`), `LICENSE` and `THIRD_PARTY_LICENSES/`.

If the platform is unsupported, or the platform package is missing (e.g.
installed with `--omit=optional` or `--no-optional`), the launcher prints
one JSON error envelope on stderr (code `INSTALL_BROKEN`) and exits 1,
matching the CLI contract in [`cli.md`](cli.md).

## musl / Alpine limitation

The Linux platform packages declare `libc: ["glibc"]`, so npm skips them on
musl-based systems such as Alpine. The Ktav binding needs glibc 2.17+, so
Alpine/musl is unsupported for every distribution channel — the npm
packages and the standalone installer alike. Use a glibc-based
distribution or a glibc container.

## Relation to the release archives

The platform packages are built from the same archives attached to each
GitHub Release (`netivri_<version>_<goos>_<goarch>.tar.gz|.zip`, see
[`release.md`](release.md)). The build step extracts the archives and runs:

```sh
node npm/scripts/build-packages.js \
  --version X.Y.Z --archive-dir <dir-with-extracted-archives> --out <out-dir>
```

The script validates that each archive contains the expected binary and
Ktav library, substitutes the version into every `package.json`, copies
`LICENSE` and `THIRD_PARTY_LICENSES/` into each package, preserves the
executable bit, writes only under `--out`, and prints a JSON summary.

## Publishing

The `npm-publish` job of the release workflow downloads the packed tarballs
built from the release archives and runs `scripts/npm-publish.sh`: the six
platform packages first, the main package last, so the
`optionalDependencies` versions always resolve. Package versions are the git
tag without the leading `v`. Authentication is an `NPM_TOKEN` secret or, with
no secret, npm Trusted Publishing (OIDC); both attach provenance. How to
switch from the token to Trusted Publishing is in
[`release.md`](release.md#publishing-to-npm-token-or-trusted-publishing).

## Planned: stable system copy

Not implemented yet. The future npm installer will call the standalone
installer in local-source mode, e.g.:

```sh
sh install.sh --from-dir <platform package dir> --dir <stable dir> --quiet
```

```powershell
powershell -ExecutionPolicy Bypass -File install.ps1 -FromDir <platform package dir> -InstallDir <stable dir> -Quiet
```

That copies the binary out of the npm tree into a stable directory
(`--from-dir` verifies no checksum, because npm's own registry integrity
check already covered the download). The launcher would run the stable
copy, so replacing it is a rename that a running daemon never blocks —
`npm update` stays safe even while netivri is up (see
[install.md](install.md), "Upgrading while netivri is running").

## Update

```sh
npm update -g netivri
# or
npm i -g netivri@latest
```

## Uninstall

```sh
npm rm -g netivri
```

Removing the main package removes the platform package installed for your
platform. No other files are written outside the npm global tree.
