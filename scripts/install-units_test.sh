#!/usr/bin/env bash
# Tests for install-units.sh: make test-ppa
set -uo pipefail

dir=$(cd "$(dirname "$0")/.." && pwd)
fail=0
check() { # check <name> <want> <got>
	if [[ $2 == "$3" ]]; then echo "ok   - $1"; else echo "FAIL - $1: want '$2', got '$3'"; fail=1; fi
}

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
"$dir/scripts/install-units.sh" "$dir/deploy/systemd" "$tmp/system" "$tmp/user"
check "installs every unit" "$(cd "$dir/deploy/systemd" && ls system/* user/*)" "$(cd "$tmp" && ls system/* user/*)"
check "every ExecStart runs /usr/bin/node-tracker" "" \
	"$(grep -h '^ExecStart=' "$tmp"/*/* | grep -v '^ExecStart=/usr/bin/node-tracker ')"
check "arguments kept" "ExecStart=/usr/bin/node-tracker run -config /etc/node-tracker/config.yaml -state /var/lib/node-tracker/state.json" \
	"$(grep '^ExecStart=' "$tmp/system/node-tracker.service")"
check "system hint" "# Daemon mode. Put the config in /etc/node-tracker/config.yaml (the
# package created the node-tracker user), then:
#   systemctl enable --now node-tracker.service" "$(sed -n 1,3p "$tmp/system/node-tracker.service")"
check "user hint" "# Daemon mode, as your user. Put the config in
# ~/.config/node-tracker/config.yaml, then:" "$(sed -n 1,2p "$tmp/user/node-tracker.service")"
check "mode 644" "644" "$(stat -c %a "$tmp"/*/* | sort -u)"
# Only the ExecStart lines and the two hints change.
check "rest unchanged" "" "$(for f in "$tmp"/*/*; do
	diff <(grep -v '^ExecStart=\|^#' "$dir/deploy/systemd/${f#"$tmp"/}") <(grep -v '^ExecStart=\|^#' "$f")
done)"
check "relative dir refused" no "$("$dir/scripts/install-units.sh" "$dir/deploy/systemd" rel "$tmp/u2" 2>/dev/null && echo yes || echo no)"

# A unit with a path the rewrite doesn't know fails the install.
cp -R "$dir/deploy/systemd" "$tmp/src"
sed -i 's,^ExecStart=/usr/local/bin/,ExecStart=/opt/bin/,' "$tmp/src/system/node-tracker.service"
check "unknown ExecStart fails" no "$("$dir/scripts/install-units.sh" "$tmp/src" "$tmp/s2" "$tmp/u2" 2>/dev/null && echo yes || echo no)"

exit "$fail"
