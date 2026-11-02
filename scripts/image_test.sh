#!/usr/bin/env bash
# Tests for image.sh and the image: make test-image
# Builds the image from test binaries with podman or docker
# (CONTAINER_ENGINE picks one) and runs it without network; skipped if
# neither is installed. Needs network for the CA bundle unless it is cached.
set -uo pipefail

dir=$(cd "$(dirname "$0")/.." && pwd)
engine=${CONTAINER_ENGINE:-}
if [[ -z $engine ]]; then
	if command -v podman >/dev/null; then engine=podman; elif command -v docker >/dev/null; then engine=docker; fi
fi
if [[ -z $engine ]]; then
	echo "skip - image tests: neither podman nor docker is installed"
	exit 0
fi
export CONTAINER_ENGINE=$engine
fail=0
check() { # check <name> <want> <got>
	if [[ $2 == "$3" ]]; then echo "ok   - $1"; else echo "FAIL - $1: want '$2', got '$3'"; fail=1; fi
}
builds() { if "$dir/scripts/image.sh" "$@" >/dev/null 2>&1; then echo yes; else echo no; fi; }
# shellcheck source=release-lib.sh
source "$dir/scripts/release-lib.sh"
digest() { oci_digest "$1"; }

version=v0.0.0-imagetest
ref=localhost/node-tracker:$version
cname=node-tracker-imagetest-$$
tmp=$(mktemp -d)
# shellcheck disable=SC2329 # run by the EXIT trap
cleanup() {
	"$engine" rm -f -v "$cname" >/dev/null 2>&1
	if [[ $engine == podman ]]; then podman manifest rm "$ref" >/dev/null 2>&1; else docker rmi "$ref" >/dev/null 2>&1; fi
	# The images of the platforms, now untagged.
	"$engine" image prune -a -f --filter "label=org.opencontainers.image.version=$version" >/dev/null 2>&1
	rm -rf "$tmp"
}
trap cleanup EXIT

