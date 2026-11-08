#!/usr/bin/env bash
# Builds the signed source package of a stable release tag for each Ubuntu
# release and uploads it to the PPA. Run via 'make ppa'.
# Usage: ppa/build-ppa.sh <tag> [<ppa-rev>]
# Without <ppa-rev>, Ubuntu releases that already have the tag's version are
# skipped. With it, all are built as <version>-1ppa<ppa-rev>~<release>1.
# Needs go, curl, python3, debuild, dput and the release key.
set -euo pipefail

lp_owner=al-maisan
lp_ppa=node-tracker
# shellcheck source=../scripts/release-key.sh
source "$(dirname "${BASH_SOURCE[0]}")/../scripts/release-key.sh"
releases=(resolute) # 26.04 LTS

die() {
	echo "build-ppa: $*" >&2
	exit 1
}

lp_api=https://api.launchpad.net/1.0/
# Uploads sent from here: Launchpad lists one only after processing it,
# minutes later.
sent_log=${XDG_CACHE_HOME:-$HOME/.cache}/node-tracker/ppa-sent

# Prints the JSON at <url> of Launchpad's API. no-cache: Launchpad's proxy
# serves anonymous reads minutes stale, which hides a fresh upload; a cached
# answer anyway (a HIT or an Age from any proxy layer) is an error.
lp_page() {
	local hdrs out cached
	hdrs=$(mktemp) || return 1
	out=$(curl -fsS --connect-timeout 10 --max-time 60 -H 'Cache-Control: no-cache' -D "$hdrs" "$1") || {
		rm -f "$hdrs"
		return 1
	}
	cached=$(grep -iE '^(x-cache: *hit|age: *[1-9])' "$hdrs" | tr -d '\r' | paste -sd' ')
	rm -f "$hdrs"
	if [[ -n $cached ]]; then
		echo "build-ppa: Launchpad's proxy answered from its cache ($cached)" >&2
		return 1
	fi
	echo "$out"
}

# Reads a getPublishedSources page on stdin; prints "<version> <status>"
# per upload, then "next <url>" if there are more pages.
page_records() {
	python3 -I -c '
import json, sys
d = json.load(sys.stdin)
for e in d["entries"]:
    print(e.get("source_package_version") or "-", e.get("status") or "-")
n = d.get("next_collection_link")
if n:
    if not n.startswith(sys.argv[1]):
        sys.exit("unexpected next page: " + n)
    print("next", n)
' "$lp_api"
}

# Prints "<version> <status>" for every upload to the PPA, newest first.
launchpad_records() {
	local url=$lp_api"~$lp_owner/+archive/ubuntu/$lp_ppa?ws.op=getPublishedSources&source_name=node-tracker&exact_match=true&ws.size=300"
	local page recs n
	for ((n = 0; n < 20; n++)); do
		page=$(lp_page "$url") || return 1
		recs=$(page_records <<<"$page") || return 1
		grep -v '^next ' <<<"$recs" || true
		url=$(sed -n 's/^next //p' <<<"$recs")
		[[ -n $url ]] || return 0
	done
	echo "build-ppa: more than $n pages of uploads" >&2
	return 1
}

