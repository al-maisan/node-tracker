#!/usr/bin/env bash
# Writes the Homebrew formula <dir>/node-tracker.rb for a release tag from
# <dir>/SHA256SUMS; <dir> is dist by default.
# Usage: scripts/brew-formula.sh <tag> [<owner/repo> [<dir>]]
set -euo pipefail
cd "$(dirname "$0")/.."

die() {
	echo "brew-formula: $*" >&2
	exit 1
}

(($# >= 1 && $# <= 3)) || die "usage: $0 <tag> [<owner/repo> [<dir>]]"
tag=$1 repo=${2:-al-maisan/node-tracker} dir=${3:-dist}
[[ $tag =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.]+)?$ ]] || die "tag '$tag' is not vMAJOR.MINOR.PATCH[-PRE]"
[[ $repo =~ ^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$ ]] || die "repo '$repo' is not owner/name"
[[ -f $dir/SHA256SUMS ]] || die "$dir/SHA256SUMS not found; run 'make dist VERSION=$tag' first"
rm -f "$dir/node-tracker.rb"

version=${tag#v}
# Prints the url and sha256 lines of one archive, indented for the formula.
archive() {
	local name=node-tracker_${version}_$1.tar.gz sum
	sum=$(awk -v n="$name" '$2 == n || $2 == "*" n { print $1 }' "$dir/SHA256SUMS")
	[[ $sum =~ ^[0-9a-f]{64}$ ]] || die "no single valid SHA-256 for $name in $dir/SHA256SUMS"
	printf '      url "https://github.com/%s/releases/download/%s/%s"\n      sha256 "%s"\n' "$repo" "$tag" "$name" "$sum"
}

# Homebrew has no 32-bit ARM, so no linux_armv6. One per line:
# 'a=$(x) b=$(y)' would hide a failing x.
darwin_arm64=$(archive darwin_arm64)
darwin_amd64=$(archive darwin_amd64)
linux_arm64=$(archive linux_arm64)
linux_amd64=$(archive linux_amd64)

tmp=$(mktemp "$dir/node-tracker.rb.XXXXXX")
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
    man1.install "node-tracker.1"
    doc.install "README.md", "assets", "docs"
    pkgshare.install "deploy", "examples"
  end

  # brew services start node-tracker: launchd on macOS, a systemd user
  # service on Linux. node-tracker logs to stderr.
  service do
    run [opt_bin/"node-tracker", "run"]
    keep_alive true
    log_path var/"log/node-tracker.log"
    error_log_path var/"log/node-tracker.log"
  end

  test do
    assert_match "node-tracker v#{version} (", shell_output("#{bin}/node-tracker version")
  end
end
EOF
chmod 644 "$tmp"
mv "$tmp" "$dir/node-tracker.rb"
echo "brew-formula: $dir/node-tracker.rb"
