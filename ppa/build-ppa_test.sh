#!/usr/bin/env bash
# Tests for the upload detection in build-ppa.sh: make test-ppa
# No network: feeds getPublishedSources-shaped JSON on stdin; curl is stubbed.
set -uo pipefail

# shellcheck source=build-ppa.sh
source "$(dirname "$0")/build-ppa.sh"
set +e # build-ppa.sh sets -e; the checks must go on after a failure

fail=0
check() { # check <name> <want> <got>
	if [[ $2 == "$3" ]]; then echo "ok   - $1"; else echo "FAIL - $1: want '$2', got '$3'"; fail=1; fi
}
yes_no() { if "$@"; then echo yes; else echo no; fi; }

json='{"entries": [
 {"source_package_name": "node-tracker", "source_package_version": "1.0.0-1ppa1~resolute1"},
 {"source_package_name": "node-tracker", "source_package_version": "0.9.0-1ppa1~resolute1"},
 {"source_package_name": "node-tracker", "source_package_version": "1.0.0-1ppa3~questing1"},
 {"source_package_name": "node-tracker", "source_package_version": "1.0.01-1ppa9~resolute1"},
 {"source_package_name": "node-tracker", "source_package_version": "1x0y0-1ppa9~noble1"},
 {"source_package_name": "node-tracker", "source_package_version": null}
]}'

up=$(uploaded_versions 1.0.0 <<<"$json")
check "only this version's uploads" "1.0.0-1ppa1~resolute1 1.0.0-1ppa3~questing1" "$(paste -sd' ' <<<"$up")"
check "uploaded for resolute" yes "$(yes_no uploaded_for resolute "$up")"
check "uploaded for questing" yes "$(yes_no uploaded_for questing "$up")"
check "not uploaded for noble" no "$(yes_no uploaded_for noble "$up")"
check "next revision above the highest" 4 "$(next_rev "$up")"

up=$(uploaded_versions 1.1.0 <<<"$json")
check "new version: nothing uploaded" "" "$up"
check "new version: not uploaded for resolute" no "$(yes_no uploaded_for resolute "$up")"
check "new version: revision 1" 1 "$(next_rev "$up")"

check "empty PPA" "" "$(uploaded_versions 1.0.0 <<<'{"entries": []}')"
check "bad JSON fails" no "$(yes_no uploaded_versions 1.0.0 <<<'<html>' 2>/dev/null)"

# shellcheck disable=SC2329 # stands in for curl in launchpad_uploads
curl() { printf '%s\n' "$@"; }
args=$(launchpad_uploads)
check "uploads: bypasses the proxy cache" yes "$(yes_no grep -qx 'Cache-Control: no-cache' <<<"$args")"
check "uploads: every status (none filtered)" no "$(yes_no grep -q 'status=' <<<"$args")"
unset -f curl

k=$release_key
check "release key signed (primary)" yes "$(yes_no signed_by_release_key <<<"[GNUPG:] VALIDSIG $k 2026-11-01 0 0 4 0 1 10 00 $k")"
check "release key signed (subkey)" yes "$(yes_no signed_by_release_key <<<"[GNUPG:] VALIDSIG ABCD 2026-11-01 0 0 4 0 1 10 00 $k")"
check "other key rejected" no "$(yes_no signed_by_release_key <<<'[GNUPG:] VALIDSIG ABCD 2026-11-01 0 0 4 0 1 10 00 ABCD')"
check "bad signature rejected" no "$(yes_no signed_by_release_key <<<"[GNUPG:] BADSIG $k x")"

exit "$fail"
