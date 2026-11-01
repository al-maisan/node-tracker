#!/usr/bin/env bash
# Installs the systemd units for the deb and rpm packages: <src>/system to
# <unitdir>, <src>/user to <userunitdir>, with ExecStart and the install
# hints fitted to /usr/bin/node-tracker and the packaged user. The packages
# ship a copy of this script, so it also works for older tags.
# Usage: scripts/install-units.sh <src> <unitdir> <userunitdir>
#   (<src> is deploy/systemd of the source tree)
set -euo pipefail

die() {
	echo "install-units: $*" >&2
	exit 1
}

(($# == 3)) || die "usage: $0 <src> <unitdir> <userunitdir>"
src=$1
shift
[[ $1 == /* && $2 == /* ]] || die "unit dirs must be absolute paths"

# Prints the packaged form of the unit on stdin.
rewrite() {
	sed -e 's@^ExecStart=\(/usr/local/bin\|%h/\.local/bin\)/node-tracker\( \|$\)@ExecStart=/usr/bin/node-tracker\2@' \
		-e 's@^# Daemon mode\. Install the binary to /usr/local/bin, the config to$@# Daemon mode. Put the config in /etc/node-tracker/config.yaml (the@' \
		-e 's@^# /etc/node-tracker/config\.yaml, then:$@# package created the node-tracker user), then:@' \
		-e '/^#   useradd /d' \
		-e 's@^# Daemon mode, as your user\. Install the binary to ~/\.local/bin and the config$@# Daemon mode, as your user. Put the config in@' \
		-e 's@^# to ~/\.config/node-tracker/config\.yaml, then:$@# ~/.config/node-tracker/config.yaml, then:@'
}

for kind in system user; do
	dst=$1
	if [[ $kind == user ]]; then dst=$2; fi
	install -d "$dst"
	n=0
	for f in "$src/$kind"/*.service "$src/$kind"/*.timer; do
		out=$dst/${f##*/}
		rewrite <"$f" >"$out"
		chmod 644 "$out"
		# A unit edited upstream must not slip through with a stale path or hint.
		if grep -En '/usr/local/bin|\.local/bin|useradd|Install the binary' "$out" >&2; then
			die "$out: not fully rewritten"
		fi
		if [[ $out == *.service ]] && ! grep -q '^ExecStart=/usr/bin/node-tracker ' "$out"; then
			die "$out: no ExecStart=/usr/bin/node-tracker"
		fi
		n=$((n + 1))
	done
	((n > 0)) || die "no units in $src/$kind"
done
