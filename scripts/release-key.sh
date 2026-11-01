# Sourced by the release and packaging scripts: the release key and checks
# that it made a signature.
# shellcheck shell=bash
release_key=${RELEASE_KEY:-753B6ECF2B458FF3D19D568C1E0A288397AE739E}

# Reads gpg status lines on stdin; true if the release key (primary key or
# subkey) made a valid signature.
signed_by_release_key() {
	awk -v k="$release_key" '$1 == "[GNUPG:]" && $2 == "VALIDSIG" && ($3 == k || $NF == k) { ok = 1 } END { exit !ok }'
}

# True if <tag> in the repository <dir> is signed by the release key.
tag_signed() {
	git -C "$2" verify-tag --raw "$1" 2>&1 | signed_by_release_key
}
