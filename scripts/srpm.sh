#!/usr/bin/env bash
# Builds the source RPM of a stable release tag in the per-user
# ~/.cache/node-tracker/rpmbuild and prints its path. Run via 'make srpm'.
# Usage: scripts/srpm.sh <tag>
set -euo pipefail
cd "$(dirname "$0")/.."

die() {
	echo "srpm: $*" >&2
	exit 1
}

(($# == 1)) || die "usage: $0 <tag>"
tag=$1
# shellcheck source=release-key.sh
source scripts/release-key.sh
# COPR signs what it builds: build only what the release key tagged.
tag_signed "$tag" . || die "tag '$tag' is not signed by $release_key"
# A fixed /tmp path could be pre-created by another user.
dir=${XDG_CACHE_HOME:-$HOME/.cache}/node-tracker/rpmbuild
rm -rf "$dir"
install -d -m 700 "$dir"
mkdir "$dir/SOURCES" "$dir/SPECS" "$dir/SRPMS"
scripts/gen-spec.sh "$tag" "$dir/SPECS/node-tracker.spec"
scripts/src-tarball.sh "$tag" "$dir/SOURCES/node-tracker-${tag#v}.tar.gz" >&2
install -m 644 scripts/install-units.sh "$dir/SOURCES/install-units.sh"
install -m 644 deploy/sysusers.d/node-tracker.conf "$dir/SOURCES/node-tracker.sysusers"
rpmbuild -bs --define "_topdir $dir" "$dir/SPECS/node-tracker.spec" >&2
srpms=("$dir"/SRPMS/node-tracker-"${tag#v}"-*.src.rpm)
[[ -f ${srpms[0]} && ${#srpms[@]} == 1 ]] || die "expected one source RPM in $dir/SRPMS"
echo "${srpms[0]}"
