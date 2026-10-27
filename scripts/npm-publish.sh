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

# npm reads a relative `dir/name.tgz` as a GitHub `user/repo` shorthand; an
# absolute path is always a file.
dist="$(cd "${dist}" && pwd)"

# Every package is in the author's scope. The Windows packages go first: if npm
# refuses a new name (spam detection), the run stops before any other package
# moves on.
scope="@phpcraftdream"
platforms=(w32-x64 w32-arm64 linux-x64 linux-arm64 darwin-x64 darwin-arm64)

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
  names+=("${scope}/netivri-${p}")
done
names+=("${scope}/netivri")

for name in "${names[@]}"; do
  # npm pack names the tarball scope-name-version.tgz, without "@" and "/".
  stem="${name#@}"
  tgz="${dist}/${stem//\//-}-${version}.tgz"
  if [ ! -f "${tgz}" ]; then
    echo "missing ${tgz}" >&2
    exit 1
  fi
  if [ "${mode}" != dry-run ] && npm view "${name}@${version}" version > /dev/null 2>&1; then
    echo "${name}@${version} is already on the registry: skipped"
    continue
  fi
  echo "publishing ${name}@${version} (${mode})"
  # `npm view` reads a cached view of the registry and can miss a version
  # published a minute ago; npm then refuses the publish with E409. That
  # version is on the registry, so it counts as skipped, not as a failure.
  log="$(mktemp)"
  if npm publish "${tgz}" "${args[@]}" 2>&1 | tee "${log}"; then
    rm -f "${log}"
    continue
  fi
  if grep -qE 'E409|previously (staged|published) version|cannot publish over' "${log}"; then
    rm -f "${log}"
    echo "${name}@${version} is already on the registry: skipped"
    continue
  fi
  rm -f "${log}"
  exit 1
done
