#!/usr/bin/env bash
# Collect the license and notice files of every third-party Go module linked
# into ./cmd/netivri (for the current GOOS/GOARCH/CGO_ENABLED) into
# <outdir>/<module path>/. Fails when a module has no license file, so a
# release never ships without notices. Run from the repository root.
#
# Usage: scripts/collect-licenses.sh <outdir>
set -euo pipefail

out="${1:?usage: collect-licenses.sh <outdir>}"
rm -rf "${out}"
mkdir -p "${out}"

list="$(go list -deps \
  -f '{{if and .Module (not .Module.Main)}}{{.Module.Path}} {{.Module.Dir}}{{end}}' \
  ./cmd/netivri | sort -u)"

count=0
missing=0
while read -r path dir; do
  [ -n "${path}" ] || continue
  found=0
  for f in "${dir}"/*; do
    [ -f "${f}" ] || continue
    name="$(basename "${f}" | tr '[:upper:]' '[:lower:]')"
    case "${name}" in
      license* | licence* | copying* | notice* | patents* | unlicense*)
        mkdir -p "${out}/${path}"
        install -m 0644 "${f}" "${out}/${path}/"
        found=1
        ;;
    esac
  done
  if [ "${found}" = 0 ]; then
    echo "no license file for module ${path}" >&2
    missing=1
  else
    count=$((count + 1))
  fi
done <<< "${list}"

if [ "${missing}" = 1 ]; then
  exit 1
fi

cat > "${out}/README.txt" <<'EOF'
License and notice files of the third-party Go modules linked into netivri,
one directory per module. netivri itself is GPL-3.0-only (see LICENSE).

The Ktav native library shipped next to the binary (ktav_cabi) is
MIT OR Apache-2.0, see https://github.com/ktav-lang/golang.
EOF

echo "collected notices for ${count} modules into ${out}" >&2
