#!/usr/bin/env bash
# Builds the release archives, the .deb packages (stable versions only) and
# SHA256SUMS in dist/. Run via 'make dist'.
# Needs GNU tar (for reproducible archives) and dpkg-deb.
set -euo pipefail
cd "$(dirname "$0")/.."

: "${VERSION:?}" "${LDFLAGS:?}"
if [[ ! $VERSION =~ ^[0-9A-Za-z][0-9A-Za-z._+-]*$ ]]; then
	echo "dist: invalid VERSION '$VERSION'" >&2
	exit 1
fi

# Same output whatever the caller's environment says.
export CGO_ENABLED=0 GOAMD64=v1 GOARM64=v8.0 GOFLAGS= GOEXPERIMENT=

# No Windows: the state lock uses flock.
# armv6 runs on every Raspberry Pi, Zero and 1 included.
targets=(linux/amd64 linux/arm64 linux/armv6 darwin/amd64 darwin/arm64)
mtime=$(git log -1 --format=%ct)
man_date=$(date -u -d "@$mtime" +%Y-%m-%d)
# Packages get stable releases only, as the PPA and Homebrew do.
debs=
if [[ $VERSION =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]]; then debs=1; else echo "dist: $VERSION is not a stable release: no .deb"; fi

rm -rf dist
mkdir dist
for t in "${targets[@]}"; do
	os=${t%/*} arch=${t#*/} goarch=${t#*/} goarm=
	if [[ $arch == armv6 ]]; then goarch=arm goarm=6; fi
	name=node-tracker_${VERSION#v}_${os}_${arch}
	stage=dist/$name
	mkdir -p "$stage/examples"
	echo "dist: $name"
	GOOS=$os GOARCH=$goarch GOARM=$goarm \
		go build -trimpath -ldflags "$LDFLAGS" -o "$stage/node-tracker" ./cmd/node-tracker
	cp -R README.md LICENSE assets deploy docs "$stage/"
	rm "$stage"/deploy/*.go # embeds the units for install-service; not for users
	cp examples/config.yaml "$stage/examples/"
	sed -e "s/@VERSION@/${VERSION#v}/g" -e "s/@DATE@/$man_date/g" node-tracker.1 >"$stage/node-tracker.1"
	if grep -n '@[A-Z]*@' "$stage/node-tracker.1" >&2; then
		echo "dist: placeholder left in node-tracker.1" >&2
		exit 1
	fi
	tar --sort=name --owner=0 --group=0 --numeric-owner --mode=go-w,a+rX \
		--mtime="@$mtime" -C dist -cf - "$name" | gzip -9n >"$stage.tar.gz"
	if [[ -n $debs && $os == linux ]]; then
		deb_arch=$arch
		if [[ $arch == armv6 ]]; then deb_arch=armhf; fi
		scripts/deb.sh "$stage/node-tracker" "${VERSION#v}" "$deb_arch" "$mtime" "dist/node-tracker_${VERSION#v}-1_$deb_arch.deb"
	fi
	rm -rf "$stage"
done
(cd dist && shopt -s nullglob && sha256sum -- *.tar.gz *.deb >SHA256SUMS)
