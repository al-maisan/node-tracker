#!/usr/bin/env bash
# Publishes a GitHub release for the signed tag at HEAD. Run via 'make release'.
# Signs dist/SHA256SUMS with your default gpg key.
set -euo pipefail
cd "$(dirname "$0")/.."

die() {
	echo "release: $*" >&2
	exit 1
}

[[ -z $(git status --porcelain) ]] || die "working tree not clean"
mapfile -t tags < <(git tag --points-at HEAD 'v*')
((${#tags[@]} == 1)) || die "need exactly one v* tag at HEAD, found ${#tags[@]}: ${tags[*]}"
tag=${tags[0]}
[[ $tag =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.]+)?$ ]] || die "tag '$tag' is not vMAJOR.MINOR.PATCH[-PRE]"
git tag --verify "$tag" >/dev/null 2>&1 || die "tag '$tag' is not signed or its signature does not verify"
remote=$(git ls-remote origin "refs/tags/$tag^{}" | cut -f1)
[[ $remote == "$(git rev-parse HEAD)" ]] || die "tag '$tag' is not pushed to origin or points to another commit there"

# Commit time as build time: anyone can rebuild the same archives.
make dist VERSION="$tag" BUILD_TIME="$(TZ=UTC git log -1 --format=%cd --date=format-local:%Y-%m-%dT%H:%M:%SZ)"
gpg --armor --detach-sign --output dist/SHA256SUMS.asc dist/SHA256SUMS
gpg --verify dist/SHA256SUMS.asc dist/SHA256SUMS

pre=()
if [[ $tag == *-* ]]; then pre=(--prerelease); fi
gh release create "$tag" --verify-tag --title "$tag" --generate-notes "${pre[@]}" \
	dist/*.tar.gz dist/SHA256SUMS dist/SHA256SUMS.asc
