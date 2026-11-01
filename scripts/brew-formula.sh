#!/usr/bin/env bash
# Writes the Homebrew formula dist/node-tracker.rb for a release tag from
# dist/SHA256SUMS. Usage: scripts/brew-formula.sh <tag> [<owner/repo>]
set -euo pipefail
cd "$(dirname "$0")/.."

die() {
	echo "brew-formula: $*" >&2
	exit 1
}

(($# == 1 || $# == 2)) || die "usage: $0 <tag> [<owner/repo>]"
tag=$1 repo=${2:-al-maisan/node-tracker}
[[ $tag =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.]+)?$ ]] || die "tag '$tag' is not vMAJOR.MINOR.PATCH[-PRE]"
[[ $repo =~ ^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$ ]] || die "repo '$repo' is not owner/name"
[[ -f dist/SHA256SUMS ]] || die "dist/SHA256SUMS not found; run 'make dist VERSION=$tag' first"
rm -f dist/node-tracker.rb

version=${tag#v}
# Prints the url and sha256 lines of one archive, indented for the formula.
archive() {
	local name=node-tracker_${version}_$1.tar.gz sum
	sum=$(awk -v n="$name" '$2 == n || $2 == "*" n { print $1 }' dist/SHA256SUMS)
	[[ $sum =~ ^[0-9a-f]{64}$ ]] || die "no single valid SHA-256 for $name in dist/SHA256SUMS"
	printf '      url "https://github.com/%s/releases/download/%s/%s"\n      sha256 "%s"\n' "$repo" "$tag" "$name" "$sum"
}

# Homebrew has no 32-bit ARM, so no linux_armv7. One per line:
# 'a=$(x) b=$(y)' would hide a failing x.
darwin_arm64=$(archive darwin_arm64)
darwin_amd64=$(archive darwin_amd64)
linux_arm64=$(archive linux_arm64)
linux_amd64=$(archive linux_amd64)

tmp=$(mktemp dist/node-tracker.rb.XXXXXX)
trap 'rm -f "$tmp"' EXIT
cat >"$tmp" <<EOF
class NodeTracker < Formula
  desc "Telegram alerts when a Bitcoin node falls behind the chain tip"
  homepage "https://github.com/$repo"
  version "$version"
  license "GPL-3.0-only"

  on_macos do
    on_arm do
$darwin_arm64
    end
    on_intel do
$darwin_amd64
    end
  end

  on_linux do
    on_arm do
$linux_arm64
    end
    on_intel do
$linux_amd64
    end
  end

  def install
    bin.install "node-tracker"
    doc.install "README.md", "assets", "docs"
    pkgshare.install "deploy", "examples"
  end

  test do
    assert_match "node-tracker v#{version} (", shell_output("#{bin}/node-tracker version")
  end
end
EOF
chmod 644 "$tmp"
mv "$tmp" dist/node-tracker.rb
echo "brew-formula: dist/node-tracker.rb"
