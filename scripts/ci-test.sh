#!/usr/bin/env bash
# Run one go test invocation from CI_* environment variables.
#   CI_PACKAGES  package patterns (default ./...)
#   CI_TAGS      comma-separated build tags (default: none)
#   CI_RUN       -run regex (optional)
#   CI_RACE      1 => add -race
#   CI_COVER     1 => add -covermode=atomic -coverprofile=coverage.out
set -euo pipefail

packages="${CI_PACKAGES:-./...}"
tags="${CI_TAGS:-}"
run="${CI_RUN:-}"
race="${CI_RACE:-0}"
cover="${CI_COVER:-0}"

args=(go test -count=1)

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

echo "+ ${args[*]}" >&2
exec "${args[@]}"
