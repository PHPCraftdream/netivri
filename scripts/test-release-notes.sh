#!/usr/bin/env bash
# Checks scripts/release-notes.sh against a throw-away changelog. Run from the
# repository root; ci.yml runs it because the release workflow depends on it.
set -euo pipefail

dir="$(mktemp -d)"
trap 'rm -rf "${dir}"' EXIT

cat > "${dir}/CHANGELOG.md" << 'EOF'
# Changelog

## [Unreleased]

- next thing

## [0.2.0] - 2026-02-01

### Added

- second

## [0.1.0-rc.1] - 2026-01-15

- candidate

## [0.1.0] - 2026-01-01

- first

## [0.0.1]

EOF
export CHANGELOG="${dir}/CHANGELOG.md"

expect() {
  local version="$1" want="$2" got
  got="$(bash scripts/release-notes.sh "${version}")"
  if [ "${got}" != "${want}" ]; then
    echo "release-notes ${version}: got [${got}], wanted [${want}]" >&2
    exit 1
  fi
}

refuse() {
  if bash scripts/release-notes.sh "$1" > /dev/null 2>&1; then
    echo "release-notes $1: expected a failure" >&2
    exit 1
  fi
}

expect 0.2.0 $'### Added\n\n- second'
expect 0.1.0 '- first'
expect 0.1.0-rc.1 '- candidate'
expect Unreleased '- next thing'

refuse 0.0.1 # empty section
refuse 9.9.9 # no section
refuse 0.2 # a prefix of a version is not that version
refuse 0.1 # nor is this one

echo "release-notes ok" >&2
