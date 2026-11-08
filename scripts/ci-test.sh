#!/usr/bin/env bash
# Run one go test invocation from CI_* environment variables.
#   CI_PACKAGES  package patterns (default ./...)
#   CI_TAGS      comma-separated build tags (default: none)
#   CI_RUN       -run regex (optional)
#   CI_RACE      1 => add -race
#   CI_COVER     1 => add -covermode=atomic -coverprofile=coverage.out
#   CI_TIMEOUT   go test -timeout (default 10m, the go default)
#   CI_VERBOSE   1 => add -v and list the slowest tests at the end (to the step
#                summary when there is one); CI_SUMMARY_TITLE names that list
set -euo pipefail

packages="${CI_PACKAGES:-./...}"
tags="${CI_TAGS:-}"
run="${CI_RUN:-}"
race="${CI_RACE:-0}"
cover="${CI_COVER:-0}"

args=(go test -count=1)

if [[ "${CI_VERBOSE:-0}" == "1" ]]; then
  args+=(-v)
fi

if [[ -n "${CI_TIMEOUT:-}" ]]; then
  args+=(-timeout="${CI_TIMEOUT}")
fi

if [[ -n "${tags}" ]]; then
  args+=(-tags="${tags}")
fi

if [[ "${race}" == "1" ]]; then
  args+=(-race)
fi

if [[ "${cover}" == "1" ]]; then
  args+=(-covermode=atomic -coverprofile=coverage.out)
fi

if [[ -n "${run}" ]]; then
  args+=(-run="${run}")
fi

# shellcheck disable=SC2086
args+=(${packages})

if [[ "${CI_VERBOSE:-0}" != "1" ]]; then
  echo "+ ${args[*]}" >&2
  exec "${args[@]}"
fi

echo "+ ${args[*]}" >&2
log="$(mktemp)"
set +e
"${args[@]}" 2>&1 | tee "${log}"
status=${PIPESTATUS[0]}
set -e

# The top-level tests with their time, longest first: where the minutes go.
{
  echo "### ${CI_SUMMARY_TITLE:-Slowest tests}"
  echo '```'
  grep -E '^--- (PASS|FAIL): ' "${log}" |
    sed -E 's/^--- (PASS|FAIL): ([^ ]+) \(([0-9.]+)s\).*/\3s \2/' |
    sort -rn | head -20 || true
  echo '```'
} | tee -a "${GITHUB_STEP_SUMMARY:-/dev/null}"
rm -f "${log}"
exit "${status}"
