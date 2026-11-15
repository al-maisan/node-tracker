#!/usr/bin/env bash
# Fails when Go has a newer patch release of the .go-version minor than
# .go-version, so the release toolchain gets bumped. CI runs it daily.
# Tests: GO_VERSION_FILE=<file>, GO_DL_JSON=<go.dev/dl/?mode=json file>.
set -euo pipefail
cd "$(dirname "$0")/.."

die() {
	echo "go-version-check: $*" >&2
	exit 1
}

have=$(<"${GO_VERSION_FILE:-.go-version}")
[[ $have =~ ^([0-9]+\.[0-9]+)\.[0-9]+$ ]] || die ".go-version '$have' is not MAJOR.MINOR.PATCH"
minor=${BASH_REMATCH[1]}
if [[ -n ${GO_DL_JSON:-} ]]; then
	json=$(<"$GO_DL_JSON")
else
	json=$(curl -fsSL --max-time 30 'https://go.dev/dl/?mode=json') || die "cannot read the Go release list"
fi
grep -qE '"version": *"go[0-9]' <<<"$json" || die "no Go versions in the release list (format changed?)"
latest=$(grep -oE "\"version\": *\"go${minor//./\\.}\.[0-9]+\"" <<<"$json" | grep -oE '[0-9]+\.[0-9]+\.[0-9]+' | sort -V | tail -1) || true
[[ -n $latest ]] || die "go$minor is no longer supported: move .go-version to a supported Go"
[[ $(printf '%s\n' "$have" "$latest" | sort -V | tail -1) == "$have" ]] || die "go$latest is out: bump .go-version (go$have)"
echo "go-version-check: go$have is the latest go$minor"
