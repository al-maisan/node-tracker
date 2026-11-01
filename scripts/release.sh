#!/usr/bin/env bash
# Publishes a GitHub release for the signed tag at HEAD. Run via 'make release'.
# Signs dist/SHA256SUMS with the release key.
set -euo pipefail
cd "$(dirname "$0")/.."

# shellcheck source=release-key.sh
source scripts/release-key.sh
# shellcheck source=release-lib.sh
source scripts/release-lib.sh
# Official toolchain, so anyone can rebuild the same archives. Bump with new
# Go patch releases.
export GOTOOLCHAIN=${RELEASE_GO:-go1.26.10}

die() {
	echo "release: $*" >&2
	exit 1
}

[[ -z $(git status --porcelain) ]] || die "working tree not clean"

repo=$(origin_repo) || exit 1
remote=https://github.com/$repo.git

mapfile -t tags < <(git tag --points-at HEAD 'v*')
((${#tags[@]} == 1)) || die "need exactly one v* tag at HEAD, found ${#tags[@]}: ${tags[*]}"
tag=${tags[0]}
[[ $tag =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.]+)?$ ]] || die "tag '$tag' is not vMAJOR.MINOR.PATCH[-PRE]"
tag_signed "$tag" . || die "tag '$tag' is not signed by $release_key"

# The remote tag must be this very tag object, not just point to HEAD.
refs=$(git ls-remote "$remote" "refs/tags/$tag" "refs/tags/$tag^{}")
[[ $(awk -v r="refs/tags/$tag" '$2 == r { print $1 }' <<<"$refs") == "$(git rev-parse "refs/tags/$tag")" ]] ||
	die "tag '$tag' on $repo is missing or is another tag object"
[[ $(awk -v r="refs/tags/$tag^{}" '$2 == r { print $1 }' <<<"$refs") == "$(git rev-parse HEAD)" ]] ||
	die "tag '$tag' on $repo points to another commit"

main=$(git ls-remote "$remote" refs/heads/main | cut -f1)
git cat-file -e "$main^{commit}" 2>/dev/null || die "main of $repo ($main) is not local; fetch it first"
git merge-base --is-ancestor HEAD "$main" || die "HEAD is not on main of $repo"

make lint test vuln
# Commit time as build time: anyone can rebuild the same archives.
make dist VERSION="$tag" BUILD_TIME="$(TZ=UTC git log -1 --format=%cd --date=format-local:%Y-%m-%dT%H:%M:%SZ)"
gpg --local-user "$release_key" --armor --detach-sign --output dist/SHA256SUMS.asc dist/SHA256SUMS
gpg --status-fd 1 --verify dist/SHA256SUMS.asc dist/SHA256SUMS 2>/dev/null | signed_by_release_key ||
	die "SHA256SUMS.asc does not verify with $release_key"
# Homebrew gets stable releases only. Written before publishing, so a
# failure stops the release.
if [[ $tag != *-* ]]; then scripts/brew-formula.sh "$tag" "$repo"; fi

# The container image gets stable releases only. Built and the login
# checked before publishing, so a failure stops the release; pushed after
# (push-image.sh).
if [[ $tag != *-* ]]; then
	login=$(ghcr_login) || exit 1
	echo "release: pushing the image to ghcr.io as $login after the GitHub release"
	IMAGE_SOURCE=https://github.com/$repo scripts/image.sh "$tag" dist dist/image
fi

pre=()
if [[ $tag == *-* ]]; then pre=(--prerelease); fi
shopt -s nullglob
debs=(dist/*.deb) # stable releases only
shopt -u nullglob
gh release create "$tag" --repo "$repo" --verify-tag --title "$tag" --generate-notes "${pre[@]}" \
	--notes "Built with $(go env GOVERSION). Verify: see README, Quick start." \
	dist/*.tar.gz "${debs[@]}" dist/SHA256SUMS dist/SHA256SUMS.asc

if [[ $tag != *-* ]]; then
	tap=${repo%%/*}/homebrew-tap
	echo "release: publish the formula to $tap:"
	printf 'd=$(mktemp -d) && git clone https://github.com/%s.git "$d" && mkdir -p "$d/Formula" && cp %q "$d/Formula/" && git -C "$d" add Formula/node-tracker.rb && git -C "$d" commit -m "node-tracker %s" && git -C "$d" push\n' \
		"$tap" "$PWD/dist/node-tracker.rb" "$tag"
	# Last: the release is out, so a failed push only needs a rerun of this.
	scripts/push-image.sh "$tag" ||
		die "the image push failed, the rest of the release is done; fix the cause, then run: make push-image TAG=$tag"
fi
