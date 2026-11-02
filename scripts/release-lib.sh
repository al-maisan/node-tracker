# Sourced by the release scripts: the GitHub repo of origin and helpers for
# the container image. On failure they say why on stderr and return 1.
# shellcheck shell=bash

# Prints owner/name of the GitHub repo of origin.
origin_repo() {
	local url repo
	url=$(git remote get-url origin) || return 1
	repo=${url%.git}
	repo=${repo#*github.com[:/]}
	if [[ ! $repo =~ ^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$ ]]; then
		echo "origin '$url' is not a GitHub repo" >&2
		return 1
	fi
	echo "$repo"
}

# Reads tag names on stdin; prints the highest stable one (vMAJOR.MINOR.PATCH),
# or nothing.
highest_stable_tag() {
	{ grep -E '^v[0-9]+\.[0-9]+\.[0-9]+$' || true; } | sort -V | tail -n 1
}

# Prints the user logged in to ghcr.io.
ghcr_login() {
	if ! command -v skopeo >/dev/null; then
		echo "skopeo not found (Fedora: sudo dnf install skopeo)" >&2
		return 1
	fi
	if ! skopeo login --get-login ghcr.io 2>/dev/null; then
		echo "not logged in to ghcr.io; run 'podman login ghcr.io' with a GitHub token (classic) that has write:packages" >&2
		return 1
	fi
}

# Prints the digest of the one image in the OCI layout <dir>.
oci_digest() {
	local -a digests
	mapfile -t digests < <(grep -o '"digest": *"sha256:[0-9a-f]\{64\}"' "$1/index.json" 2>/dev/null | grep -o 'sha256:[0-9a-f]*')
	if ((${#digests[@]} != 1)); then
		echo "$1/index.json does not list exactly one image" >&2
		return 1
	fi
	echo "${digests[0]}"
}
