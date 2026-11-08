#!/usr/bin/env bash
# Tests for the upload detection in build-ppa.sh: make test-ppa
# No network: feeds Launchpad-shaped records and pages; curl and lp_page are stubbed.
set -uo pipefail

# shellcheck source=build-ppa.sh
source "$(dirname "$0")/build-ppa.sh"
set +e # build-ppa.sh sets -e; the checks must go on after a failure

fail=0
check() { # check <name> <want> <got>
	if [[ $2 == "$3" ]]; then echo "ok   - $1"; else echo "FAIL - $1: want '$2', got '$3'"; fail=1; fi
}
yes_no() { if "$@"; then echo yes; else echo no; fi; }
quiet() { "$@" >/dev/null; }

records='1.0.0-1ppa1~resolute1 Published
0.9.0-1ppa1~resolute1 Superseded
1.0.0-1ppa3~questing1 Pending
1.0.0-1ppa4~resolute1 Deleted
1.0.01-1ppa9~resolute1 Published
1x0y0-1ppa9~noble1 Published
- -'

all=$(uploaded_versions 1.0.0 <<<"$records")
up=$(uploaded_versions 1.0.0 live <<<"$records")
check "only this version's uploads" "1.0.0-1ppa1~resolute1 1.0.0-1ppa3~questing1 1.0.0-1ppa4~resolute1" "$(paste -sd' ' <<<"$all")"
check "live: not the deleted one" "1.0.0-1ppa1~resolute1 1.0.0-1ppa3~questing1" "$(paste -sd' ' <<<"$up")"
check "uploaded for resolute" yes "$(yes_no uploaded_for resolute "$up")"
check "uploaded for questing (pending)" yes "$(yes_no uploaded_for questing "$up")"
check "not uploaded for noble" no "$(yes_no uploaded_for noble "$up")"
check "next revision above the highest, deleted included" 5 "$(next_rev "$all")"
check "only deleted: not uploaded" no "$(yes_no uploaded_for resolute "$(uploaded_versions 1.0.0 live <<<'1.0.0-1ppa1~resolute1 Deleted')")"

up=$(uploaded_versions 1.1.0 <<<"$records")
check "new version: nothing uploaded" "" "$up"
check "new version: not uploaded for resolute" no "$(yes_no uploaded_for resolute "$up")"
check "new version: revision 1" 1 "$(next_rev "$up")"
check "empty PPA" "" "$(uploaded_versions 1.0.0 <<<'')"

page='{"entries": [{"source_package_version": "1.0.0-1ppa1~resolute1", "status": "Pending"}, {"source_package_version": null}],
 "next_collection_link": "https://api.launchpad.net/1.0/x?ws.start=300"}'
check "page: records, then the next page" "1.0.0-1ppa1~resolute1 Pending|- -|next https://api.launchpad.net/1.0/x?ws.start=300" "$(page_records <<<"$page" | paste -sd'|')"
check "page: last one" "1.0.0-1ppa1~resolute1 Published" "$(page_records <<<'{"entries": [{"source_package_version": "1.0.0-1ppa1~resolute1", "status": "Published"}]}')"
check "page: a foreign next page fails" no "$(yes_no page_records <<<'{"entries": [], "next_collection_link": "https://evil.example/"}' 2>/dev/null)"
check "page: bad JSON fails" no "$(yes_no page_records <<<'<html>' 2>/dev/null)"
check "page: no entries fails" no "$(yes_no page_records <<<'{}' 2>/dev/null)"

# Two pages, as Launchpad pages them.
# shellcheck disable=SC2329 # stands in for the API in launchpad_records
lp_page() {
	case $1 in
	*ws.start=300) echo '{"entries": [{"source_package_version": "1.0.0-1ppa1~resolute1", "status": "Superseded"}]}' ;;
	*) echo '{"entries": [{"source_package_version": "1.1.0-1ppa1~resolute1", "status": "Published"}], "next_collection_link": "https://api.launchpad.net/1.0/p?ws.start=300"}' ;;
	esac
}
check "every page is read" "1.1.0-1ppa1~resolute1 Published|1.0.0-1ppa1~resolute1 Superseded" "$(launchpad_records | paste -sd'|')"
# shellcheck disable=SC2329
lp_page() { echo '{"entries": [], "next_collection_link": "https://api.launchpad.net/1.0/p?ws.start=300"}'; }
check "endless pages fail" no "$(yes_no launchpad_records 2>/dev/null)"
unset -f lp_page
source "$(dirname "$0")/build-ppa.sh" # the real lp_page again

