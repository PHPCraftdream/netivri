#!/usr/bin/env bash
# Checks scripts/ci-scope.sh against a made-up module: which packages a diff
# selects, what runs everything and what is refused. Run from the repository
# root; ci.yml's lint job runs it.
set -euo pipefail

dir="$(mktemp -d)"
trap 'rm -rf "${dir}"' EXIT
printf '# comment\ndocs/cli.md ./cmd/app\n' > "${dir}/owners.txt"

m=example.com/m
pkgs="${m}/cmd/app
${m}/internal/core
${m}/internal/util
${m}/internal/other
${m}/internal/testutil"
# `go list -test` lines: the package, its test builds and the generated main.
graph="${m}/internal/util
${m}/internal/core	${m}/internal/util
${m}/cmd/app	${m}/internal/core ${m}/internal/util
${m}/internal/other
${m}/internal/testutil
${m}/internal/other [${m}/internal/other.test]	${m}/internal/testutil
${m}/internal/other.test	${m}/internal/other ${m}/internal/testutil"

# scope EVENT CHANGED [MESSAGE]: prints the script output.
scope() {
  CI_EVENT="$1" CI_CHANGED="$2" CI_MESSAGE="${3:-}" CI_MODULE="${m}" CI_PKGS="${pkgs}" \
    CI_GRAPH="${graph}" CI_OWNERS="${dir}/owners.txt" bash scripts/ci-scope.sh
}

# expect OUTPUT KEY WANT
expect() {
  local got
  got="$(printf '%s\n' "$1" | sed -n "s/^$2=//p")"
  if [ "${got}" != "$3" ]; then
    echo "$2: got [${got}], wanted [$3]" >&2
    printf '%s\n' "$1" >&2
    exit 1
  fi
}

# A change of util reaches what imports it, transitively.
for event in push pull_request; do
  out="$(scope "${event}" 'internal/util/x.go')"
  expect "${out}" packages './cmd/app ./internal/core ./internal/util'
  expect "${out}" full false
done

# A package nobody imports selects itself only; a test helper selects the
# packages whose tests use it; a file below a package directory belongs to it.
out="$(scope push 'internal/other/x_test.go')"
expect "${out}" packages './internal/other'
out="$(scope push 'internal/testutil/h.go')"
expect "${out}" packages './internal/other ./internal/testutil'
out="$(scope push 'internal/core/testdata/a.json')"
expect "${out}" packages './cmd/app ./internal/core'

# Documents run no test, except a file a test reads.
out="$(scope push $'docs/guide.md\nCHANGELOG.md\nREADME.md\nLICENSE')"
expect "${out}" packages ''
expect "${out}" full false
out="$(scope push 'docs/cli.md')"
expect "${out}" packages './cmd/app'

# Anything that is not a file of one package runs everything.
for change in 'go.mod' 'go.sum' '.github/workflows/ci.yml' 'scripts/ci-test.sh' 'third_party/x/y.go' \
  'npm/package.json' 'install.sh' 'internal/gone/x.go'; do
  out="$(scope push $'internal/util/x.go\n'"${change}")"
  expect "${out}" packages './...'
  expect "${out}" full true
done

# A diff that reaches every package is a full run.
out="$(scope push $'internal/util/x.go\ninternal/other/x.go\ninternal/testutil/h.go')"
expect "${out}" full true
expect "${out}" packages './...'

# An unknown diff runs everything.
out="$(CI_EVENT=push CI_MODULE="${m}" CI_PKGS="${pkgs}" CI_GRAPH="${graph}" env -u CI_CHANGED bash scripts/ci-scope.sh)"
expect "${out}" full true

# "ci: full" in the message runs everything (bullet, case and CRLF do not
# matter); the same words inside a sentence, or any other line, do not.
for message in $'Fix\n\nci: full' $'Fix\r\n\r\n* CI:  Full \r\n' $'  - ci: full'; do
  out="$(scope push 'docs/guide.md' "${message}")"
  expect "${out}" full true
  expect "${out}" packages './...'
done
for message in 'no ci: full here' $'ci: fuller\nci-scope: full' 'ci: ./internal/util'; do
  out="$(scope push 'docs/guide.md' "${message}")"
  expect "${out}" full false
  expect "${out}" packages ''
done

# Other events ignore the diff.
out="$(scope schedule 'docs/guide.md')"
expect "${out}" full true
expect "${out}" packages './...'

# Manual runs take their inputs, which are checked.
dispatch() {
  CI_EVENT=workflow_dispatch CI_IN_PACKAGES="${1:-}" CI_IN_RUN="${2:-}" CI_IN_OS="${3:-}" bash scripts/ci-scope.sh
}
out="$(dispatch)"
expect "${out}" full true
expect "${out}" packages './...'
expect "${out}" matrix_os '["ubuntu-latest","windows-latest","macos-latest"]'
expect "${out}" matrix_os_e2e '["ubuntu-latest","windows-latest"]'
out="$(dispatch './internal/auth/... ./internal/core' 'TestHash|TestA$' windows)"
expect "${out}" packages './internal/auth/... ./internal/core'
expect "${out}" run 'TestHash|TestA$'
expect "${out}" matrix_os '["windows-latest"]'
expect "${out}" matrix_os_e2e '["windows-latest"]'
expect "${out}" full false
out="$(dispatch '' '' macos)"
expect "${out}" matrix_os_e2e '[]'

refuse() {
  if dispatch "$@" > /dev/null 2>&1; then
    echo "accepted, should be refused: $*" >&2
    exit 1
  fi
}
refuse './x;y'
refuse '../x'
refuse '/abs/path'
refuse './x/../y'
refuse './x/...y'
refuse '$(id)'
refuse '' '$(id)'
refuse '' 'a`id`'
refuse '' '"a"'
refuse '' 'a b'
refuse '' 'a\b'
refuse '' "$(printf 'a%.0s' $(seq 1 201))"
refuse '' '' plan9
many=""
for i in $(seq 1 21); do many="${many} ./p${i}"; done
refuse "${many}"

echo "ci-scope: ok"
