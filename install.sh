#!/bin/sh
# netivri standalone installer (POSIX sh, no bashisms).
#
# Downloads a release archive, verifies its SHA256 checksum against the
# published SHA256SUMS, and installs the netivri binary plus the Ktav
# native library (which must sit next to the binary) into a user
# directory. See docs/install.md.
set -eu

REPO="PHPCraftdream/netivri"
PROG="install.sh"
UNINSTALL=0

VERSION="${NETIVRI_VERSION:-}"
INSTALL_DIR="${NETIVRI_INSTALL_DIR:-${HOME:-.}/.netivri/bin}"
BASE_URL="${NETIVRI_BASE_URL:-}"

usage() {
  cat <<USAGE_EOF
Usage: $PROG [options]

Install the netivri binary and its Ktav native library.

Options:
  --version X.Y.Z  Install this version (default: latest release).
                   Env: NETIVRI_VERSION
  --dir DIR        Install directory (default: ${HOME}/.netivri/bin).
                   Env: NETIVRI_INSTALL_DIR
  --base-url URL   Download base URL: https://, http://, file:// or a
                   plain local directory that contains the archives and
                   SHA256SUMS. Env: NETIVRI_BASE_URL
  --uninstall      Remove the installed files.
  --help           Show this help.

Exit codes: 0 success, 1 failure, 2 usage error.
USAGE_EOF
}

fail() {
  printf '%s: error: %s\n' "$PROG" "$*" >&2
  exit 1
}

while [ $# -gt 0 ]; do
  case "$1" in
    --version)
      [ $# -ge 2 ] || { usage >&2; exit 2; }
      VERSION="$2"
      shift 2
      ;;
    --dir)
      [ $# -ge 2 ] || { usage >&2; exit 2; }
      INSTALL_DIR="$2"
      shift 2
      ;;
    --base-url)
      [ $# -ge 2 ] || { usage >&2; exit 2; }
      BASE_URL="$2"
      shift 2
      ;;
    --uninstall)
      UNINSTALL=1
      shift
      ;;
    --help|-h)
      usage
      exit 0
      ;;
    *)
      usage >&2
      exit 2
      ;;
  esac
done

# The version ends up in URLs and file names: allow only version characters.
case "$VERSION" in
  *[!0-9A-Za-z.+-]*)
    printf '%s: error: invalid version: %s\n' "$PROG" "$VERSION" >&2
    exit 2
    ;;
esac

case "$(uname -s)" in
  Linux) GOOS=linux ;;
  Darwin) GOOS=darwin ;;
  *) fail "unsupported operating system: $(uname -s) (supported: Linux, macOS)" ;;
esac

case "$(uname -m)" in
  x86_64|amd64) GOARCH=amd64 ;;
  aarch64|arm64) GOARCH=arm64 ;;
  *) fail "unsupported architecture: $(uname -m) (supported: x86_64, aarch64)" ;;
esac

case "$GOOS" in
  linux) LIB=libktav_cabi.so ;;
  darwin) LIB=libktav_cabi.dylib ;;
esac

if [ "$GOOS" = "linux" ]; then
  if [ -f /etc/alpine-release ]; then
    fail "Alpine Linux (musl) is not supported: the Ktav native library needs glibc 2.17+"
  fi
  if command -v ldd >/dev/null 2>&1 && ldd --version 2>&1 | grep -qi musl; then
    fail "musl-based Linux is not supported: the Ktav native library needs glibc 2.17+"
  fi
fi

# local_path FILE_URL: turn a file:// URL into a filesystem path.
local_path() {
  case "$1" in
    file:///*) printf '%s\n' "${1#file://}" ;;
    file://*) fail "file:// URLs must use an absolute path: $1" ;;
    *) printf '%s\n' "$1" ;;
  esac
}

FETCH=""

ensure_fetch() {
  [ -n "$FETCH" ] && return 0
  if command -v curl >/dev/null 2>&1; then
    FETCH=curl
  elif command -v wget >/dev/null 2>&1; then
    FETCH=wget
  else
    fail "need curl or wget to download; alternatively pass --base-url with a file:// URL or a local directory"
  fi
}

# fetch SRC DST: copy or download SRC to DST.
# HTTPS_PROXY / ALL_PROXY are honored natively by curl and wget and are
# deliberately not stripped here.
fetch() {
  case "$1" in
    file://*)
      cp "$(local_path "$1")" "$2"
      ;;
    http://*|https://*)
      ensure_fetch
      if [ "$FETCH" = "curl" ]; then
        curl -fSL --retry 3 -o "$2" "$1"
      else
        wget -O "$2" "$1"
      fi
      ;;
    *)
      cp "$1" "$2"
      ;;
  esac
}

# resolve_latest: print the latest release version (without the v).
resolve_latest() {
  ensure_fetch
  url="https://github.com/${REPO}/releases/latest"
  loc=""
  if [ "$FETCH" = "curl" ]; then
    loc="$(curl -fsSIo /dev/null -w '%{redirect_url}' "$url" || true)"
  else
    loc="$(wget -S --spider --max-redirect=0 "$url" 2>&1 || true)"
    loc="$(printf '%s\n' "$loc" | sed -n 's/^[[:space:]]*Location: *//p' | tail -n 1)"
  fi
  [ -n "$loc" ] || fail "cannot resolve the latest release tag from $url"
  tag="${loc##*/}"
  case "$tag" in
    v*) ;;
    *) fail "unexpected redirect when resolving the latest release: $loc" ;;
  esac
  printf '%s\n' "${tag#v}"
}

