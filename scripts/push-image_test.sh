#!/usr/bin/env bash
# Tests for push-image.sh and release-lib.sh: make test-ppa
# No network: only the checks before the first download.
set -uo pipefail

dir=$(cd "$(dirname "$0")/.." && pwd)
# shellcheck source=release-lib.sh
source "$dir/scripts/release-lib.sh"
fail=0
check() { # check <name> <want> <got>
	if [[ $2 == "$3" ]]; then echo "ok   - $1"; else echo "FAIL - $1: want '$2', got '$3'"; fail=1; fi
}
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

highest() { printf '%s\n' "$@" | highest_stable_tag; }
check "highest stable tag" v1.2.0 "$(highest v1.1.0 v1.2.0 v1.1.5)"
check "backport is not the highest" v1.2.0 "$(highest v1.2.0 v1.1.5)"
check "versions sort as numbers" v1.10.0 "$(highest v1.9.0 v1.10.0 v1.2.0)"
check "prereleases do not count" v1.0.0 "$(highest v2.0.0-rc1 v1.0.0)"
check "other tags do not count" v1.0.0 "$(highest v1.0.0 v9 x1.2.3 v1.2.3.4)"
check "no stable tag" "" "$(highest v1.0.0-rc1)"

d=sha256:$(printf '%064d' 1)
mkdir "$tmp/one" "$tmp/two" "$tmp/pretty" "$tmp/none"
printf '{"manifests":[{"digest":"%s"}]}' "$d" >"$tmp/one/index.json"
printf '{"manifests":[{"digest":"%s"},{"digest":"%s"}]}' "$d" "$d" >"$tmp/two/index.json"
printf '{\n  "manifests": [\n    {\n      "digest": "%s"\n    }\n  ]\n}\n' "$d" >"$tmp/pretty/index.json"
check "digest of the layout" "$d" "$(oci_digest "$tmp/one")"
check "digest of a pretty index" "$d" "$(oci_digest "$tmp/pretty")"
check "rejects two images" no "$(oci_digest "$tmp/two" >/dev/null 2>&1 && echo yes || echo no)"
check "rejects no index" no "$(oci_digest "$tmp/none" >/dev/null 2>&1 && echo yes || echo no)"

repo() { git -C "$tmp/git" remote set-url origin "$1" && (cd "$tmp/git" && origin_repo 2>/dev/null) || echo fails; }
git init -q "$tmp/git" && git -C "$tmp/git" remote add origin x
check "origin over https" al-maisan/node-tracker "$(repo https://github.com/al-maisan/node-tracker.git)"
check "origin over ssh" al-maisan/node-tracker "$(repo git@github.com:al-maisan/node-tracker.git)"
check "origin elsewhere" fails "$(repo https://gitlab.com/a/b/c.git)"

pushes() { if "$dir/scripts/push-image.sh" "$@" >/dev/null 2>&1; then echo yes; else echo no; fi; }
check "push: needs a tag" no "$(pushes)"
check "push: rejects a prerelease" no "$(pushes v1.2.3-rc1)"
check "push: rejects a bad tag" no "$(pushes 'v1.2;x')"
check "push: says why" "push-image: 'v1.2.3-rc1' is not a stable tag vMAJOR.MINOR.PATCH" "$("$dir/scripts/push-image.sh" v1.2.3-rc1 2>&1)"

exit "$fail"
