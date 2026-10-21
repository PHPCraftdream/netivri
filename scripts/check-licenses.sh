#!/usr/bin/env bash
# The dependency license policy in one place (see docs/licenses.md).
#
# 1. Every Go module linked into the binary must carry a license that can be
#    combined into a GPL-3.0-only work; anything else fails the check.
# 2. Every place that declares netivri's own license must say GPL-3.0-only.
#
# Run from the repository root. CI calls it from ci.yml and release.yml.
set -euo pipefail

GO_LICENSES_VERSION=v1.6.0

# All of these can be combined into a GPL-3.0 work. A new failure needs human
# review: never extend this list or the ignores just to make the check pass.
ALLOWED='GPL-3.0,LGPL-3.0,MIT,Apache-2.0,BSD-2-Clause,BSD-3-Clause,ISC,MPL-2.0,Unlicense,CC0-1.0,BlueOak-1.0.0'

# Audited exceptions, each explained in docs/licenses.md.
IGNORED_MODULES=(
  github.com/metacubex/chacha
  github.com/rasky/go-lzo
  modernc.org/mathutil
)

PATH="$(go env GOPATH)/bin:${PATH}"
if ! command -v go-licenses >/dev/null 2>&1; then
  go install "github.com/google/go-licenses@${GO_LICENSES_VERSION}"
fi

ignore_flags=()
for m in "${IGNORED_MODULES[@]}"; do
  ignore_flags+=("--ignore=${m}")
done

echo "== dependency licenses" >&2
go-licenses check ./... "--allowed_licenses=${ALLOWED}" "${ignore_flags[@]}"

echo "== declared license of netivri" >&2
fail=0
bad() {
  echo "license declaration: $*" >&2
  fail=1
}

for f in LICENSE third_party/mihomo/LICENSE; do
  if ! grep -q 'GNU GENERAL PUBLIC LICENSE' "${f}" || ! grep -q 'Version 3, 29 June 2007' "${f}"; then
    bad "${f} is not the GPL v3 text"
  fi
done

declaration='["'\'']?license["'\'']?[[:space:]]*:'
for f in npm/netivri/package.json npm/scripts/build-packages.js; do
  if ! grep -Eq "${declaration}" "${f}"; then
    bad "${f} declares no license"
  elif grep -E "${declaration}" "${f}" | grep -vq 'GPL-3.0-only'; then
    bad "${f} declares a license other than GPL-3.0-only"
  fi
done

if ! grep -q 'GPL-3.0-only' README.md; then
  bad "README.md does not state GPL-3.0-only"
fi

if [ "${fail}" = 1 ]; then
  exit 1
fi
echo "licenses ok" >&2
