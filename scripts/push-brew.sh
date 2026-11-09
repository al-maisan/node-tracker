#!/usr/bin/env bash
# Publishes the Homebrew formula of the stable release <tag> to
# <owner>/homebrew-tap. The formula is written from the SHA256SUMS of the
# GitHub release, checked against the release key, so this can be rerun at
# any time. The tap is changed only if the formula differs, and never moved
# back to an older version. 'make release' runs it; by hand:
# 'make brew TAG=vX.Y.Z'. Needs gh, gpg and git (the commit follows the
# git config, e.g. signing). Usage: scripts/push-brew.sh <tag>
set -euo pipefail
cd "$(dirname "$0")/.."

# shellcheck source=release-key.sh
source scripts/release-key.sh
# shellcheck source=release-lib.sh
source scripts/release-lib.sh

die() {
	echo "push-brew: $*" >&2
	exit 1
}

(($# == 1)) || die "usage: $0 <tag>"
tag=$1
[[ $tag =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]] || die "'$tag' is not a stable tag vMAJOR.MINOR.PATCH"
repo=$(origin_repo) || exit 1
tap=${repo%%/*}/homebrew-tap

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
gh release download "$tag" --repo "$repo" --dir "$tmp" --pattern SHA256SUMS --pattern SHA256SUMS.asc ||
	die "downloading SHA256SUMS of release $tag failed"
gpg --status-fd 1 --verify "$tmp/SHA256SUMS.asc" "$tmp/SHA256SUMS" 2>/dev/null | signed_by_release_key ||
	die "SHA256SUMS of release $tag is not signed by $release_key"
scripts/brew-formula.sh "$tag" "$repo" "$tmp" >/dev/null

git clone --quiet --depth 1 "https://github.com/$tap.git" "$tmp/tap" || die "cloning $tap failed"
f=$tmp/tap/Formula/node-tracker.rb
have=$(formula_version "$f")
brew_newer "$tag" "$have" || {
	echo "push-brew: $tap has $have, newer than $tag, so it stays"
	exit 0
}
if cmp -s "$tmp/node-tracker.rb" "$f"; then
	echo "push-brew: $tap already has $tag"
	exit 0
fi
mkdir -p "$tmp/tap/Formula"
cp "$tmp/node-tracker.rb" "$f"
git -C "$tmp/tap" add Formula/node-tracker.rb
git -C "$tmp/tap" commit --quiet -m "node-tracker $tag" || die "committing to $tap failed"
git -C "$tmp/tap" push --quiet || die "pushing to $tap failed"
echo "push-brew: published $tag to $tap"
