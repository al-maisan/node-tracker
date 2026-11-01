#!/usr/bin/env bash
# Builds a stable release tag in Fedora COPR for every supported Fedora
# release and arch that lacks it. Run via 'make copr'.
# Usage: scripts/copr-build.sh <tag>
# Supported: released (final images in fedoraproject.org/releases.json) and
# maintained (state "current" in Bodhi, which counts betas as current), for
# x86_64 and aarch64. COPR_CHROOTS="fedora-44-x86_64 ..." overrides the list.
# A chroot is done if a build of this version succeeded or is still running
# there; COPR_FORCE=1 builds for all of them anyway.
# Needs go, curl, python3, rpmbuild and a configured copr-cli.
set -euo pipefail

owner=al-maisan
project=node-tracker
package=node-tracker
arches=(x86_64 aarch64)
copr_api=https://copr.fedorainfracloud.org/api_3

die() {
	echo "copr-build: $*" >&2
	exit 1
}

# Reads fedoraproject.org/releases.json on stdin; prints the Fedora versions
# with final (non-beta) images.
released_versions() {
	python3 -I -c '
import json, sys
vs = {str(r.get("version", "")) for r in json.load(sys.stdin)}
print("\n".join(sorted((v for v in vs if v.isdigit()), key=int)))'
}

# Reads Bodhi's releases JSON on stdin; prints fedora-N-ARCH for every current
# Fedora release in <released> (newline-separated) and every <arch>.
supported_chroots() {
	python3 -I -c '
import json, sys
released = set(sys.argv[1].split())
vs = {r["version"] for r in json.load(sys.stdin).get("releases", [])
      if r.get("id_prefix") == "FEDORA" and r.get("state") == "current" and r.get("version") in released}
for v in sorted(vs, key=int):
    for a in sys.argv[2:]:
        print(f"fedora-{v}-{a}")
' "$@"
}

# Reads COPR's build/list JSON on stdin; prints the chroots of the builds of
# <nvr> (e.g. 1.0.0-1) that succeeded or are still in progress.
built_chroots() {
	python3 -I -c '
import json, sys
done = set()
for b in json.load(sys.stdin).get("items", []):
    if (b.get("source_package") or {}).get("version") == sys.argv[1] and b.get("state") not in ("failed", "canceled"):
        done.update(b.get("chroots") or [])
print("\n".join(sorted(done)))
' "$1"
}

# Reads COPR's project JSON on stdin; prints the chroots enabled in it.
project_chroots() {
	python3 -I -c 'import json, sys; print("\n".join(sorted(json.load(sys.stdin).get("chroot_repos", {}))))'
}

# Prints the lines of <wanted> that are not in <have> (both newline-separated).
missing() {
	comm -23 <(sed '/^$/d' <<<"$1" | sort -u) <(sed '/^$/d' <<<"$2" | sort -u)
}

# Prints the Release number of the spec template (1%{?dist} -> 1).
spec_release() {
	awk '/^Release:/ { sub(/%.*/, "", $2); print $2; exit }' "$(dirname "$0")/../node-tracker.spec"
}

# The functions above are sourced by copr-build_test.sh; stop here in that case.
if [[ ${BASH_SOURCE[0]} != "$0" ]]; then return 0; fi

(($# == 1)) || die "usage: $0 <tag>"
tag=$1
[[ $tag =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]] || die "tag '$tag' is not vMAJOR.MINOR.PATCH"
src=$(cd "$(dirname "$0")/.." && pwd)
nvr=${tag#v}-$(spec_release)

# Fail loudly: guessing would build for the wrong releases or twice.
fetch() {
	curl -fsS "$1" || die "cannot fetch $1"
}

if [[ -n ${COPR_CHROOTS:-} ]]; then
	targets=$(tr -s ' ' '\n' <<<"$COPR_CHROOTS" | sed '/^$/d')
	if grep -vxE 'fedora-[0-9]+-(x86_64|aarch64)' <<<"$targets" >&2; then die "COPR_CHROOTS: not fedora-N-x86_64|aarch64"; fi
else
	releases=$(fetch https://fedoraproject.org/releases.json)
	bodhi=$(fetch "https://bodhi.fedoraproject.org/releases/?state=current&rows_per_page=100")
	released=$(released_versions <<<"$releases")
	targets=$(supported_chroots "$released" "${arches[@]}" <<<"$bodhi")
	[[ -n $targets ]] || die "found no released and maintained Fedora release"
fi

todo=$targets
if [[ -z ${COPR_FORCE:-} ]]; then
	builds=$(fetch "$copr_api/build/list?ownername=$owner&projectname=$project&packagename=$package&limit=100&order=id&order_type=DESC")
	built=$(built_chroots "$nvr" <<<"$builds")
	todo=$(missing "$targets" "$built")
	if [[ -z $todo ]]; then
		echo "copr-build: $package-$nvr is in COPR for $(paste -sd' ' <<<"$targets"). To rebuild: make copr COPR_FORCE=1"
		exit 0
	fi
fi
echo "copr-build: building $package-$nvr for $(paste -sd' ' <<<"$todo")"

# COPR builds only for chroots enabled in the project: enable new ones, keep the rest.
project_json=$(fetch "$copr_api/project?ownername=$owner&projectname=$project")
enabled=$(project_chroots <<<"$project_json")
new=$(missing "$todo" "$enabled")
if [[ -n $new ]]; then
	echo "copr-build: enabling in $owner/$project: $(paste -sd' ' <<<"$new")"
	mapfile -t all < <(printf '%s\n%s\n' "$enabled" "$new" | sed '/^$/d' | sort -u)
	copr-cli modify "$owner/$project" "${all[@]/#/--chroot=}"
fi

srpm=$("$src/scripts/srpm.sh" "$tag")
mapfile -t chroots <<<"$todo"
copr-cli build "$owner/$project" "${chroots[@]/#/--chroot=}" "$srpm"
