#!/bin/bash
# Record docs/demo/demo.gif from demo.tape with vhs. node-tracker talks to a
# local fake of bitcoind, Esplora and the Telegram Bot API (fake/main.go, kept
# out of ./... by a build tag), inside a container without network, so nothing
# leaves the machine. The node is stuck 6 blocks behind the tip. Needs docker,
# go and git.
# Usage: docs/demo/record.sh
set -euo pipefail

DEMO=$(cd "$(dirname "$0")" && pwd)
SRC=$(cd "$DEMO/../.." && pwd)
WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT

# Same ldflags as the Makefile.
make -C "$SRC" -s build
cp "$SRC/bin/node-tracker" "$WORK/"
(cd "$SRC" && CGO_ENABLED=0 go build -trimpath -o "$WORK/fake" docs/demo/fake/main.go)
cp "$DEMO/config.yaml" "$DEMO/demo.tape" "$WORK/"
chmod 755 "$WORK" && chmod 644 "$WORK"/*.yaml "$WORK"/*.tape

# vhs v0.12.1, pinned: its output is committed.
VHS=ghcr.io/charmbracelet/vhs@sha256:ea49a6a1c529be83153e88321892b5585964418f0b9055e8c1e0d732194234a3
# mempool.space is the fake's TLS listener (fake/main.go), so the reference
# is not on the node's host.
docker run --rm --network none --add-host mempool.space:127.0.0.1 -v "$WORK:/vhs" -w /vhs "$VHS" demo.tape

cat "$WORK/telegram.log"
grep -q 'node-tracker test message' "$WORK/telegram.log" && grep -q 'orendil is lagging' "$WORK/telegram.log" && grep -q 'esplora:mempool.space' "$WORK/telegram.log" ||
    { echo "fake Telegram did not get the test message and the alert from esplora:mempool.space" >&2; exit 1; }
cp "$WORK/demo.gif" "$DEMO/demo.gif"
echo "wrote $DEMO/demo.gif ($(du -h "$DEMO/demo.gif" | cut -f1))"
