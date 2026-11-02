#!/usr/bin/env bash
# Tests for doc-version.sh: make test-ppa
# shellcheck disable=SC2016 # the backticks are Markdown, not commands
set -uo pipefail

dir=$(cd "$(dirname "$0")/.." && pwd)
fail=0
check() { # check <name> <want> <got>
	if [[ $2 == "$3" ]]; then echo "ok   - $1"; else echo "FAIL - $1: want '$2', got '$3'"; fail=1; fi
}
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
mkdir "$tmp/docs"
dv() { DOC_ROOT=$tmp "$dir/scripts/doc-version.sh" "$@" 2>/dev/null; }

cat >"$tmp/README.md" <<'EOF'
`sudo apt install ./node-tracker_1.2.0-1_arm64.deb`
EOF
cat >"$tmp/docs/install.md" <<'EOF'
amd64, arm64, armhf; from v1.1.0 on. (example: v1.2.0 on a Pi):
sudo apt install ./node-tracker_1.2.0-1_arm64.deb
tar -xzf node-tracker_1.2.0_linux_amd64.tar.gz && sudo install -m 755 node-tracker_1.2.0_linux_amd64/node-tracker /usr/local/bin/
`node-tracker_1.2.0-1_arm64.deb: OK`; image ghcr.io/al-maisan/node-tracker:v1.2.3
EOF

dv check 1.2.0
check "current version passes" 0 $?
dv check 1.3.2
check "stale version fails" 1 $?
dv set 1.3.2
check "set" 0 $?
dv check 1.3.2
check "set version passes" 0 $?
check "README" '`sudo apt install ./node-tracker_1.3.2-1_arm64.deb`' "$(cat "$tmp/README.md")"
want='amd64, arm64, armhf; from v1.1.0 on. (example: v1.3.2 on a Pi):
sudo apt install ./node-tracker_1.3.2-1_arm64.deb
tar -xzf node-tracker_1.3.2_linux_amd64.tar.gz && sudo install -m 755 node-tracker_1.3.2_linux_amd64/node-tracker /usr/local/bin/
`node-tracker_1.3.2-1_arm64.deb: OK`; image ghcr.io/al-maisan/node-tracker:v1.2.3'
check "install.md: examples set, history and placeholder kept" "$want" "$(cat "$tmp/docs/install.md")"
printf 'example: v1.3.2\nexample: v1.3.1.\n' >"$tmp/README.md"
dv set 1.3.3
check "set at a line end and before a dot" "$(printf 'example: v1.3.3\nexample: v1.3.3.')" "$(cat "$tmp/README.md")"
dv check 1.3.3
check "check at a line end" 0 $?
: >"$tmp/README.md"
: >"$tmp/docs/install.md"
dv check 1.3.3
check "no example at all fails" 1 $?
echo 'example: v1.3.3' >"$tmp/README.md"
rm "$tmp/docs/install.md"
dv check 1.3.3
check "a missing file fails" 1 $?
dv set v1.3.2
check "a v prefix is refused" 2 $?
dv bump 1.3.2
check "an unknown command is refused" 2 $?

exit "$fail"