# Reads launchpad_records on stdin; prints the package versions uploaded for
# upstream <version>. With "live", not the Deleted or Obsolete ones.
uploaded_versions() {
	local v s
	while read -r v s; do
		[[ $v =~ ^${1//./\\.}-1ppa[0-9]+~[a-z]+[0-9]+$ ]] || continue
		[[ ${2:-} == live && ($s == Deleted || $s == Obsolete) ]] && continue
		echo "$v"
	done
	return 0
}

# Prints the package versions of upstream <version> that took a revision:
# every upload in <records> (Deleted too), and every send in <log>, which
# Launchpad may never list (rejected, or still queued).
taken_versions() {
	{
		echo "$2"
		[[ ! -f $3 ]] || cat "$3"
	} | uploaded_versions "$1"
}

# Prints the uploads in <log> sent less than an hour before <now> that are
# not in <listed> (versions, one per line).
unlisted_sent() {
	local v t
	[[ -f $1 ]] || return 0
	while read -r v t; do
		[[ $t =~ ^[0-9]+$ ]] && (($2 - t < 3600)) && ! grep -qxF "$v" <<<"$3" && echo "$v"
	done <"$1"
	return 0
}

# True if <uploaded> (from uploaded_versions) has a version for <release>.
uploaded_for() {
	grep -qE "~$1[0-9]+$" <<<"$2"
}

# Prints the ppa revision above the highest in <uploaded>, or 1.
next_rev() {
	local top
	top=$(sed -n 's/.*-1ppa\([0-9]*\)~.*/\1/p' <<<"$1" | sort -n | tail -1)
	echo $((${top:-0} + 1))
}

# The functions above are sourced by build-ppa_test.sh; stop here in that case.
if [[ ${BASH_SOURCE[0]} != "$0" ]]; then return 0; fi

(($# == 1 || $# == 2)) || die "usage: $0 <tag> [<ppa-rev>]"
tag=$1 rev=${2:-}
[[ $tag =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]] || die "tag '$tag' is not vMAJOR.MINOR.PATCH"
[[ -z $rev || $rev =~ ^[1-9][0-9]{0,3}$ ]] || die "ppa-rev '$rev' is not a positive number"
src=$(cd "$(dirname "$0")/.." && pwd)
git -C "$src" rev-parse -q --verify "refs/tags/$tag^{commit}" >/dev/null || die "tag '$tag' not found"
# The upload is signed with the release key: sign only what it tagged.
tag_signed "$tag" "$src" || die "tag '$tag' is not signed by $release_key"
version=${tag#v}
commit=$(git -C "$src" rev-parse --short "$tag^{commit}")

# Fail loudly: guessing "nothing uploaded" would build a new orig tarball,
# which Launchpad rejects.
records=$(launchpad_records) || die "cannot ask Launchpad for the uploads"
recent=$(unlisted_sent "$sent_log" "$(date +%s)" "$(cut -d' ' -f1 <<<"$records")")
[[ -z $recent ]] || die "sent less than an hour ago and not listed by Launchpad yet: $(paste -sd' ' <<<"$recent"). Rerun once Launchpad lists it; after a rejection mail, delete its line in $sent_log"
# A deleted upload still takes its revision, but doesn't count as uploaded.
all=$(taken_versions "$version" "$records" "$sent_log")
uploaded=$(uploaded_versions "$version" live <<<"$records")

todo=("${releases[@]}")
if [[ -z $rev ]]; then
	rev=$(next_rev "$all")
	todo=()
	for r in "${releases[@]}"; do
		if uploaded_for "$r" "$uploaded"; then
			echo "build-ppa: $r has $version already: $(grep "~$r" <<<"$uploaded" | paste -sd' ')"
		else
			todo+=("$r")
		fi
	done
	if ((${#todo[@]} == 0)); then
		echo "build-ppa: nothing to do. To rebuild: make ppa PPA_REV=$rev"
		exit 0
	fi
fi

# Per-user build dir: a fixed /tmp path could be pre-created by another user.
build=${XDG_CACHE_HOME:-$HOME/.cache}/node-tracker/ppa
rm -rf "$build"
install -d -m 700 "$build"
orig=node-tracker_$version.orig.tar.gz
# Launchpad keeps one orig tarball per version: a rebuild must reuse it.
if [[ -n $uploaded ]]; then
	echo "build-ppa: reusing the published $orig"
	curl -fsSL --connect-timeout 10 --max-time 600 -o "$build/$orig" "https://launchpad.net/~$lp_owner/+archive/ubuntu/$lp_ppa/+files/$orig" ||
		die "cannot download the published $orig (not published yet? rerun later)"
	# It gets signed again below: it must be exactly the tag's tarball.
	"$src/scripts/src-tarball.sh" "$tag" "$build/local.tar.gz"
	[[ $(sha256sum <"$build/$orig") == "$(sha256sum <"$build/local.tar.gz")" ]] ||
		die "the published $orig differs from the tag's tarball (another Go version vendors differently?)"
	rm "$build/local.tar.gz"
	include_orig=-sd
else
	"$src/scripts/src-tarball.sh" "$tag" "$build/$orig"
	include_orig=-sa
fi
tar -xzf "$build/$orig" -C "$build"
[[ -d $build/node-tracker-$version ]] || die "$orig has no node-tracker-$version/"

for r in "${todo[@]}"; do
	full=$version-1ppa$rev~${r}1
	echo "build-ppa: building $full"
	dir=$build/$r
	cp -a "$build/node-tracker-$version" "$dir"
	# The packaging comes from this checkout, the sources from the tag.
	cp -a "$src/ppa/debian" "$dir/debian"
	install -m 755 "$src/scripts/install-units.sh" "$dir/debian/install-units.sh"
	install -m 755 "$src/scripts/deb-layout.sh" "$dir/debian/deb-layout.sh"
	install -m 644 "$src/deploy/sysusers.d/node-tracker.conf" "$dir/debian/node-tracker.sysusers"
	sed -i "s/@COMMIT@/$commit/" "$dir/debian/rules"
	"$src/scripts/pkg-changelog.sh" deb "$tag" "$full" "$r" >"$dir/debian/changelog"
	# -d: the build dependencies are needed on Launchpad, not here.
	(cd "$dir" && debuild -S "$include_orig" -d -k"$release_key")
	# Logged before: a dput that fails after sending still counts.
	mkdir -p "${sent_log%/*}"
	echo "$full $(date +%s)" >>"$sent_log"
	dput "ppa:$lp_owner/$lp_ppa" "$build/node-tracker_${full}_source.changes"
done
echo "build-ppa: uploaded; see https://launchpad.net/~$lp_owner/+archive/ubuntu/$lp_ppa/+packages"
