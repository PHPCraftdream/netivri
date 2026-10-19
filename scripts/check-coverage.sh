#!/usr/bin/env bash
# Check per-package coverage floors from a Go cover profile.
# Usage: check-coverage.sh [coverage-profile]   (default: coverage.out)
# Floors: scripts/coverage-min.txt, format documented in that file.
# Only bash + awk. Prints a markdown table; exits non-zero below floor.
set -euo pipefail

profile="${1:-coverage.out}"
floors="$(cd "$(dirname "$0")" && pwd)/coverage-min.txt"

if [[ ! -f "${profile}" ]]; then
  echo "error: coverage profile not found: ${profile}" >&2
  exit 1
fi
if [[ ! -f "${floors}" ]]; then
  echo "error: floors file not found: ${floors}" >&2
  exit 1
fi

awk -v profile="${profile}" -v floors="${floors}" '
function floor_for(pkg,    i, fl) {
  fl = deffloor + 0
  for (i = 1; i <= nfloor; i++) {
    if (pkg == fsuf[i] || index(pkg, "/" fsuf[i]) == length(pkg) - length(fsuf[i])) {
      fl = fmin[i] + 0
      break
    }
  }
  return fl
}
BEGIN {
  nfloor = 0
  deffloor = 0
  FS = "[ \t]+"
}
# --- pass 1: floors ---
{ sub(/\r$/, "") }
FILENAME == ARGV[1] {
  if ($1 == "" || $1 ~ /^#/) next
  if ($1 == "default") { deffloor = $2 + 0; next }
  if (NF >= 2) { nfloor++; fsuf[nfloor] = $1; fmin[nfloor] = $2 + 0 }
  next
}
# --- pass 2: coverage profile ---
# Lines: name.go:startLine.startCol,endLine.endCol numStmts hitCount
/^mode:/ { next }
{
  split($1, a, ":")
  path = a[1]
  n = split(path, parts, "/")
  pkg = path
  sub(/\/[^\/]*$/, "", pkg)
  if (pkg == path) pkg = "."   # file in cwd
  stmts = $2 + 0
  hit = $3 + 0
  if (stmts <= 0) next
  total[pkg] += stmts
  if (hit > 0) covered[pkg] += stmts
  if (!(pkg in seenseq)) { seenseq[pkg] = ++npkg; order[npkg] = pkg }
}
END {
  printf "| Package | Coverage | Floor | Status |\n"
  printf "|---|---|---|---|\n"
  fail = 0
  for (p = 1; p <= npkg; p++) {
    pkg = order[p]
    if (total[pkg] <= 0) continue
    pct = covered[pkg] * 100.0 / total[pkg]
    fl = floor_for(pkg)
    ok = (pct + 0.0001 >= fl)
    if (!ok) fail = 1
    printf "| %s | %.1f%% | %.1f%% | %s |\n", pkg, pct, fl, (ok ? "✅" : "❌")
  }
  exit fail
}
' "${floors}" "${profile}"
