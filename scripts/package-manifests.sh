#!/usr/bin/env bash
# Write the Scoop manifest (netivri.json) and the Homebrew formula
# (netivri.rb) of a release into DIR, from the archives that sit there. The
# hashes are computed from the archive files, the URLs point at the GitHub
# Release of the tag.
#
# Usage: scripts/package-manifests.sh <version> <dir>      e.g. 0.1.0 artifacts
set -euo pipefail

version="${1:?usage: package-manifests.sh <version> <dir>}"
dir="${2:?usage: package-manifests.sh <version> <dir>}"
base="https://github.com/PHPCraftdream/netivri/releases/download/v${version}"
desc="Local SOCKS5/SOCKS4/HTTP proxy service built on Mihomo, with per-account exit countries"
home="https://github.com/PHPCraftdream/netivri"

sha() {
  local f="${dir}/$1"
  [ -f "${f}" ] || { echo "missing ${f}" >&2; exit 1; }
  sha256sum "${f}" | awk '{print $1}'
}

win_amd64="netivri_${version}_windows_amd64.zip"
win_arm64="netivri_${version}_windows_arm64.zip"
cat > "${dir}/netivri.json" << JSON
{
  "version": "${version}",
  "description": "${desc}",
  "homepage": "${home}",
  "license": "GPL-3.0-only",
  "architecture": {
    "64bit": {
      "url": "${base}/${win_amd64}",
      "hash": "$(sha "${win_amd64}")",
      "extract_dir": "netivri_${version}_windows_amd64"
    },
    "arm64": {
      "url": "${base}/${win_arm64}",
      "hash": "$(sha "${win_arm64}")",
      "extract_dir": "netivri_${version}_windows_arm64"
    }
  },
  "bin": "netivri.exe"
}
JSON

archive() { echo "netivri_${version}_$1.tar.gz"; }
cat > "${dir}/netivri.rb" << RUBY
class Netivri < Formula
  desc "${desc}"
  homepage "${home}"
  version "${version}"
  license "GPL-3.0-only"

  on_macos do
    on_arm do
      url "${base}/$(archive darwin_arm64)"
      sha256 "$(sha "$(archive darwin_arm64)")"
    end
    on_intel do
      url "${base}/$(archive darwin_amd64)"
      sha256 "$(sha "$(archive darwin_amd64)")"
    end
  end

  on_linux do
    on_arm do
      url "${base}/$(archive linux_arm64)"
      sha256 "$(sha "$(archive linux_arm64)")"
    end
    on_intel do
      url "${base}/$(archive linux_amd64)"
      sha256 "$(sha "$(archive linux_amd64)")"
    end
  end

  def install
    bin.install "netivri"
    doc.install "LICENSE", "README.md"
  end

  test do
    assert_match version.to_s, shell_output("#{bin}/netivri version")
  end
end
RUBY
echo "wrote ${dir}/netivri.json and ${dir}/netivri.rb" >&2
