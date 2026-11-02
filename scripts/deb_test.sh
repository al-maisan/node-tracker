#!/usr/bin/env bash
# Tests for deb.sh and deb-layout.sh: make test-ppa
# Needs dpkg-deb; no network once the Go module cache has go.yaml.in/yaml/v3.
set -uo pipefail

dir=$(cd "$(dirname "$0")/.." && pwd)
fail=0
check() { # check <name> <want> <got>
	if [[ $2 == "$3" ]]; then echo "ok   - $1"; else echo "FAIL - $1: want '$2', got '$3'"; fail=1; fi
}
builds() { if "$dir/scripts/deb.sh" "$@" >/dev/null 2>&1; then echo yes; else echo no; fi; }

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
printf '#!/bin/sh\n' >"$tmp/bin"
mtime=1767225600 # 2026-01-01
for d in a b; do
	"$dir/scripts/deb.sh" "$tmp/bin" 1.2.3 arm64 "$mtime" "$tmp/$d.deb" >/dev/null || { echo "FAIL - build"; exit 1; }
done
check "same inputs, same .deb" "$(sha256sum <"$tmp/a.deb")" "$(sha256sum <"$tmp/b.deb")"

f() { dpkg-deb -f "$tmp/a.deb" "$1"; }
check "version" "1.2.3-1" "$(f Version)"
check "arch" "arm64" "$(f Architecture)"
check "depends" "systemd | systemd-standalone-sysusers | systemd-sysusers" "$(f Depends)"
check "fields from ppa/debian/control" "$(grep -E '^(Maintainer|Section|Homepage):' "$dir/ppa/debian/control" | sort)" \
	"$(dpkg-deb -f "$tmp/a.deb" Maintainer Section Homepage | sort)"
check "description from ppa/debian/control" "$(sed -n '/^Description:/,$p' "$dir/ppa/debian/control" | sed 's/^Description: //')" "$(f Description)"

list=$(dpkg-deb -c "$tmp/a.deb" | awk '{ print $2, $6 }')
check "owned by root" "root/root" "$(awk '{ print $1 }' <<<"$list" | sort -u)"
check "mtime" "$(date -u -d "@$mtime" '+%Y-%m-%d %H:%M')" "$(TZ=UTC dpkg-deb -c "$tmp/a.deb" | awk '{ print $4, $5 }' | sort -u)"
paths=$(awk '{ print $2 }' <<<"$list")
for p in ./usr/bin/node-tracker ./usr/lib/systemd/system/node-tracker.service ./usr/lib/systemd/user/node-tracker.service \
	./usr/lib/sysusers.d/node-tracker.conf ./usr/share/man/man1/node-tracker.1.gz ./usr/share/doc/node-tracker/copyright \
	./usr/share/doc/node-tracker/changelog.Debian.gz ./usr/share/doc/node-tracker/examples/config.yaml \
	./usr/share/doc/node-tracker/vendor/go.yaml.in/yaml/v3/NOTICE ./usr/share/lintian/overrides/node-tracker; do
	check "has $p" yes "$(grep -qxF -- "$p" <<<"$paths" && echo yes || echo no)"
done
# install -t fails on a directory: docs/*.md must match only files (docs/demo/
# is a directory), and each must be packaged.
check "every doc" "$(cd "$dir" && ls docs/*.md)" "$(sed -n 's,^\./usr/share/doc/node-tracker/\(docs/.*\.md\)$,\1,p' <<<"$paths")"
check "every unit" "$(cd "$dir/deploy/systemd" && ls system/* user/*)" \
	"$(sed -n 's,^\./usr/lib/systemd/\(system\|user\)/\(.\),\1/\2,p' <<<"$paths" | LC_ALL=C sort)"

dpkg-deb -R "$tmp/a.deb" "$tmp/x"
check "md5sums match" "" "$(cd "$tmp/x" && md5sum --quiet -c DEBIAN/md5sums 2>&1)"
check "md5sums list every file" "$(cd "$tmp/x" && find . -path ./DEBIAN -prune -o -type f -printf '%P\n' | LC_ALL=C sort)" \
	"$(awk '{ print $2 }' "$tmp/x/DEBIAN/md5sums")"
check "man page filled in" "1" "$(zcat "$tmp/x/usr/share/man/man1/node-tracker.1.gz" | grep -c '^\.TH NODE\\-TRACKER 1 "2026-01-01" "node-tracker 1.2.3"')"
check "changelog version" "node-tracker (1.2.3-1) stable; urgency=medium" "$(zcat "$tmp/x/usr/share/doc/node-tracker/changelog.Debian.gz" | head -1)"
check "maintainer scripts parse" "" "$(for s in postinst prerm postrm; do sh -n "$tmp/x/DEBIAN/$s" 2>&1; done)"
check "sysusers file of postinst" yes "$(grep -q 'systemd-sysusers .* node-tracker.conf$' "$tmp/x/DEBIAN/postinst" && echo yes || echo no)"

# The maintainer scripts are copied from the PPA package: a new or renamed
# unit needs a fresh copy.
# shellcheck disable=SC2012 # unit names are plain
units=$(cd "$dir/deploy/systemd/system" && ls | paste -sd' ')
installable=$(cd "$dir/deploy/systemd/system" && grep -l '^\[Install\]' -- * | paste -sd' ')
# shellcheck disable=SC2012 # unit names are plain
check "user units match system units" "$units" "$(cd "$dir/deploy/systemd/user" && ls | paste -sd' ')"
# An upgrade restarts the running units, and does not stop them before.
check "no preinst" no "$([[ -e $tmp/x/DEBIAN/preinst ]] && echo yes || echo no)"
check "postinst restarts every unit" "$units" "$(sed -n "s/.*try-restart '\(.*\)' >.*/\1/p" "$tmp/x/DEBIAN/postinst" | sed "s/' '/ /g")"
check "prerm stops every unit" "$units" "$(sed -n "s/.*stop '\(.*\)' >.*/\1/p" "$tmp/x/DEBIAN/prerm" | sed "s/' '/ /g")"
check "postrm purges the enableable units" "$installable
$installable" "$(sed -n "s/.*purge '\(.*\)' >.*/\1/p" "$tmp/x/DEBIAN/postrm" | sed "s/' '/ /g")"
check "postinst handles the enableable units" "$(for u in $installable; do echo "$u"; done | sort)" \
	"$(sed -n "s/.*update-state '\([^']*\)' >.*/\1/p" "$tmp/x/DEBIAN/postinst" | sort -u)"
check "postinst: system and user" 2 "$(grep -c "update-state 'node-tracker.service'" "$tmp/x/DEBIAN/postinst")"

check "rejects prerelease version" no "$(builds "$tmp/bin" 1.2.3-rc1 arm64 "$mtime" "$tmp/c.deb")"
check "rejects unknown arch" no "$(builds "$tmp/bin" 1.2.3 i386 "$mtime" "$tmp/c.deb")"
check "rejects missing binary" no "$(builds "$tmp/nope" 1.2.3 arm64 "$mtime" "$tmp/c.deb")"
check "rejects bad mtime" no "$(builds "$tmp/bin" 1.2.3 arm64 1e9 "$tmp/c.deb")"
check "rejects non-.deb output" no "$(builds "$tmp/bin" 1.2.3 arm64 "$mtime" "$tmp/c.tar")"
check "layout rejects relative dest" no \
	"$(cd "$dir" && scripts/deb-layout.sh rel "$tmp/bin" 1.2.3 2026-01-01 >/dev/null 2>&1 && echo yes || echo no)"

exit "$fail"
