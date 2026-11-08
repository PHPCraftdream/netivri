#!/usr/bin/env bash
# Prints the -run regex of one shard of the tagged tests of some package
# directories, or nothing when the shard has no test.
#   usage: ci-e2e-shard.sh SHARD TOTAL DIR...      (SHARD counts from 1)
#   CI_TEST_TAG  the build tag whose tests are dealt out (default e2e)
# The e2e tests are the Test functions of the *_test.go files whose
# //go:build line names the e2e tag (the unit tests of the package run in
# the unit job, and are not repeated here); the integration job uses the same
# script with CI_TEST_TAG=integration. They are dealt out one by one in
# file order, so the heavy neighbours (the protocol files) land in different
# shards. The names come from the sources, no test binary is built for this.
set -euo pipefail

tag="${CI_TEST_TAG:-e2e}"
shard="${1:?shard}"
total="${2:?total}"
shift 2

if ! [[ "${shard}" =~ ^[0-9]+$ && "${total}" =~ ^[0-9]+$ ]] || [ "${shard}" -lt 1 ] || [ "${shard}" -gt "${total}" ]; then
  echo "ci-e2e-shard: bad shard ${shard}/${total}" >&2
  exit 2
fi

names=()
for dir in "$@"; do
  # sort: the same order everywhere, whatever the file system returns.
  while IFS= read -r f; do
    grep -qE "^//go:build .*\b${tag}\b" "${f}" || continue
    while IFS= read -r name; do
      [ "${name}" = "TestMain" ] && continue
      names+=("${name}")
    done < <(grep -hoE '^func Test[A-Za-z0-9_]*' "${f}" | sed 's/^func //')
  done < <(ls "${dir}"/*_test.go 2>/dev/null | sort)
done

picked=""
for i in "${!names[@]}"; do
  if [ $((i % total)) -eq $((shard - 1)) ]; then
    picked="${picked:+${picked}|}${names[$i]}"
  fi
done

if [ -n "${picked}" ]; then
  echo "^(${picked})\$"
fi
