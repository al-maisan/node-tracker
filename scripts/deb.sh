#!/usr/bin/env bash
# Builds the release .deb from a release binary: the PPA package's files
# (deb-layout.sh) plus what debhelper adds there (sysusers file, copyright,
# changelog, lintian overrides, maintainer scripts). Called by dist.sh.
# The same inputs give the same .deb. Needs dpkg-deb.
# Usage: scripts/deb.sh <binary> <version> <arch> <mtime> <out.deb>
#   (<version> is MAJOR.MINOR.PATCH, <arch> amd64, arm64 or armhf,
#   <mtime> the commit time in seconds since the epoch)
set -euo pipefail

die() {
	echo "deb: $*" >&2
	exit 1
}

(($# == 5)) || die "usage: $0 <binary> <version> <arch> <mtime> <out.deb>"
# Relative to the caller's directory, before the cd.
bin=$(realpath -e -- "$1") || die "no binary '$1'"
out=$(realpath -m -- "$5")
version=$2 arch=$3 mtime=$4
cd "$(dirname "$0")/.."
[[ $version =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || die "version '$version' is not MAJOR.MINOR.PATCH"
[[ $arch =~ ^(amd64|arm64|armhf)$ ]] || die "unsupported arch '$arch'"
[[ $mtime =~ ^[0-9]+$ ]] || die "invalid mtime '$mtime'"
[[ $out == *.deb && -d $(dirname "$out") ]] || die "'$out' is not a .deb in an existing directory"
command -v dpkg-deb >/dev/null || die "dpkg-deb not found (Fedora: sudo dnf install dpkg)"

# Prints field <name> of the first paragraph in ppa/debian/control that has
# it, with its continuation lines.
field() {
	awk -v f="$1:" '$1 == f { p = 1; print; next } p && /^[ \t]/ { print; next } p { exit }' ppa/debian/control
}
maintainer=$(field Maintainer)
[[ -n $maintainer ]] || die "no Maintainer in ppa/debian/control"

umask 022
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
root=$tmp/root
doc=$root/usr/share/doc/node-tracker
scripts/deb-layout.sh "$root" "$bin" "$version" "$(date -u -d "@$mtime" +%Y-%m-%d)"
if [[ -f $root/usr/share/man/man1/node-tracker.1 ]]; then gzip -9n "$root/usr/share/man/man1/node-tracker.1"; fi
install -Dpm 0644 deploy/sysusers.d/node-tracker.conf "$root/usr/lib/sysusers.d/node-tracker.conf"
install -d "$root/usr/share/lintian/overrides"
{
	cat ppa/debian/node-tracker.lintian-overrides
	echo "# Not a Debian upload, so there is no ITP bug to close."
	echo "node-tracker: initial-upload-closes-no-bugs"
} >"$root/usr/share/lintian/overrides/node-tracker"
install -pm 0644 ppa/debian/copyright "$doc/copyright"
{
	echo "node-tracker ($version-1) stable; urgency=medium"
	echo
	echo "  * Release v$version, see"
	echo "    https://github.com/al-maisan/node-tracker/releases/tag/v$version"
	echo
	echo " -- ${maintainer#Maintainer: }  $(LC_ALL=C date -u -R -d "@$mtime")"
} | gzip -9n >"$doc/changelog.Debian.gz"
chmod 644 "$doc/changelog.Debian.gz" "$root/usr/share/lintian/overrides/node-tracker"

install -d "$root/DEBIAN"
install -pm 0755 scripts/deb/postinst scripts/deb/prerm scripts/deb/postrm "$root/DEBIAN/"
(cd "$root" && find . -path ./DEBIAN -prune -o -type f -printf '%P\0' | LC_ALL=C sort -z | xargs -0 md5sum) >"$root/DEBIAN/md5sums"
# As dpkg-gencontrol counts it: KiB per file, rounded up, plus 1 per directory.
size=$(find "$root" -path "$root/DEBIAN" -prune -o -mindepth 1 \( -type d -printf '1\n' -o -printf '%s\n' \) |
	awk '{ n += $1 == 1 ? 1 : int(($1 + 1023) / 1024) } END { print n }')
{
	echo "Package: node-tracker"
	echo "Version: $version-1"
	echo "Architecture: $arch"
	echo "$maintainer"
	echo "Installed-Size: $size"
	echo "Depends: systemd | systemd-standalone-sysusers | systemd-sysusers"
	field Section
	echo "Priority: optional"
	field Homepage
	field Description
} >"$root/DEBIAN/control"
chmod 644 "$root/DEBIAN/control" "$root/DEBIAN/md5sums"
grep -qx 'Section: [a-z]*' "$root/DEBIAN/control" || die "no Section in ppa/debian/control"
grep -q '^Description: .' "$root/DEBIAN/control" || die "no Description in ppa/debian/control"

find "$root" -exec touch -h -d "@$mtime" {} +
SOURCE_DATE_EPOCH=$mtime dpkg-deb --root-owner-group -Zxz --threads-max=1 --build "$root" "$tmp/out.deb" >/dev/null
mv "$tmp/out.deb" "$out"
echo "deb: $out"
