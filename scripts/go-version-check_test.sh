#!/usr/bin/env bash
# Tests for go-version-check.sh: make test-ppa
set -uo pipefail

dir=$(cd "$(dirname "$0")/.." && pwd)
fail=0
check() { # check <name> <want> <got>
	if [[ $2 == "$3" ]]; then echo "ok   - $1"; else echo "FAIL - $1: want '$2', got '$3'"; fail=1; fi
}
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
cat >"$tmp/dl.json" <<'JSON'
[
 {
  "version": "go1.27.4",
  "stable": true
 },
 {
  "version": "go1.26.11",
  "stable": true
 }
]
JSON
gvc() { # gvc <.go-version>; prints the exit code and the message
	printf '%s\n' "$1" >"$tmp/go-version"
	local out
	out=$(GO_VERSION_FILE=$tmp/go-version GO_DL_JSON=$tmp/dl.json "$dir/scripts/go-version-check.sh" 2>&1)
	echo "$? $out"
}

check "latest" "0 go-version-check: go1.26.11 is the latest go1.26" "$(gvc 1.26.11)"
check "patch behind" "1 go-version-check: go1.26.11 is out: bump .go-version (go1.26.10)" "$(gvc 1.26.10)"
check "patch 9 vs 11 (not string order)" "1 go-version-check: go1.26.11 is out: bump .go-version (go1.26.9)" "$(gvc 1.26.9)"
check "ahead of the list" "0 go-version-check: go1.26.12 is the latest go1.26" "$(gvc 1.26.12)"
check "unsupported minor" "1 go-version-check: go1.25 is no longer supported: move .go-version to a supported Go" "$(gvc 1.25.9)"
check "no prefix match (1.2 vs 1.26)" "1 go-version-check: go1.2 is no longer supported: move .go-version to a supported Go" "$(gvc 1.2.3)"
check "bad .go-version" "1 go-version-check: .go-version 'go1.26.11' is not MAJOR.MINOR.PATCH" "$(gvc go1.26.11)"
printf '[]\n' >"$tmp/dl.json"
check "empty list" "1 go-version-check: no Go versions in the release list (format changed?)" "$(gvc 1.26.11)"
printf '<html>Go downloads</html>\n' >"$tmp/dl.json"
check "HTML page" "1 go-version-check: no Go versions in the release list (format changed?)" "$(gvc 1.26.11)"
exit $fail
