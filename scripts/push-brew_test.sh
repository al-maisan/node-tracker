#!/usr/bin/env bash
# Tests for push-brew.sh, its release-lib.sh helpers and brew-formula.sh's
# output dir: make test-ppa. No network: only the checks before the download.
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

# A formula written to another dir, from that dir's SHA256SUMS.
for a in darwin_arm64 darwin_amd64 linux_arm64 linux_amd64 linux_armv6; do
	printf '%064d  node-tracker_1.4.0_%s.tar.gz\n' "${#a}" "$a"
done >"$tmp/SHA256SUMS"
"$dir/scripts/brew-formula.sh" v1.4.0 al-maisan/node-tracker "$tmp" >/dev/null
check "formula in the given dir" yes "$([[ -f $tmp/node-tracker.rb ]] && echo yes || echo no)"
check "formula version" 1.4.0 "$(formula_version "$tmp/node-tracker.rb")"
check "formula url" 1 "$(grep -c 'url "https://github.com/al-maisan/node-tracker/releases/download/v1.4.0/node-tracker_1.4.0_linux_amd64.tar.gz"' "$tmp/node-tracker.rb")"
check "no formula without SHA256SUMS" no "$("$dir/scripts/brew-formula.sh" v1.4.0 al-maisan/node-tracker "$tmp/none" >/dev/null 2>&1 && echo yes || echo no)"
check "no version in a missing formula" "" "$(formula_version "$tmp/missing.rb")"

tapv() { tap_version "$1" 2>/dev/null || echo fails; }
printf 'class NodeTracker < Formula\n  version "x"\nend\n' >"$tmp/bad.rb"
check "tap version" 1.4.0 "$(tapv "$tmp/node-tracker.rb")"
check "no formula in the tap yet" "" "$(tapv "$tmp/missing.rb")"
check "unreadable tap version fails" fails "$(tapv "$tmp/bad.rb")"

newer() { if brew_newer "$@"; then echo yes; else echo no; fi; }
check "empty tap" yes "$(newer v1.4.0 "")"
check "newer release" yes "$(newer v1.4.0 1.3.2)"
check "same release" yes "$(newer v1.4.0 1.4.0)"
check "fix release of an older version" no "$(newer v1.3.3 1.4.0)"
check "versions sort as numbers" yes "$(newer v1.10.0 1.9.0)"

pushes() { if "$dir/scripts/push-brew.sh" "$@" >/dev/null 2>&1; then echo yes; else echo no; fi; }
check "push: needs a tag" no "$(pushes)"
check "push: rejects a prerelease" no "$(pushes v1.2.3-rc1)"
check "push: rejects a bad tag" no "$(pushes 'v1.2;x')"
check "push: says why" "push-brew: 'v1.2.3-rc1' is not a stable tag vMAJOR.MINOR.PATCH" "$("$dir/scripts/push-brew.sh" v1.2.3-rc1 2>&1)"

exit "$fail"
