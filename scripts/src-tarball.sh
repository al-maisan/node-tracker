#!/usr/bin/env bash
# Writes the source tarball of the deb and rpm packages: the tree of a stable
# release tag plus the vendored Go modules (the build servers have no
# network), as node-tracker-<version>/. The same tag gives the same tarball.
# Usage: scripts/src-tarball.sh <tag> <out.tar.gz>
set -euo pipefail
cd "$(dirname "$0")/.."

die() {
	echo "src-tarball: $*" >&2
	exit 1
}

(($# == 2)) || die "usage: $0 <tag> <out.tar.gz>"
tag=$1 out=$2
# Packages get stable releases only, as Homebrew does.
[[ $tag =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]] || die "tag '$tag' is not vMAJOR.MINOR.PATCH"
git rev-parse -q --verify "refs/tags/$tag^{commit}" >/dev/null || die "tag '$tag' not found"
[[ $out == *.tar.gz && -d $(dirname "$out") ]] || die "'$out' is not a .tar.gz in an existing directory"

name=node-tracker-${tag#v}
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
git archive --prefix="$name/" "$tag" | tar -xf - -C "$tmp"
# go.sum pins every module, so the vendored tree is the same on every run.
(cd "$tmp/$name" && GOFLAGS=-mod=mod go mod vendor)
tar --sort=name --owner=0 --group=0 --numeric-owner --mode=go-w,a+rX \
	--mtime="@$(git log -1 --format=%ct "$tag^{commit}")" -C "$tmp" -cf - "$name" | gzip -9n >"$tmp/out.tar.gz"
mv "$tmp/out.tar.gz" "$out"
echo "src-tarball: $out"
