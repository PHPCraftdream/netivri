# npm distribution

`npm i -g @netivri/proxy` installs a prebuilt netivri binary. The packages are
repackaged release artifacts, not rebuilds; no compiler runs on the user's
machine, and there are **no postinstall scripts and no downloads at install
time** — npm's own registry connection (including any proxy configured for
npm) is the only network path.

## Layout

- `@netivri/proxy` — the main package: a small dependency-free CommonJS launcher
  (`bin/netivri.js`). It maps `process.platform` / `process.arch` (plus a
  to a platform package,
  resolves the binary with `require.resolve`, and spawns it with
  `stdio: 'inherit'`, forwarding SIGINT/SIGTERM/SIGHUP and the exit code.
- `@netivri/proxy-{OS}-{CPU}` — six platform packages (`linux-x64`,
  `linux-arm64`, `darwin-x64`, `darwin-arm64`, `w32-x64`,
  `w32-arm64`) installed as `optionalDependencies` of the main package.
  Each declares `os` and `cpu` (no `libc`: the Linux binary is static, so
  glibc and musl hosts get the same package), so npm downloads exactly one. Each contains `bin/netivri[.exe]` (a single static
  file), `LICENSE` and `THIRD_PARTY_LICENSES/`.

All seven packages are published in the `@netivri` npm organization, which keeps the
platform packages out of the unscoped namespace. The platform packages carry no
keywords and a description that points to the main package, so they do not
show up in `npm search` next to it; each package has
`publishConfig.access: public`, which a scoped package needs to be public.

If the platform is unsupported, or the platform package is missing (e.g.
installed with `--omit=optional` or `--no-optional`), the launcher prints
one JSON error envelope on stderr (code `INSTALL_BROKEN`) and exits 1,
matching the CLI contract in [`cli.md`](cli.md).

## musl / Alpine

The Linux binary is a static Go build (`CGO_ENABLED=0`; the release workflow
checks that `file` reports "statically linked"), so there are no separate musl
packages: `@netivri/proxy-linux-x64` and `-linux-arm64` carry no `libc`
field and install on glibc and musl hosts alike. The `alpine test` job of the
release workflow installs the packed packages with `node:20-alpine` (amd64 and
arm64) and runs the launcher before anything is published.

## Relation to the release archives

The platform packages are built from the same archives attached to each
GitHub Release (`netivri_{VERSION}_{GOOS}_{GOARCH}.tar.gz|.zip`, see
[`release.md`](release.md)). The build step extracts the archives and runs:

```sh
node npm/scripts/build-packages.js \
  --version X.Y.Z --archive-dir {DIR_WITH_EXTRACTED_ARCHIVES} --out {OUT_DIR}
```

The script validates that each archive contains the expected binary,
substitutes the version into every `package.json`, copies
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
sh install.sh --from-dir {PLATFORM_PACKAGE_DIR} --dir {STABLE_DIR} --quiet
```

```powershell
powershell -ExecutionPolicy Bypass -File install.ps1 -FromDir {PLATFORM_PACKAGE_DIR} -InstallDir {STABLE_DIR} -Quiet
```

That copies the binary out of the npm tree into a stable directory
(`--from-dir` verifies no checksum, because npm's own registry integrity
check already covered the download). The launcher would run the stable
copy, so replacing it is a rename that a running angel never blocks —
`npm update` stays safe even while netivri is up (see
[install.md](install.md), "Upgrading while netivri is running").

## Update

```sh
npm update -g @netivri/proxy
# or
npm i -g @netivri/proxy@latest
```

## Uninstall

```sh
npm rm -g @netivri/proxy
```

Removing the main package removes the platform package installed for your
platform. No other files are written outside the npm global tree.
