#!/usr/bin/env bash
# Tests for the helpers in copr-build.sh: make test-copr
# No network: feeds releases.json-, Bodhi- and COPR-shaped JSON on stdin.
set -uo pipefail

# shellcheck source=copr-build.sh
source "$(dirname "$0")/copr-build.sh"
set +e # copr-build.sh sets -e; the checks must go on after a failure

fail=0
check() { # check <name> <want> <got>
	if [[ $2 == "$3" ]]; then echo "ok   - $1"; else echo "FAIL - $1: want '$2', got '$3'"; fail=1; fi
}
flat() { paste -sd' '; }

# As of 2026-11-01: 42 released but no longer maintained, 45 maintained but beta only.
releases='[{"version": "45 Beta", "variant": "Workstation"}, {"version": "44", "variant": "Workstation"},
 {"version": "44", "variant": "Server"}, {"version": "43", "variant": "Workstation"}, {"version": "42", "variant": "KDE"}]'
bodhi='{"releases": [
 {"name": "EPEL-9", "id_prefix": "FEDORA-EPEL", "version": "9", "state": "current"},
 {"name": "F43", "id_prefix": "FEDORA", "version": "43", "state": "current"},
 {"name": "F43C", "id_prefix": "FEDORA-CONTAINER", "version": "43", "state": "current"},
 {"name": "F44", "id_prefix": "FEDORA", "version": "44", "state": "current"},
 {"name": "F44F", "id_prefix": "FEDORA-FLATPAK", "version": "44", "state": "current"},
 {"name": "F45", "id_prefix": "FEDORA", "version": "45", "state": "current"},
 {"name": "ELN", "id_prefix": "FEDORA", "version": "eln", "state": "pending"}]}'
builds='{"items": [
 {"id": 6, "state": "failed",    "chroots": ["fedora-44-x86_64"], "source_package": {"version": "1.0.0-1"}},
 {"id": 5, "state": "succeeded", "chroots": ["fedora-43-x86_64", "fedora-43-aarch64"], "source_package": {"version": "1.0.0-1"}},
 {"id": 4, "state": "running",   "chroots": ["fedora-44-aarch64"], "source_package": {"version": "1.0.0-1"}},
 {"id": 3, "state": "succeeded", "chroots": ["fedora-44-x86_64"], "source_package": {"version": "1.0.0-10"}},
 {"id": 2, "state": "importing", "chroots": [], "source_package": null}]}'
want_all="fedora-43-x86_64 fedora-43-aarch64 fedora-44-x86_64 fedora-44-aarch64"

released=$(released_versions <<<"$releases")
check "released: final images only" "42 43 44" "$(flat <<<"$released")"
targets=$(supported_chroots "$released" x86_64 aarch64 <<<"$bodhi")
check "supported: released and maintained, both arches" "$want_all" "$(flat <<<"$targets")"
check "supported: picks up 45 once released" "fedora-43-x86_64 fedora-44-x86_64 fedora-45-x86_64" \
	"$(supported_chroots "$(printf '43\n44\n45')" x86_64 <<<"$bodhi" | flat)"

built=$(built_chroots 1.0.0-1 <<<"$builds")
check "built: succeeded and running count, failed does not" "fedora-43-aarch64 fedora-43-x86_64 fedora-44-aarch64" "$(flat <<<"$built")"
check "built: 1.0.0-1 is not 1.0.0-10" "fedora-44-x86_64" "$(built_chroots 1.0.0-10 <<<"$builds")"
check "built: unknown version" "" "$(built_chroots 2.0.0-1 <<<"$builds")"
check "built: bad JSON fails" 1 "$(built_chroots 1.0.0-1 <<<'<html>' >/dev/null 2>&1; echo $?)"

check "todo: only the missing chroot" "fedora-44-x86_64" "$(missing "$targets" "$built" | flat)"
check "todo: nothing when all are built" "" "$(missing "$targets" "$targets")"
check "todo: everything when nothing is built" "$(sort <<<"$targets" | flat)" "$(missing "$targets" "" | flat)"

check "project chroots" "fedora-43-aarch64 fedora-43-x86_64" \
	"$(project_chroots <<<'{"chroot_repos": {"fedora-43-x86_64": "https://x", "fedora-43-aarch64": "https://y"}}' | flat)"
check "release from the spec template" 1 "$(spec_release)"


# install -t fails on a directory: the spec's docs pattern must match only
# files in this tree (docs/demo/ is a directory).
root=$(cd "$(dirname "$0")/.." && pwd)
pat=$(sed -n 's@.*/docs \(docs/[^ ]*\)$@\1@p' "$root/node-tracker.spec")
check "spec installs docs/*.md" "docs/*.md" "$pat"
dirs=$(cd "$root" && for f in $pat; do [[ -f $f ]] || echo "$f"; done)
check "spec docs pattern matches only files" "" "$dirs"
exit "$fail"
