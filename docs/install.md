# Standalone installation (no npm)

The standalone installers download a prebuilt release archive, verify its
SHA256 checksum, and put the `netivri` binary (a single file) in a user
directory. They never rebuild anything, never
use `sudo`, never touch your shell profile, and never prompt.

With Node.js 18 or newer you can install the same binary through npm
instead:

```sh
npm i -g netivri
```

npm picks the package of your platform (`netivri-linux-x64`,
`netivri-darwin-arm64`, `netivri-w32-x64`, ...) by itself; how the packages
are laid out and published is in [docs/npm.md](npm.md). Upgrade with
`npm i -g netivri@latest`, remove with `npm rm -g netivri`.

## One-liners

macOS / Linux (any POSIX `sh`, including dash and ash):

```sh
curl -fsSL https://raw.githubusercontent.com/PHPCraftdream/netivri/main/install.sh | sh
```

Windows PowerShell:

```powershell
iwr -useb https://raw.githubusercontent.com/PHPCraftdream/netivri/main/install.ps1 | iex
```

Piping a script from the network into a shell runs code you have not read.
The safer variants download the script first, inspect it, and run it:

```sh
curl -fsSLO https://raw.githubusercontent.com/PHPCraftdream/netivri/main/install.sh
sh install.sh
```

```powershell
iwr -useb https://raw.githubusercontent.com/PHPCraftdream/netivri/main/install.ps1 -OutFile install.ps1
powershell -ExecutionPolicy Bypass -File install.ps1
```

To verify the installer script itself, fetch it over two independent
channels (e.g. `curl` and `git clone` of the tagged release) and `diff`
them, or audit it by hand — it is short and has no dependencies beyond
curl/wget, tar, sha256sum/shasum (POSIX) and built-in PowerShell cmdlets.

## From the release page

Every release also carries, next to the archives and `SHA256SUMS` (all of
them listed there and covered by the build provenance):

- `install.sh` and `install.ps1` — the installers of that tag. They are the
  safest way to run the script that matches the version:
  `curl -fsSL https://github.com/PHPCraftdream/netivri/releases/download/v{VERSION}/install.sh | sh`
  and `iwr -useb https://github.com/PHPCraftdream/netivri/releases/download/v{VERSION}/install.ps1 | iex`.
  (`releases/latest/download/install.sh` always points at the newest one.)
- `netivri_{VERSION}_linux_{ARCH}.deb` and `.rpm` (amd64 and arm64): the binary
  in `/usr/bin` and the licenses under `/usr/share/doc/netivri`. Install with
  `sudo dpkg -i` or `sudo rpm -i`; remove with `dpkg -r netivri` or
  `rpm -e netivri`. The amd64 packages are installed and removed again by the
  release workflow before anything is published.
- `netivri.json` — a Scoop manifest: `scoop install https://github.com/PHPCraftdream/netivri/releases/download/v{VERSION}/netivri.json`.
- `netivri.rb` — a Homebrew formula for macOS and Linux. Put it into a tap of
  your own (`brew tap-new {USER}/netivri`, copy the file to its `Formula`
  directory, `brew install {USER}/netivri/netivri`); Homebrew no longer
  installs a formula straight from a file.
- `netivri-{VERSION}.tgz` and `netivri-{PLATFORM}-{VERSION}.tgz` — the npm
  packages, for an install without the registry:
  `npm i -g netivri-{PLATFORM}-{VERSION}.tgz netivri-{VERSION}.tgz` (the first
  file is the package of your platform, for example `netivri-linux-x64`; the
  Windows ones are `netivri-w32-x64` and `netivri-w32-arm64`).

The Scoop manifest and the Homebrew formula are written from the archives of
the release and checked for syntax; they are not installed by the workflow.
The `.deb`, `.rpm` and npm tarball installs are.

## Supported platforms

| OS | Architectures | Notes |
|---|---|---|
| Linux | amd64, arm64 | glibc distributions |
| macOS (darwin) | amd64, arm64 | |
| Windows | amd64, arm64 | PowerShell 5.1 or 7 |

**Alpine and every other musl-based distribution are not supported.** The
Linux release is built and tested for glibc only; the installer detects musl
(`ldd --version` or `/etc/alpine-release`) and aborts with a clear message.
See [docs/release.md](release.md).

## Options

Both installers take the same options (POSIX names / PowerShell parameters):

| install.sh | install.ps1 | Env variable | Default | Meaning |
|---|---|---|---|---|
| `--version X.Y.Z` | `-Version X.Y.Z` | `NETIVRI_VERSION` | latest release | Version to install (no leading `v`). |
| `--dir {DIR}` | `-InstallDir {DIR}` | `NETIVRI_INSTALL_DIR` | `~/.netivri/bin` / `%LOCALAPPDATA%\netivri\bin` | Install directory. |
| `--base-url {URL}` | `-BaseUrl {URL}` | `NETIVRI_BASE_URL` | GitHub releases | Download base URL (see below). |
| `--from-dir {DIR}` | `-FromDir {DIR}` | `NETIVRI_FROM_DIR` | — | Install from a local directory instead of downloading (see below). |
| — | `-Proxy {URL}` | — | system proxy | HTTP(S) proxy for `Invoke-WebRequest`. |
| — | `-AddToPath` | — | off | Add the install dir to the **user** `PATH`. |
| `--quiet` | `-Quiet` | — | off | Print nothing informational (progress, results, notes, PATH hints); errors still go to stderr. |
| `--uninstall` | `-Uninstall` | — | — | Remove the installed files. |
| `--help` | `-Help` | — | — | Show help. |

Exit codes: `0` success, `1` failure, `2` usage error.

`--base-url` / `-BaseUrl` accepts:

