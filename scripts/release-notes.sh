#!/usr/bin/env bash
# Print the CHANGELOG.md section of one version (the text between its
# "## [X.Y.Z]" heading and the next "## [" heading) for the GitHub Release.
# Fails when the section is missing or empty, so a release is never cut
# without a changelog entry.
#
# Usage: scripts/release-notes.sh <version>      e.g. 0.1.0
set -euo pipefail

version="${1:?usage: release-notes.sh <version>}"
file="${CHANGELOG:-CHANGELOG.md}"

notes="$(awk -v v="${version}" '
  /^## \[/ {
    if (found) exit
    if (index($0, "## [" v "]") == 1) { found = 1; next }
  }
  found { lines[++n] = $0 }
  END {
    s = 1
    while (s <= n && lines[s] ~ /^[[:space:]]*$/) s++
    e = n
    while (e >= s && lines[e] ~ /^[[:space:]]*$/) e--
    for (i = s; i <= e; i++) print lines[i]
  }
' "${file}")"

if [ -z "${notes}" ]; then
  echo "no changelog section for version ${version} in ${file}" >&2
  exit 1
fi
printf '%s\n' "${notes}"