# Release archives as dist.sh names them, with only the binary.
dist=$tmp/dist
mkdir "$dist"
for a in amd64 arm64 armv6; do
	name=node-tracker_${version#v}_linux_$a goarch=$a goarm=
	if [[ $a == armv6 ]]; then goarch=arm goarm=6; fi
	(cd "$dir" && CGO_ENABLED=0 GOOS=linux GOARCH=$goarch GOARM=$goarm go build -trimpath \
		-ldflags "-s -w -X main.version=$version" -o "$dist/$name/node-tracker" ./cmd/node-tracker) || { echo "FAIL - go build $a"; exit 1; }
	tar -C "$dist" -czf "$dist/$name.tar.gz" "$name"
	rm -rf "${dist:?}/$name"
done
(cd "$dist" && sha256sum -- *.tar.gz >SHA256SUMS)

for d in a b; do
	"$dir/scripts/image.sh" "$version" "$dist" "$tmp/$d" >/dev/null || { echo "FAIL - build"; exit 1; }
done
check "same inputs, same digest" "$(digest "$tmp/a")" "$(digest "$tmp/b")"
check "rebuild replaces the layout" yes "$(builds "$version" "$dist" "$tmp/a")"
mkdir "$tmp/other"
check "rejects an output that is not an OCI layout" no "$(builds "$version" "$dist" "$tmp/other")"
check "rejects bad version" no "$(builds 'v1;rm' "$dist" "$tmp/c")"
check "rejects missing archives" no "$(builds v9.9.9 "$dist" "$tmp/c")"
cp -r "$dist" "$tmp/bad"
printf x >>"$tmp/bad/node-tracker_${version#v}_linux_arm64.tar.gz"
check "rejects an archive that does not match SHA256SUMS" no "$(builds "$version" "$tmp/bad" "$tmp/c")"
"$dir/scripts/image.sh" "$version" "$dist" "$tmp/a" >/dev/null # the local image of these archives

index=$(cat "$tmp/a/blobs/sha256/$(digest "$tmp/a" | cut -d: -f2)")
check "platforms" "amd64 arm64 arm" "$(grep -o '"architecture": *"[a-z0-9]*"' <<<"$index" | cut -d'"' -f4 | paste -sd' ')"
check "arm is v6" 1 "$(grep -c '"variant": *"v6"' <<<"$index")"
# Each platform has its binary: the last layer of each image.
sha() { grep -o 'sha256:[0-9a-f]\{64\}' "$@" | cut -d: -f2; }
for m in $(sha <<<"$index"); do
	layer=$(sha "$tmp/a/blobs/sha256/$m" | tail -1)
	tar -xzOf "$tmp/a/blobs/sha256/$layer" node-tracker >"$tmp/bin"
	go version -m "$tmp/bin" | awk '$1 == "build" && $2 ~ /^GOARCH=|^GOARM=/ { print $2 }' | paste -sd' '
done >"$tmp/goarch"
check "binaries" "GOARCH=amd64
GOARCH=arm64
GOARCH=arm GOARM=6" "$(cat "$tmp/goarch")"
inspect() { "$engine" image inspect --format "$1" "$ref"; }
check "non-root user" "65532:65532" "$(inspect '{{.Config.User}}')"
check "version label" "$version" "$(inspect '{{index .Config.Labels "org.opencontainers.image.version"}}')"
check "source label" "https://github.com/al-maisan/node-tracker" "$(inspect '{{index .Config.Labels "org.opencontainers.image.source"}}')"
check "revision label" "$(git -C "$dir" rev-parse HEAD)" "$(inspect '{{index .Config.Labels "org.opencontainers.image.revision"}}')"
check "created label is the commit time" "$(date -u -d "@$(git -C "$dir" log -1 --format=%ct)" +%Y-%m-%dT%H:%M:%SZ)" \
	"$(inspect '{{index .Config.Labels "org.opencontainers.image.created"}}')"

# The files of the image for this host.
c=$("$engine" create "$ref")
files=$("$engine" export "$c" | tar -tvf - --numeric-owner | awk '{ print $1, $2, $6 }' | LC_ALL=C sort -k3)
ca=$("$engine" export "$c" | tar -xOf - etc/ssl/certs/ca-certificates.crt | sha256sum | cut -d' ' -f1)
"$engine" rm -v "$c" >/dev/null
check "files" "drwxr-xr-x 0/0 etc/
drwxr-xr-x 0/0 etc/ssl/
drwxr-xr-x 0/0 etc/ssl/certs/
-rw-r--r-- 0/0 etc/ssl/certs/ca-certificates.crt
-rwxr-xr-x 0/0 node-tracker
drwx------ 65532/65532 state/" "$(grep -E ' (etc/(ssl/(certs/(ca-certificates.crt)?)?)?|node-tracker|state/)$' <<<"$files")"
check "pinned CA bundle" "$(sed -n 's/^ca_sha256=//p' "$dir/scripts/image.sh")" "$ca"

run() { "$engine" run --network none --read-only --cap-drop ALL --security-opt no-new-privileges "$@"; }
check "version" "node-tracker $version" "$(run --rm --entrypoint /node-tracker "$ref" version | cut -d' ' -f1-2)"

# A node that is down: once saves its state to the /state volume as the
# image's user, with no capabilities, and exits 4 (not in sync).
cfg=$tmp/cfg
mkdir "$cfg"
cat >"$cfg/config.yaml" <<'EOF'
targets:
  - name: test
    node:
      kind: bitcoind
      url: http://127.0.0.1:1
      username: tracker
      password_env: NODE_RPC_PASSWORD
    references: []
notifiers:
  - name: telegram
    kind: telegram
    bot_token_file: /run/secrets/telegram-token
    chat_id: "123456789"
EOF
echo 123456:test-token >"$cfg/telegram-token"
chmod 644 "$cfg/config.yaml" "$cfg/telegram-token"
mounts=(-e NODE_RPC_PASSWORD=x -v "$cfg/config.yaml:/config/config.yaml:ro,Z" -v "$cfg/telegram-token:/run/secrets/telegram-token:ro,Z")
run -d --name "$cname" "${mounts[@]}" "$ref" >/dev/null
for _ in $(seq 50); do "$engine" logs "$cname" 2>&1 | grep -q 'msg="node-tracker started"' && break; sleep 0.2; done
"$engine" stop -t 10 "$cname" >/dev/null
out=$("$engine" logs "$cname" 2>&1)
"$engine" rm -v "$cname" >/dev/null
check "run: the entrypoint starts the daemon" 1 "$(grep -c 'msg="node-tracker started".*state=/state/state.json' <<<"$out")"
check "run: stops on SIGTERM" 1 "$(grep -c 'msg="node-tracker stopped"' <<<"$out")"
run --rm "${mounts[@]}" --entrypoint /node-tracker "$ref" once -config /config/config.yaml -state /state/state.json >/dev/null 2>&1
check "once: state written, node down" 4 "$?"
run --rm "${mounts[@]}" --entrypoint /node-tracker "$ref" check -config /config/config.yaml >"$tmp/check" 2>&1
check "check: node down" 4 "$?"
check "check: config ok" 1 "$(grep -c '^config .*ok' "$tmp/check")"
echo 'targets: [{name: test, nod: {}}]' >"$cfg/config.yaml"
run --rm "${mounts[@]}" --entrypoint /node-tracker "$ref" check -config /config/config.yaml >/dev/null 2>&1
check "check: bad config" 2 "$?"

exit "$fail"
