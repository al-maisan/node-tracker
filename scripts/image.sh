#!/usr/bin/env bash
# Builds the container image for linux/amd64, linux/arm64 and linux/arm/v6
# from the binaries in the release archives of <dist>, checked against
# <dist>/SHA256SUMS, so the image adds no build of its own. Writes it as an
# OCI layout to <out> and tags it localhost/node-tracker:<version> in the
# local image store. The same archives and commit give the same image
# digest. Run via 'make image'; 'make release' pushes <out> to ghcr.io.
# Needs podman, or docker with the containerd image store (CONTAINER_ENGINE
# picks one), GNU tar and curl.
# Usage: scripts/image.sh <version> <dist> <out>
set -euo pipefail

die() {
	echo "image: $*" >&2
	exit 1
}

(($# == 3)) || die "usage: $0 <version> <dist> <out>"
# Relative to the caller's directory, before the cd.
dist=$(realpath -e -- "$2") || die "no directory '$2'"
out=$(realpath -m -- "$3")
version=$1
cd "$(dirname "$0")/.."
# shellcheck source=release-lib.sh
source scripts/release-lib.sh
# An image tag: at most 128 of A-Z a-z 0-9 _ . -
[[ $version =~ ^[0-9A-Za-z][0-9A-Za-z._-]{0,127}$ ]] || die "invalid version '$version'"
[[ -f $dist/SHA256SUMS ]] || die "no $dist/SHA256SUMS; run make dist first"
# Never remove anything but an earlier image.
[[ ! -e $out || -f $out/oci-layout ]] || die "'$out' exists and is not an OCI layout"
source=${IMAGE_SOURCE:-https://github.com/al-maisan/node-tracker}
[[ $source =~ ^https://github\.com/[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$ ]] || die "IMAGE_SOURCE '$source' is not a GitHub repo URL"

engine=${CONTAINER_ENGINE:-}
if [[ -z $engine ]]; then
	if command -v podman >/dev/null; then engine=podman; elif command -v docker >/dev/null; then engine=docker; fi
fi
[[ $engine =~ ^(podman|docker)$ ]] || die "need podman or docker (CONTAINER_ENGINE '$engine')"
command -v "$engine" >/dev/null || die "$engine not found"

# Mozilla's CA certificates as curl extracts them
# (https://curl.se/docs/caextract.html), pinned by date and hash. To update,
# set both to the newest cacert-<date>.pem there and its sha256sum.
ca_url=https://curl.se/ca/cacert-2026-10-20.pem
ca_sha256=796f1389bfea13c0ed78f663b765baaff54a508e5483786bd6ece73c5e3e3cbe
cache=${XDG_CACHE_HOME:-$HOME/.cache}/node-tracker
ca=$cache/${ca_url##*/}
sha() { sha256sum <"$1" | cut -d' ' -f1; }
if [[ ! -f $ca || $(sha "$ca") != "$ca_sha256" ]]; then
	mkdir -p "$cache"
	curl -fsSL --proto '=https' --tlsv1.2 -o "$ca.tmp" "$ca_url" || die "downloading $ca_url failed"
	[[ $(sha "$ca.tmp") == "$ca_sha256" ]] || { rm -f "$ca.tmp"; die "$ca_url does not have sha256 $ca_sha256"; }
	mv "$ca.tmp" "$ca"
fi

umask 022
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
ctx=$tmp/ctx
mkdir -p "$ctx/state-root/state"
chmod 700 "$ctx/state-root/state"
install -D -m 644 "$ca" "$ctx/rootfs/etc/ssl/certs/ca-certificates.crt"
# Archive archs and their platforms. The archive arch is also the context
# directory of the platform: its TARGETARCH and TARGETVARIANT, joined.
archs=(amd64 arm64 armv6)
platforms=(linux/amd64 linux/arm64 linux/arm/v6)
for a in "${archs[@]}"; do
	name=node-tracker_${version#v}_linux_$a
	want=$(awk -v n="$name.tar.gz" '$2 == n { print $1 }' "$dist/SHA256SUMS")
	[[ -n $want ]] || die "$name.tar.gz is not in $dist/SHA256SUMS"
	[[ -f $dist/$name.tar.gz && $(sha "$dist/$name.tar.gz") == "$want" ]] ||
		die "$dist/$name.tar.gz is missing or does not match SHA256SUMS"
	tar -xzf "$dist/$name.tar.gz" -C "$tmp" "$name/node-tracker"
	mkdir "$ctx/$a"
	install -m 755 "$tmp/$name/node-tracker" "$ctx/$a/node-tracker"
done

# The commit time, as for the archives: build time, label and timestamps.
mtime=$(git log -1 --format=%ct)
args=(
	--build-arg "VERSION=$version"
	--build-arg "REVISION=$(git rev-parse HEAD)"
	--build-arg "CREATED=$(date -u -d "@$mtime" +%Y-%m-%dT%H:%M:%SZ)"
	--build-arg "SOURCE=$source"
	-f Containerfile
)
index=(
	"org.opencontainers.image.source=$source"
	"org.opencontainers.image.description=Alerts via Telegram when a Bitcoin node falls behind the chain tip"
	"org.opencontainers.image.licenses=GPL-3.0-only"
)
ref=localhost/node-tracker:$version
rm -rf "$out"
echo "image: building $ref with $engine"
if [[ $engine == podman ]]; then
	if podman manifest exists "$ref"; then podman manifest rm "$ref" >/dev/null; fi
	# One platform at a time: a parallel build adds them in random order,
	# which changes the digest.
	for p in "${platforms[@]}"; do
		podman build --quiet --no-cache --layers=false --platform "$p" --manifest "$ref" --timestamp "$mtime" \
			--identity-label=false "${args[@]}" "$ctx" >/dev/null
	done
	for a in "${index[@]}"; do podman manifest annotate --index --annotation "$a" "$ref" >/dev/null; done
	podman manifest push --quiet --all --format oci "$ref" "oci:$out"
else
	annotations=()
	for a in "${index[@]}"; do annotations+=(--annotation "index:$a"); done
	SOURCE_DATE_EPOCH=$mtime docker buildx build --quiet --no-cache --provenance=false --sbom=false \
		--platform "$(IFS=,; echo "${platforms[*]}")" "${args[@]}" "${annotations[@]}" \
		--output "type=oci,dest=$out,tar=false,name=$ref,rewrite-timestamp=true" "$ctx" >/dev/null
	# Loaded from the layout: a second --output races on the shared layers.
	tar -C "$out" -cf - . | docker load >/dev/null
fi
digest=$(oci_digest "$out") || exit 1
echo "image: $out ($ref), digest $digest"
