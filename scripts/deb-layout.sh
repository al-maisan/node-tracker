#!/usr/bin/env bash
# Installs the files of the deb package under <dest>: the binary, docs, man
# page and systemd units. Shared by ppa/debian/rules and scripts/deb.sh, so
# the PPA package and the release .deb hold the same files. Run from the
# source tree; the PPA ships a copy of this script next to install-units.sh,
# so it also works for older tags.
# Usage: deb-layout.sh <dest> <binary> <version> <man-date>
set -euo pipefail

die() {
	echo "deb-layout: $*" >&2
	exit 1
}

(($# == 4)) || die "usage: $0 <dest> <binary> <version> <man-date>"
dest=$1 bin=$2 version=$3 man_date=$4
[[ $dest == /* ]] || die "dest must be an absolute path"
[[ -f $bin ]] || die "no binary '$bin'"
[[ $version =~ ^[0-9][0-9A-Za-z.+~]*$ ]] || die "invalid version '$version'"
[[ $man_date =~ ^[0-9]{4}-[0-9]{2}-[0-9]{2}$ ]] || die "invalid man date '$man_date'"
umask 022

# The vendored copy in the PPA build, the module cache otherwise.
notice=vendor/go.yaml.in/yaml/v3/NOTICE
if [[ ! -f $notice ]]; then notice=$(go list -m -f '{{.Dir}}' go.yaml.in/yaml/v3)/NOTICE; fi

doc=$dest/usr/share/doc/node-tracker
install -Dpm 0755 "$bin" "$dest/usr/bin/node-tracker"
install -Dpm 0644 -t "$doc" README.md
install -Dpm 0644 -t "$doc/assets" assets/*
install -Dpm 0644 -t "$doc/docs" docs/*.md
install -Dpm 0644 -t "$doc/examples" examples/config.yaml
install -Dpm 0644 -t "$doc/vendor/go.yaml.in/yaml/v3" "$notice"
# Tags before the man page (v1.0.0) have none.
if [[ -f node-tracker.1 ]]; then
	install -d "$dest/usr/share/man/man1"
	sed -e "s/@VERSION@/$version/g" -e "s/@DATE@/$man_date/g" node-tracker.1 >"$dest/usr/share/man/man1/node-tracker.1"
	chmod 644 "$dest/usr/share/man/man1/node-tracker.1"
	if grep -n '@[A-Z]*@' "$dest/usr/share/man/man1/node-tracker.1" >&2; then
		die "placeholder left in node-tracker.1"
	fi
fi
bash "$(dirname "${BASH_SOURCE[0]}")/install-units.sh" deploy/systemd "$dest/usr/lib/systemd/system" "$dest/usr/lib/systemd/user"
