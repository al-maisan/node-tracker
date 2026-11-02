#!/usr/bin/env bash
# Prints a package changelog from the GitHub releases up to a stable tag:
#   rpm <tag>                    the RPM %changelog: one entry per release
#   deb <tag> <version> <dist>   the debian/changelog entry for <tag>
# An entry lists the commits since the previous release (or the last 20 for
# the first one). The tag counts as released even before its GitHub release
# exists. Dates are commit times, so a rebuild gives the same changelog.
set -euo pipefail
cd "$(dirname "$0")/.."

maintainer="Muharem Hrnjadovic <muharem@linux.com>"
repo=al-maisan/node-tracker
stable='^v[0-9]+\.[0-9]+\.[0-9]+$'

die() {
	echo "pkg-changelog: $*" >&2
	exit 1
}

# Reads release tags on stdin; prints the stable ones up to <tag>, plus <tag>,
# newest first.
releases_upto() {
	{
		grep -E "$stable" || true
		echo "$1"
	} | sort -uV | awk -v t="$1" '{ print } $0 == t { exit }' | tac
}

# Prints the commit subjects of release <tag> as items starting with <bullet>.
# <prev> is the previous release, or empty.
items() {
	local bullet=$1 tag=$2 prev=$3 lines
	if [[ -n $prev ]]; then
		lines=$(git log --no-merges --format=%s "$prev..$tag")
	else
		lines=$(git log --no-merges --format=%s --max-count=20 "$tag")
	fi
	if [[ -z $lines ]]; then lines="Release $tag."; fi
	# shellcheck disable=SC2001 # prefixes every line
	sed "s/^/$bullet/" <<<"$lines"
}

# Prints the commit time of <tag> in the strftime <format>, in UTC.
tag_date() {
	LC_ALL=C TZ=UTC git log -1 --format=%cd --date="format-local:$2" "$1^{commit}"
}

# Escapes '%' on stdin as '%%': rpm expands macros in %changelog, so a commit
# subject like "%(cmd)" would run cmd during the build. '$' needs nothing.
rpm_escape() {
	sed 's/%/%%/g'
}

# Reads a page of GitHub's releases JSON on stdin; prints the tag names of
# the published ones. Fails on anything else.
release_tags() {
	python3 -I -c '
import json, sys
for r in json.load(sys.stdin):
    t = r.get("tag_name")
    if not r.get("draft") and isinstance(t, str):
        print(t)
'
}

# Prints the RPM %changelog for the release list (newest first) on stdin.
rpm_changelog() {
	local -a tags
	local i
	mapfile -t tags
	for i in "${!tags[@]}"; do
		echo "* $(tag_date "${tags[i]}" '%a %b %d %Y') $maintainer - ${tags[i]#v}-1"
		items '- ' "${tags[i]}" "${tags[i + 1]:-}"
		echo
	done | rpm_escape
}

# Prints the debian/changelog entry for <tag>, given the release list (newest
# first) on stdin.
deb_entry() {
	local tag=$1 version=$2 dist=$3 prev
	prev=$(sed -n 2p)
	echo "node-tracker ($version) $dist; urgency=medium"
	echo
	items '  * ' "$tag" "$prev"
	echo
	echo " -- $maintainer  $(tag_date "$tag" '%a, %d %b %Y %H:%M:%S +0000')"
}

# The functions above are sourced by pkg-changelog_test.sh; stop here in that case.
if [[ ${BASH_SOURCE[0]} != "$0" ]]; then return 0; fi

(($# >= 2)) || die "usage: $0 rpm <tag> | deb <tag> <version> <dist>"
format=$1 tag=$2
[[ $tag =~ $stable ]] || die "tag '$tag' is not vMAJOR.MINOR.PATCH"
git rev-parse -q --verify "refs/tags/$tag^{commit}" >/dev/null || die "tag '$tag' not found"
# Public data: the API needs no login (60 calls an hour per address).
released=
for page in {1..10}; do
	json=$(curl -fsS -H 'Accept: application/vnd.github+json' "https://api.github.com/repos/$repo/releases?per_page=100&page=$page") ||
		die "cannot list the GitHub releases of $repo"
	page_tags=$(release_tags <<<"$json") || die "unexpected answer listing the GitHub releases of $repo"
	[[ -n $page_tags ]] || break
	released+=$page_tags$'\n'
done
releases=$(releases_upto "$tag" <<<"$released")
for t in $releases; do
	git rev-parse -q --verify "refs/tags/$t^{commit}" >/dev/null || die "released tag '$t' is not local; fetch the tags first"
done

case $format in
rpm)
	(($# == 2)) || die "usage: $0 rpm <tag>"
	rpm_changelog <<<"$releases"
	;;
deb)
	(($# == 4)) || die "usage: $0 deb <tag> <version> <dist>"
	[[ $3 =~ ^[0-9][0-9A-Za-z.+~-]*$ ]] || die "invalid version '$3'"
	[[ $4 =~ ^[a-z]+$ ]] || die "invalid distribution '$4'"
	deb_entry "$tag" "$3" "$4" <<<"$releases"
	;;
*) die "unknown format '$format'" ;;
esac
