#!/usr/bin/env bash
# Writes the RPM spec of a stable release tag from the node-tracker.spec
# template, with the version, the commit and the %changelog.
# Usage: scripts/gen-spec.sh <tag> <out.spec>
set -euo pipefail
cd "$(dirname "$0")/.."

die() {
	echo "gen-spec: $*" >&2
	exit 1
}

(($# == 2)) || die "usage: $0 <tag> <out.spec>"
tag=$1 out=$2
[[ $tag =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]] || die "tag '$tag' is not vMAJOR.MINOR.PATCH"
git rev-parse -q --verify "refs/tags/$tag^{commit}" >/dev/null || die "tag '$tag' not found"
[[ $out == *.spec && -d $(dirname "$out") ]] || die "'$out' is not a .spec in an existing directory"

commit=$(git rev-parse --short "$tag^{commit}")
changelog=$(scripts/pkg-changelog.sh rpm "$tag")
tmp=$(mktemp "$out.XXXXXX")
trap 'rm -f "$tmp"' EXIT
# ENVIRON, unlike awk -v, takes the values as they are.
VERSION=${tag#v} COMMIT=$commit CHANGELOG=$changelog awk '
	{ gsub(/@VERSION@/, ENVIRON["VERSION"]); gsub(/@COMMIT@/, ENVIRON["COMMIT"]) }
	$0 == "@CHANGELOG@" { print ENVIRON["CHANGELOG"]; next }
	{ print }' node-tracker.spec >"$tmp"
if grep -n '@[A-Z]*@' "$tmp" >&2; then die "placeholder left in the spec"; fi
chmod 644 "$tmp"
mv "$tmp" "$out"
echo "gen-spec: $out" >&2
