#!/usr/bin/env bash
# Builds the release archives and SHA256SUMS in dist/. Run via 'make dist'.
# Needs GNU tar (for reproducible archives).
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
targets=(linux/amd64 linux/arm64 linux/armv7 darwin/amd64 darwin/arm64)
mtime=$(git log -1 --format=%ct)

rm -rf dist
mkdir dist
for t in "${targets[@]}"; do
	os=${t%/*} arch=${t#*/} goarch=${t#*/} goarm=
	if [[ $arch == armv7 ]]; then goarch=arm goarm=7; fi
	name=node-tracker_${VERSION#v}_${os}_${arch}
	stage=dist/$name
	mkdir -p "$stage/examples"
	echo "dist: $name"
	GOOS=$os GOARCH=$goarch GOARM=$goarm \
		go build -trimpath -ldflags "$LDFLAGS" -o "$stage/node-tracker" ./cmd/node-tracker
	cp -R README.md LICENSE assets deploy docs "$stage/"
	rm "$stage"/deploy/*.go # embeds the units for install-service; not for users
	cp examples/config.yaml "$stage/examples/"
	tar --sort=name --owner=0 --group=0 --numeric-owner --mode=go-w,a+rX \
		--mtime="@$mtime" -C dist -cf - "$name" | gzip -9n >"$stage.tar.gz"
	rm -rf "$stage"
done
(cd dist && sha256sum -- *.tar.gz >SHA256SUMS)
