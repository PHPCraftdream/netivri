#!/usr/bin/env bash
# Publish the packed netivri npm packages: the six platform packages first and
# the main package last, so a half-failed run never leaves a main package that
# points at missing binaries. Safe to re-run: a version that is already on the
# registry is skipped, so a run that failed halfway completes on the next try.
#
# Usage: scripts/npm-publish.sh <version> <dist-dir> <mode>
#   mode  token    publish with the npm auth already configured (NPM_TOKEN)
#         oidc     publish through npm Trusted Publishing (GitHub OIDC)
#         dry-run  `npm publish --dry-run`, no registry write, no auth needed
#
# Both real modes attach a provenance statement. A version with a prerelease
# part (1.2.3-rc.1) goes to the `next` dist-tag instead of `latest`.
set -euo pipefail

version="${1:?usage: npm-publish.sh <version> <dist-dir> <mode>}"
dist="${2:?usage: npm-publish.sh <version> <dist-dir> <mode>}"
mode="${3:?usage: npm-publish.sh <version> <dist-dir> <mode>}"

platforms=(linux-x64 linux-arm64 darwin-x64 darwin-arm64 win32-x64 win32-arm64)

args=(--access public)
case "${mode}" in
  token | oidc) args+=(--provenance) ;;
  dry-run) args+=(--dry-run) ;;
  *)
    echo "unknown mode: ${mode}" >&2
    exit 2
    ;;
esac
case "${version}" in
  *-*) args+=(--tag next) ;;
esac

names=()
for p in "${platforms[@]}"; do
  names+=("netivri-${p}")
done
names+=(netivri)

for name in "${names[@]}"; do
  tgz="${dist}/${name}-${version}.tgz"
  if [ ! -f "${tgz}" ]; then
    echo "missing ${tgz}" >&2
    exit 1
  fi
  if [ "${mode}" != dry-run ] && npm view "${name}@${version}" version > /dev/null 2>&1; then
    echo "${name}@${version} is already on the registry: skipped"
    continue
  fi
  echo "publishing ${name}@${version} (${mode})"
  npm publish "${tgz}" "${args[@]}"
done