# curl stands in for the network: it writes $t_hdrs to the -D file and
# prints its arguments.
# shellcheck disable=SC2329
curl() {
	local a prev='' hdrs=''
	for a; do
		[[ $prev == -D ]] && hdrs=$a
		prev=$a
	done
	printf '%b' "$t_hdrs" >"$hdrs"
	printf '%s\n' "$@"
}
t_hdrs='HTTP/1.1 200 OK\r\nX-Cache: MISS from proxy\r\n\r\n'
args=$(lp_page https://api.launchpad.net/1.0/p)
check "page: past the proxy cache" yes "$(yes_no grep -qx 'Cache-Control: no-cache' <<<"$args")"
check "page: fails on HTTP errors" yes "$(yes_no grep -qx -- -fsS <<<"$args")"
check "page: bounded in time" yes "$(yes_no grep -qx -- --max-time <<<"$args")"
t_hdrs='HTTP/1.1 200 OK\r\nAge: 0\r\nX-Cache: MISS from a\r\n\r\n'
check "page: Age 0 passes" yes "$(yes_no quiet lp_page https://api.launchpad.net/1.0/p)"
t_hdrs='HTTP/1.1 200 OK\r\nX-Cache: HIT from proxy\r\nAge: 0\r\n\r\n'
check "page: a cache hit fails" no "$(yes_no lp_page https://api.launchpad.net/1.0/p 2>/dev/null)"
t_hdrs='HTTP/1.1 200 OK\r\nX-Cache: MISS from a\r\nx-cache: HIT from b\r\n\r\n'
check "page: a hit in a second proxy layer fails" no "$(yes_no lp_page https://api.launchpad.net/1.0/p 2>/dev/null)"
t_hdrs='HTTP/1.1 200 OK\r\nAge: 12\r\n\r\n'
check "page: an aged answer fails" no "$(yes_no lp_page https://api.launchpad.net/1.0/p 2>/dev/null)"
unset -f curl
# shellcheck disable=SC2329
lp_page() { printf '%s\n' "$1" >&2; echo '{"entries": []}'; }
url=$(launchpad_records 2>&1 >/dev/null)
check "query: this PPA's node-tracker, every status" \
	"https://api.launchpad.net/1.0/~al-maisan/+archive/ubuntu/node-tracker?ws.op=getPublishedSources&source_name=node-tracker&exact_match=true&ws.size=300" "$url"
unset -f lp_page

log=$(mktemp)
now=1800000000
printf '%s\n' "1.0.0-1ppa2~resolute1 $((now - 60))" "1.0.0-1ppa1~resolute1 $((now - 30))" "0.9.0-1ppa1~resolute1 $((now - 7200))" "junk" >"$log"
check "sent: recent and not listed" "1.0.0-1ppa2~resolute1" "$(unlisted_sent "$log" "$now" "1.0.0-1ppa1~resolute1")"
check "sent: none once listed" "" "$(unlisted_sent "$log" "$now" "$(printf '1.0.0-1ppa1~resolute1\n1.0.0-1ppa2~resolute1')")"
check "sent: no log" "" "$(unlisted_sent "$log.none" "$now" "")"
check "taken: listed and sent, any age" "1.0.0-1ppa1~resolute1 1.0.0-1ppa2~resolute1 1.0.0-1ppa1~resolute1" \
	"$(taken_versions 1.0.0 "1.0.0-1ppa1~resolute1 Deleted" "$log" | paste -sd' ')"
check "taken: sent long ago still takes its revision" 3 "$(next_rev "$(taken_versions 1.0.0 "" "$log")")"
check "taken: no log" "1.0.0-1ppa1~resolute1" "$(taken_versions 1.0.0 "1.0.0-1ppa1~resolute1 Published" "$log.none")"
rm -f "$log"

k=$release_key
check "release key signed (primary)" yes "$(yes_no signed_by_release_key <<<"[GNUPG:] VALIDSIG $k 2026-11-01 0 0 4 0 1 10 00 $k")"
check "release key signed (subkey)" yes "$(yes_no signed_by_release_key <<<"[GNUPG:] VALIDSIG ABCD 2026-11-01 0 0 4 0 1 10 00 $k")"
check "other key rejected" no "$(yes_no signed_by_release_key <<<'[GNUPG:] VALIDSIG ABCD 2026-11-01 0 0 4 0 1 10 00 ABCD')"
check "bad signature rejected" no "$(yes_no signed_by_release_key <<<"[GNUPG:] BADSIG $k x")"

exit "$fail"
