#!/usr/bin/env bash
# Pushes the container image of the stable release <tag> to ghcr.io as
# <tag>, and as latest if <tag> is the highest stable tag on GitHub. The
# image is rebuilt to dist/image from the linux archives of the GitHub
# release, checked against its SHA256SUMS signed by the release key, so this
# can be rerun at any time. 'make release' runs it; by hand:
# 'git checkout vX.Y.Z && make push-image TAG=vX.Y.Z'.
# Needs gh, gpg, skopeo, podman or docker, and a login to ghcr.io.
# Usage: scripts/push-image.sh <tag>
set -euo pipefail
cd "$(dirname "$0")/.."

# shellcheck source=release-key.sh
source scripts/release-key.sh
# shellcheck source=release-lib.sh
source scripts/release-lib.sh

die() {
	echo "push-image: $*" >&2
	exit 1
}

(($# == 1)) || die "usage: $0 <tag>"
tag=$1
[[ $tag =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]] || die "'$tag' is not a stable tag vMAJOR.MINOR.PATCH"
[[ -z $(git status --porcelain) ]] || die "working tree not clean"
# The image's revision and timestamps come from HEAD.
[[ $(git rev-parse -q --verify "refs/tags/$tag^{commit}") == "$(git rev-parse HEAD)" ]] ||
	die "HEAD is not $tag; run: git checkout $tag"
tag_signed "$tag" . || die "tag '$tag' is not signed by $release_key"
repo=$(origin_repo) || exit 1
image=ghcr.io/${repo,,}
login=$(ghcr_login) || exit 1

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
gh release download "$tag" --repo "$repo" --dir "$tmp" --pattern SHA256SUMS --pattern SHA256SUMS.asc \
	--pattern "node-tracker_${tag#v}_linux_*.tar.gz" || die "downloading the assets of release $tag failed"
gpg --status-fd 1 --verify "$tmp/SHA256SUMS.asc" "$tmp/SHA256SUMS" 2>/dev/null | signed_by_release_key ||
	die "SHA256SUMS of release $tag is not signed by $release_key"
mkdir -p dist
IMAGE_SOURCE=https://github.com/$repo scripts/image.sh "$tag" "$tmp" dist/image
digest=$(oci_digest dist/image) || exit 1

# latest never moves back, e.g. for a fix release of an older version.
tags=("$tag")
highest=$(git ls-remote --tags --refs "https://github.com/$repo.git" 'v*' | sed 's,.*refs/tags/,,' | highest_stable_tag) ||
	die "listing the tags of $repo failed"
if [[ $highest == "$tag" ]]; then
	tags+=(latest)
else
	echo "push-image: $tag is not the highest stable tag ($highest), so latest stays"
fi
for t in "${tags[@]}"; do
	skopeo copy --quiet --all --preserve-digests --digestfile "$tmp/digest" oci:dist/image "docker://$image:$t" ||
		die "pushing $image:$t as $login failed"
	[[ $(cat "$tmp/digest") == "$digest" ]] || die "$image:$t is $(cat "$tmp/digest"), but dist/image is $digest"
	echo "push-image: pushed $image:$t, digest $digest"
done