# sha_of FILE: print the SHA256 of FILE.
sha_of() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print $1}'
  elif command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$1" | awk '{print $1}'
  else
    fail "need sha256sum or shasum to verify the archive checksum"
  fi
}

# install_file SRC DST MODE: copy via a temp name, then rename.
install_file() {
  tmpdst="$2.tmp.$$"
  cp "$1" "$tmpdst"
  chmod "$3" "$tmpdst"
  mv -f "$tmpdst" "$2"
}

do_uninstall() {
  share="$INSTALL_DIR/../share/netivri"
  removed=0
  for f in "$INSTALL_DIR/netivri" "$INSTALL_DIR/$LIB"; do
    if [ -f "$f" ]; then
      rm -f "$f"
      printf 'removed %s\n' "$f"
      removed=1
    fi
  done
  if [ -d "$share" ]; then
    rm -rf "$share"
    printf 'removed %s\n' "$share"
    removed=1
  fi
  if [ "$removed" -eq 0 ]; then
    printf 'nothing to uninstall in %s\n' "$INSTALL_DIR"
  fi
}

if [ "$UNINSTALL" -eq 1 ]; then
  do_uninstall
  exit 0
fi

if [ -z "$VERSION" ]; then
  if [ -n "$BASE_URL" ]; then
    fail "when --base-url is used, --version (or NETIVRI_VERSION) must also be given"
  fi
  VERSION="$(resolve_latest)"
  printf 'latest release: %s\n' "$VERSION" >&2
fi

ARCHIVE="netivri_${VERSION}_${GOOS}_${GOARCH}.tar.gz"

if [ -n "$BASE_URL" ]; then
  case "$BASE_URL" in
    */) base="${BASE_URL%/}" ;;
    *) base="$BASE_URL" ;;
  esac
  ARCHIVE_URL="$base/$ARCHIVE"
  SUMS_URL="$base/SHA256SUMS"
else
  dl="https://github.com/${REPO}/releases/download/v${VERSION}"
  ARCHIVE_URL="$dl/$ARCHIVE"
  SUMS_URL="$dl/SHA256SUMS"
fi

TMP="$(mktemp -d "${TMPDIR:-/tmp}/netivri-install.XXXXXX")"
trap 'rm -rf "$TMP"' EXIT

printf 'downloading %s\n' "$ARCHIVE_URL" >&2
fetch "$ARCHIVE_URL" "$TMP/$ARCHIVE"
fetch "$SUMS_URL" "$TMP/SHA256SUMS"

# Verify the checksum BEFORE anything is extracted or installed.
expected="$(awk -v a="$ARCHIVE" '$1 ~ /^[0-9a-fA-F]{64}$/ && $2 == a {print $1; exit}' "$TMP/SHA256SUMS")"
[ -n "$expected" ] || fail "SHA256SUMS has no entry for $ARCHIVE"
actual="$(sha_of "$TMP/$ARCHIVE")"
if [ "$actual" != "$expected" ]; then
  fail "checksum mismatch for $ARCHIVE: expected $expected, got $actual"
fi

command -v tar >/dev/null 2>&1 || fail "need tar to extract the archive"
mkdir -p "$TMP/extracted"
tar -xzf "$TMP/$ARCHIVE" -C "$TMP/extracted"

SRC="$TMP/extracted/netivri_${VERSION}_${GOOS}_${GOARCH}"
if [ ! -d "$SRC" ]; then
  SRC="$(find "$TMP/extracted" -maxdepth 1 -type d -name 'netivri_*' | head -n 1)"
fi
if [ -z "$SRC" ] || [ ! -d "$SRC" ]; then
  fail "the archive does not contain the expected netivri_${VERSION}_${GOOS}_${GOARCH}/ directory"
fi
[ -f "$SRC/netivri" ] || fail "the archive does not contain the netivri binary"
[ -f "$SRC/$LIB" ] || fail "the archive does not contain $LIB"

SHARE="$INSTALL_DIR/../share/netivri"
mkdir -p "$INSTALL_DIR" "$SHARE/THIRD_PARTY_LICENSES"

install_file "$SRC/netivri" "$INSTALL_DIR/netivri" 755
install_file "$SRC/$LIB" "$INSTALL_DIR/$LIB" 755
cp "$SRC/LICENSE" "$SHARE/LICENSE"
cp -R "$SRC/THIRD_PARTY_LICENSES/." "$SHARE/THIRD_PARTY_LICENSES/"

printf 'installed netivri %s to %s\n' "$VERSION" "$INSTALL_DIR"
case ":${PATH:-}:" in
  *":$INSTALL_DIR:"*) ;;
  *)
    printf '%s is not on your PATH. Add it with:\n  export PATH="%s:$PATH"\n' "$INSTALL_DIR" "$INSTALL_DIR"
    ;;
esac
