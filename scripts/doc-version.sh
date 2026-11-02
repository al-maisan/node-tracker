#!/usr/bin/env bash
# The release version in the download examples of the docs.
#   doc-version.sh set X.Y.Z    rewrites them; run before tagging vX.Y.Z
#   doc-version.sh check X.Y.Z  fails if one shows another version
# Only the examples: "node-tracker_<version>_" and "node-tracker_<version>-1_"
# file names, and "example: v<version>". History ("from v1.1.0 on") and the
# v1.2.3 placeholders stay. check also fails if it finds no example at all,
# so a rewording can't drop the guard of make release.
set -euo pipefail
cd "${DOC_ROOT:-$(dirname "$0")/..}"

files=(README.md docs/install.md)
ver='[0-9]+\.[0-9]+\.[0-9]+'
usage() {
	echo "usage: $0 set|check X.Y.Z" >&2
	exit 2
}
(($# == 2)) && [[ $1 == set || $1 == check ]] && [[ $2 =~ ^$ver$ ]] || usage
v=$2
for f in "${files[@]}"; do
	[[ -f $f ]] || {
		echo "doc-version: $f is missing" >&2
		exit 1
	}
done
case $1 in
set)
	sed -E -i "s/node-tracker_$ver(-1)?_/node-tracker_$v\1_/g; s/example: v$ver([^0-9]|$)/example: v$v\1/g" "${files[@]}"
	;;
check)
	found=$(grep -nEo "node-tracker_$ver(-1)?_|example: v$ver([^0-9]|$)" "${files[@]}" || true)
	if [[ -z $found ]]; then
		echo "doc-version: no download example in ${files[*]}; were they reworded?" >&2
		exit 1
	fi
	cur=${v//./\\.}
	stale=$(grep -vE "node-tracker_$cur(-1)?_|example: v$cur([^0-9]|$)" <<<"$found" || true)
	if [[ -n $stale ]]; then
		echo "doc-version: the download examples show another version than $v; run: make doc-version V=$v" >&2
		echo "$stale" >&2
		exit 1
	fi
	;;
*) usage ;;
esac