- `https://…` / `http://…` — the archive and `SHA256SUMS` are fetched from
  `{BASE}/{FILE}` directly, without the release-tag path (useful for
  mirrors and CI);
- `file://…` or a plain local directory path — the files are copied from
  `{BASE}/{FILE}`.

When `--base-url` / `-BaseUrl` is used, `--version` / `-Version` must be
given too, because the archive filename contains the version. The default
version is resolved from the `releases/latest` redirect (an HTTPS HEAD
request, no API call, no rate limit).

## Upgrading while netivri is running

The installer never stops a running angel. Instead of overwriting files
in place, it replaces them by renaming, which is safe while netivri runs:

- **Linux / macOS:** the new file is copied to a temp name in the install
  directory and `mv -f`'d over the target. The rename is atomic; the
  running process keeps executing the old file (it holds the old inode,
  even for the mapped binary). If a netivri
  process runs from the install directory, one note is printed
  (unless `--quiet`):
  `note: netivri is running (pid N); it keeps using the previous version
  until restarted: netivri down && netivri up`. The detection is
  best effort (`pgrep` plus `/proc/{PID}/exe` on Linux) and never fails
  the install.
- **Windows:** a running executable or a mapped DLL cannot be overwritten
  or deleted, but it can be renamed. The installer copies the new file
  next to the target, renames the old file aside to
  `{NAME}.old-{TIMESTAMP}`, moves the new copy into place, and then tries
  to delete the renamed-aside file — a file still held open by the running
  angel simply stays. If the install directory would ever be left without
  a working binary, the old file is renamed back and the install fails.
  Leftover `netivri.exe.old-*` files (and `ktav_cabi.dll.old-*` files of
  earlier builds) are cleaned up by the next install or uninstall; when
  some are still locked the
  installer prints once:
  `note: N old file(s) are still in use and will be removed by the next
  install or uninstall`.

`--uninstall` / `-Uninstall` on Windows renames locked files aside too,
so the uninstall never fails just because the angel is running; the
renamed-aside files disappear on a later install or uninstall.

## Installing from a local directory

`--from-dir {DIR}` (env `NETIVRI_FROM_DIR`) installs from a local directory
instead of downloading. There is no network access and no checksum
verification — the source is an ordinary local file, and when the npm
installer uses this mode npm's own registry integrity check already
covers the download. Two layouts are accepted:

- **Archive layout** — the extracted release archive: the binary
  (`netivri` / `netivri.exe`), `LICENSE` and `THIRD_PARTY_LICENSES/` sit
  directly in `{DIR}`.
- **npm platform package layout** — `{DIR}/bin/` holds the binary;
  `LICENSE` and `THIRD_PARTY_LICENSES/` sit in `{DIR}`.

The binary and the library must exist (the install fails otherwise);
`LICENSE` and `THIRD_PARTY_LICENSES/` are copied when present and silently
skipped otherwise. `--from-dir` cannot be combined with `--version` or
`--base-url` (or their env variables) — that is a usage error. In this
mode the final line is `installed netivri to {DIR}` (the version is
unknown). Everything else — the running-angel handling above, the share
files, the PATH hints — is identical to download mode.

Use `--quiet` / `-Quiet` when calling the installer from another tool
(e.g. the npm installer): it suppresses all informational output and only
errors reach stderr, with unchanged exit codes.

## How verification works

1. The archive (`netivri_{VERSION}_{OS}_{ARCH}.tar.gz` or `.zip`) and the
   release's `SHA256SUMS` file are downloaded.
2. The SHA256 of the archive is computed locally and compared with the
   entry for that exact filename in `SHA256SUMS`.
3. A missing entry or any mismatch aborts the install **before** anything
   is extracted into the install directory.
4. Only then is the archive extracted into a private temporary directory
   (removed on exit), and the files are copied into place via temp names
   and renamed, so a crash cannot leave a half-written binary.

Checksum verification is mandatory and cannot be skipped by any flag
(`--from-dir` bypasses the network entirely, and with it the checksum).

## What gets installed

```
{DIR}/netivri            (or netivri.exe)   mode 0755, real file, no symlink
{DIR}/../share/netivri/LICENSE
{DIR}/../share/netivri/THIRD_PARTY_LICENSES/
```

The library must sit next to the binary under its canonical name — netivri
resolves it there and refuses to download it from the network (see
[docs/design/config-ktav.md](design/config-ktav.md)). The licenses are
installed one level up under `share/netivri/`. Re-running the installer
upgrades in place; it does not remove anything else in the directory.

## PATH

The POSIX installer never edits shell profiles. At the end it prints the
install directory and, when it is not already on `PATH`, the exact
`export PATH=...` line to add. On Windows, pass `-AddToPath` to add the
directory to the **user** `PATH` (existing sessions are unaffected);
without it the same hint is printed instead.

`-AddToPath` edits the registry value directly: the value type
(`REG_EXPAND_SZ` or `REG_SZ`) and every existing entry stay as they are, so
`%VAR%` entries keep expanding. A directory under `%LOCALAPPDATA%` (the
default) is added as `%LOCALAPPDATA%\...` when the value can expand
variables, otherwise as the full path. An entry that already points to the
directory (in either form) is not added twice.

## Proxies

- POSIX: the script does not strip proxy variables — `HTTPS_PROXY`,
  `HTTP_PROXY` and `ALL_PROXY` are honored natively by curl and wget.
- Windows: `Invoke-WebRequest` uses the system proxy settings; pass
  `-Proxy http://proxy:port` to override.

## Uninstall

```sh
sh install.sh --uninstall
```

```powershell
powershell -ExecutionPolicy Bypass -File install.ps1 -Uninstall
```

Removes exactly what the installer put there — the binary, the Ktav
library and `share/netivri/` — and nothing else.
