VERSION    ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT     ?= $(shell git rev-parse --short HEAD 2>/dev/null)
BUILD_TIME ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS    := -s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.buildTime=$(BUILD_TIME)

.PHONY: build dist release test integration lint vuln clean

build:
	CGO_ENABLED=0 go build -trimpath -ldflags '$(LDFLAGS)' -o bin/node-tracker ./cmd/node-tracker

# Release archives + SHA256SUMS in dist/.
dist:
	VERSION='$(VERSION)' LDFLAGS='$(LDFLAGS)' scripts/dist.sh

# Needs a signed tag at HEAD, pushed to origin; signs SHA256SUMS with gpg.
release:
	scripts/release.sh

test:
	go test -race ./...

# Needs bitcoind in PATH; starts a throwaway regtest node.
integration:
	go test -race -tags integration -run Integration -count=1 ./...

lint:
	test -z "$$(gofmt -l .)" || (gofmt -l . && exit 1)
	go vet ./...
	go tool staticcheck ./...

vuln:
	go tool govulncheck ./...

clean:
	rm -rf bin dist
