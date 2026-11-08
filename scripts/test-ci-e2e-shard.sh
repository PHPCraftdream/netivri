#!/usr/bin/env bash
# Checks scripts/ci-e2e-shard.sh against made-up test files: only the e2e
# files count, TestMain is never run by name, every test lands in exactly one
# shard, and a bad call is refused. Run from the repository root; ci.yml's
# lint job runs it.
set -euo pipefail

dir="$(mktemp -d)"
trap 'rm -rf "${dir}"' EXIT
other="$(mktemp -d)"
trap 'rm -rf "${dir}" "${other}"' EXIT

cat > "${dir}/a_e2e_test.go" <<'EOF'
//go:build e2e

package app

func TestMain(m *testing.M) {}
func TestA1(t *testing.T)   {}
func TestA2(t *testing.T)   {}
func helperNotATest()       {}
EOF
cat > "${dir}/b_e2e_test.go" <<'EOF'
//go:build integration || e2e

package app

func TestB1(t *testing.T) {}
func TestB2(t *testing.T) {}
func TestB3(t *testing.T) {}
EOF
cat > "${dir}/unit_test.go" <<'EOF'
package app

func TestUnit(t *testing.T) {}
EOF
cat > "${dir}/integ_test.go" <<'EOF'
//go:build integration

package app

func TestIntegrationOnly(t *testing.T) {}
EOF
cat > "${other}/c_e2e_test.go" <<'EOF'
//go:build e2e

package other

func TestC1(t *testing.T) {}
EOF

# expect GOT WANT LABEL
expect() {
  if [ "$1" != "$2" ]; then
    echo "$3: got [$1], wanted [$2]" >&2
    exit 1
  fi
}

# One shard has all of them, in file order, without the unit, the
# integration-only tests and TestMain.
expect "$(bash scripts/ci-e2e-shard.sh 1 1 "${dir}")" '^(TestA1|TestA2|TestB1|TestB2|TestB3)$' "one shard"

# Dealt out one by one.
expect "$(bash scripts/ci-e2e-shard.sh 1 2 "${dir}")" '^(TestA1|TestB1|TestB3)$' "shard 1 of 2"
expect "$(bash scripts/ci-e2e-shard.sh 2 2 "${dir}")" '^(TestA2|TestB2)$' "shard 2 of 2"

# Every test is in exactly one of three shards.
all=""
for s in 1 2 3; do
  part="$(bash scripts/ci-e2e-shard.sh "${s}" 3 "${dir}")"
  part="${part#^(}"
  part="${part%)\$}"
  all="${all}${all:+|}${part}"
done
expect "$(printf '%s\n' "${all//|/$'\n'}" | sort | tr '\n' ' ')" "TestA1 TestA2 TestB1 TestB2 TestB3 " "three shards together"

# More shards than tests: the extra ones are empty.
expect "$(bash scripts/ci-e2e-shard.sh 6 6 "${dir}")" "" "an empty shard"

# The same script deals out the integration tests: the files that name that tag.
expect "$(CI_TEST_TAG=integration bash scripts/ci-e2e-shard.sh 1 1 "${dir}")" '^(TestB1|TestB2|TestB3|TestIntegrationOnly)$' "integration tag"
expect "$(CI_TEST_TAG=integration bash scripts/ci-e2e-shard.sh 2 2 "${dir}")" '^(TestB2|TestIntegrationOnly)$' "integration shard 2 of 2"

# Several directories are dealt out together, in the order given.
expect "$(bash scripts/ci-e2e-shard.sh 1 1 "${dir}" "${other}")" '^(TestA1|TestA2|TestB1|TestB2|TestB3|TestC1)$' "two directories"

# A directory without test files, and bad calls.
empty="$(mktemp -d)"
expect "$(bash scripts/ci-e2e-shard.sh 1 1 "${empty}")" "" "no test files"
rmdir "${empty}"
for bad in "0 3" "4 3" "x 3" "1 y"; do
  # shellcheck disable=SC2086
  if bash scripts/ci-e2e-shard.sh ${bad} "${dir}" 2>/dev/null; then
    echo "shard ${bad}: accepted" >&2
    exit 1
  fi
done

echo "ci-e2e-shard: ok"
