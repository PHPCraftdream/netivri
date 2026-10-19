#!/usr/bin/env bash
# Fetch the Ktav C ABI native library from GitHub releases and verify its
# SHA256 against scripts/ktav-lib.sha256.
# Usage: fetch-ktav-lib.sh <goos> <goarch> <destdir>
set -euo pipefail

KTAV_VERSION="v0.8.0"
BASE_URL="https://github.com/ktav-lang/golang/releases/download/${KTAV_VERSION}"

if [[ $# -ne 3 ]]; then
  echo "usage: $0 <goos> <goarch> <destdir>" >&2
  exit 1
fi

goos="$1"
goarch="$2"
destdir="$3"

case "${goos}" in
  windows) asset="ktav_cabi-${goos}-${goarch}.dll"; out="ktav_cabi.dll" ;;
  darwin)  asset="libktav_cabi-${goos}-${goarch}.dylib"; out="libktav_cabi.dylib" ;;
  linux)   asset="libktav_cabi-${goos}-${goarch}.so"; out="libktav_cabi.so" ;;
  *)
    echo "error: unsupported GOOS '${goos}'" >&2
    exit 1
    ;;
esac

# Resolve destdir to an absolute path.
mkdir -p "${destdir}"
destdir="$(cd "${destdir}" && pwd)"

shafile="$(dirname "$0")/ktav-lib.sha256"
if [[ ! -f "${shafile}" ]]; then
  echo "error: checksum file not found: ${shafile}" >&2
  exit 1
fi

expected="$(awk -v a="${asset}" '$2 == a { print $1 }' "${shafile}")"
if [[ -z "${expected}" ]]; then
  echo "error: no pinned SHA256 for asset '${asset}' in ${shafile}" >&2
  exit 1
fi

tmp="$(mktemp)"
trap 'rm -f "${tmp}"' EXIT

url="${BASE_URL}/${asset}"
echo "fetching ${url}" >&2
curl -fsSL --retry 3 -o "${tmp}" "${url}"

# Verify the digest (sha256sum, fall back to shasum -a 256).
if command -v sha256sum >/dev/null 2>&1; then
  actual="$(sha256sum "${tmp}" | awk '{ print $1 }')"
elif command -v shasum >/dev/null 2>&1; then
  actual="$(shasum -a 256 "${tmp}" | awk '{ print $1 }')"
else
  echo "error: no sha256sum or shasum available" >&2
  exit 1
fi

if [[ "${actual}" != "${expected}" ]]; then
  echo "error: SHA256 mismatch for ${asset}" >&2
  echo "  expected: ${expected}" >&2
  echo "  actual:   ${actual}" >&2
  exit 1
fi

final="${destdir}/${out}"
cp "${tmp}" "${final}"

# Go needs a native Windows path when invoked from Git Bash.
if command -v cygpath >/dev/null 2>&1; then
  final="$(cygpath -w "${final}")"
fi

echo "verified ${asset} (${actual})" >&2
echo "${final}"
