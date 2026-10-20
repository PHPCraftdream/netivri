# Standalone installation (no npm)

The standalone installers download a prebuilt release archive, verify its
SHA256 checksum, and put the `netivri` binary and the Ktav native library
next to each other in a user directory. They never rebuild anything, never
use `sudo`, never touch your shell profile, and never prompt.

An npm-based alternative exists; see [docs/npm.md](npm.md).

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

## Supported platforms

| OS | Architectures | Notes |
|---|---|---|
| Linux | amd64, arm64 | glibc 2.17+ required |
| macOS (darwin) | amd64, arm64 | |
| Windows | amd64, arm64 | PowerShell 5.1 or 7 |

**Alpine and every other musl-based distribution are not supported.** The
Ktav native library (`libktav_cabi.so`), which must sit next to the binary,
is built against glibc; the installer detects musl (`ldd --version` or
`/etc/alpine-release`) and aborts with a clear message. See
[docs/release.md](release.md) and [docs/design/config-ktav.md](design/config-ktav.md).

## Options

Both installers take the same options (POSIX names / PowerShell parameters):

| install.sh | install.ps1 | Env variable | Default | Meaning |
|---|---|---|---|---|
| `--version X.Y.Z` | `-Version X.Y.Z` | `NETIVRI_VERSION` | latest release | Version to install (no leading `v`). |
| `--dir DIR` | `-InstallDir DIR` | `NETIVRI_INSTALL_DIR` | `~/.netivri/bin` / `%LOCALAPPDATA%\netivri\bin` | Install directory. |
| `--base-url URL` | `-BaseUrl URL` | `NETIVRI_BASE_URL` | GitHub releases | Download base URL (see below). |
| — | `-Proxy URL` | — | system proxy | HTTP(S) proxy for `Invoke-WebRequest`. |
| — | `-AddToPath` | — | off | Add the install dir to the **user** `PATH`. |
| `--uninstall` | `-Uninstall` | — | — | Remove the installed files. |
| `--help` | `-Help` | — | — | Show help. |

Exit codes: `0` success, `1` failure, `2` usage error.

`--base-url` / `-BaseUrl` accepts:

- `https://…` / `http://…` — the archive and `SHA256SUMS` are fetched from
  `<base>/<file>` directly, without the release-tag path (useful for
  mirrors and CI);
- `file://…` or a plain local directory path — the files are copied from
  `<base>/<file>`.

When `--base-url` / `-BaseUrl` is used, `--version` / `-Version` must be
given too, because the archive filename contains the version. The default
version is resolved from the `releases/latest` redirect (an HTTPS HEAD
request, no API call, no rate limit).

## How verification works

1. The archive (`netivri_<version>_<os>_<arch>.tar.gz` or `.zip`) and the
   release's `SHA256SUMS` file are downloaded.
2. The SHA256 of the archive is computed locally and compared with the
   entry for that exact filename in `SHA256SUMS`.
3. A missing entry or any mismatch aborts the install **before** anything
   is extracted into the install directory.
4. Only then is the archive extracted into a private temporary directory
   (removed on exit), and the files are copied into place via temp names
   and renamed, so a crash cannot leave a half-written binary.

Checksum verification is mandatory and cannot be skipped by any flag.

## What gets installed

```
<dir>/netivri            (or netivri.exe)   mode 0755, real file, no symlink
<dir>/libktav_cabi.so | libktav_cabi.dylib | ktav_cabi.dll
<dir>/../share/netivri/LICENSE
<dir>/../share/netivri/THIRD_PARTY_LICENSES/
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
