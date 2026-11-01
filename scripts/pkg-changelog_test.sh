#!/usr/bin/env bash
# Tests for pkg-changelog.sh: make test-ppa, make test-copr
# No network: runs the helpers on a throwaway git repo.
set -uo pipefail

# shellcheck source=pkg-changelog.sh
source "$(dirname "$0")/pkg-changelog.sh"
set +e # pkg-changelog.sh sets -e; the checks must go on after a failure

fail=0
check() { # check <name> <want> <got>
	if [[ $2 == "$3" ]]; then echo "ok   - $1"; else echo "FAIL - $1: want '$2', got '$3'"; fail=1; fi
}
flat() { paste -sd' '; }

check "releases: stable only, up to the tag, newest first" "v1.10.0 v1.9.0 v1.2.0" \
	"$(printf 'v1.11.0\nv1.2.0\nv1.10.0\nv1.10.0-rc1\nv1.9.0\nlatest\n' | releases_upto v1.10.0 | flat)"
check "releases: the tag counts before its release" "v1.11.0 v1.10.0" "$(printf 'v1.10.0\n' | releases_upto v1.11.0 | flat)"
check "release tags: published ones only" "v1.1.0 v1.0.0" \
	"$(release_tags <<<'[{"tag_name":"v1.1.0","draft":false},{"tag_name":"v1.2.0","draft":true},{"tag_name":null},{"tag_name":"v1.0.0"}]' | flat)"
check "release tags: none" "" "$(release_tags <<<'[]' | flat)"
check "release tags: not JSON fails" no "$(release_tags <<<'<html>' >/dev/null 2>&1 && echo yes || echo no)"
check "releases: none yet" "v1.0.0" "$(releases_upto v1.0.0 </dev/null | flat)"

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
cd "$tmp" || exit 1
g() { git -c user.name=t -c user.email=t@t -c commit.gpgsign=false -c tag.gpgsign=false "$@"; }
commit() { GIT_COMMITTER_DATE=$1 g commit -q --allow-empty -m "$2"; }
g init -q
commit 2026-10-01T08:00:00+0200 "Initial commit"
g tag v1.0.0
commit 2026-10-20T08:00:00+0200 'Fix 100% of $(id) bugs'
commit 2026-11-01T08:30:00+0100 "Add a feature (#2)"
g tag v1.1.0
g tag v1.1.1

check "deb: commits since the previous release, UTC date" "node-tracker (1.1.0-1ppa1~resolute1) resolute; urgency=medium

  * Add a feature (#2)
  * Fix 100% of \$(id) bugs

 -- Muharem Hrnjadovic <muharem@linux.com>  Sun, 01 Nov 2026 07:30:00 +0000" \
	"$(printf 'v1.1.0\nv1.0.0\n' | deb_entry v1.1.0 1.1.0-1ppa1~resolute1 resolute)"
check "deb: first release lists its commits" "  * Initial commit" "$(deb_entry v1.0.0 1.0.0-1ppa1~resolute1 resolute <<<v1.0.0 | sed -n 3p)"
check "deb: release without commits" "  * Release v1.1.1." \
	"$(printf 'v1.1.1\nv1.1.0\n' | deb_entry v1.1.1 1.1.1-1ppa1~resolute1 resolute | sed -n 3p)"

check "rpm: escape doubles %" "- 100%% done, %%(id) %%{version} \$(id)" "$(rpm_escape <<<'- 100% done, %(id) %{version} $(id)')"
commit 2026-11-02T00:30:00+0100 "Fix %(touch $tmp/pwned) and \$(touch $tmp/pwned2) at 100%"
g tag v1.2.0
check "rpm: one entry per release, newest first, escaped" "* Sun Nov 01 2026 Muharem Hrnjadovic <muharem@linux.com> - 1.2.0-1
- Fix %%(touch $tmp/pwned) and \$(touch $tmp/pwned2) at 100%%

* Sun Nov 01 2026 Muharem Hrnjadovic <muharem@linux.com> - 1.1.0-1
- Add a feature (#2)
- Fix 100%% of \$(id) bugs

* Thu Oct 01 2026 Muharem Hrnjadovic <muharem@linux.com> - 1.0.0-1
- Initial commit" "$(printf 'v1.2.0\nv1.1.0\nv1.0.0\n' | rpm_changelog)"

# With rpm installed: the escaped changelog renders literally and runs nothing.
if command -v rpmspec >/dev/null; then
	{
		printf 'Name: x\nVersion: 1\nRelease: 1\nSummary: x\nLicense: MIT\n%%description\nx\n%%changelog\n'
		printf 'v1.2.0\nv1.1.0\n' | rpm_changelog
	} >"$tmp/x.spec"
	check "rpmspec: renders literally" "- Fix %(touch $tmp/pwned) and \$(touch $tmp/pwned2) at 100%" "$(rpmspec -P "$tmp/x.spec" 2>/dev/null | grep "^- Fix %")"
	check "rpmspec: nothing run" "no" "$(if [[ -e $tmp/pwned || -e $tmp/pwned2 ]]; then echo yes; else echo no; fi)"
fi

exit "$fail"
