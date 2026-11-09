VERSION    ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT     ?= $(shell git rev-parse --short HEAD 2>/dev/null)
BUILD_TIME ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS    := -s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.buildTime=$(BUILD_TIME)
# Packages are built from the newest stable tag at or below HEAD.
TAG        ?= $(shell git describe --tags --abbrev=0 --match 'v[0-9]*' --exclude '*-*' 2>/dev/null)
RPM_DIR    := $(or $(XDG_CACHE_HOME),$(HOME)/.cache)/node-tracker/rpmbuild

.PHONY: build dist image push-image brew test-image doc-version release ppa gen-spec srpm copr publish test test-ppa test-copr integration lint vuln clean

build:
	CGO_ENABLED=0 go build -trimpath -ldflags '$(LDFLAGS)' -o bin/node-tracker ./cmd/node-tracker

# Release archives + SHA256SUMS in dist/.
dist:
	VERSION='$(VERSION)' LDFLAGS='$(LDFLAGS)' scripts/dist.sh

# Multi-arch container image of the archives in dist/, as an OCI layout in
# dist/image and as localhost/node-tracker:VERSION. Needs podman or docker.
image:
	scripts/image.sh '$(VERSION)' dist dist/image

# Rebuilds the image of stable TAG from its signed release assets and pushes
# it to ghcr.io; 'make release' runs it. Needs a clean checkout of TAG.
push-image:
	scripts/push-image.sh '$(TAG)'

# Builds the image from test binaries and runs it; skipped without podman
# or docker. Needs network for the CA bundle unless it is cached.
test-image:
	scripts/image_test.sh

# Sets the version of the download examples in the docs: make doc-version
# V=1.2.3, before tagging v1.2.3. 'make release' refuses stale ones.
doc-version:
	scripts/doc-version.sh set '$(V)'

# Needs a signed tag at HEAD, pushed to origin; signs SHA256SUMS with gpg.
release:
	scripts/release.sh

# Uploads TAG to the Ubuntu PPA for the Ubuntu releases that lack it; signs
# with gpg. Rebuild an uploaded version with PPA_REV=<next revision>.
ppa:
	ppa/build-ppa.sh '$(TAG)' $(PPA_REV)

# The RPM spec of TAG, from node-tracker.spec.
gen-spec:
	install -d -m 700 $(RPM_DIR)/SPECS
	scripts/gen-spec.sh '$(TAG)' $(RPM_DIR)/SPECS/node-tracker.spec

# The source RPM of TAG in $(RPM_DIR)/SRPMS.
srpm:
	scripts/srpm.sh '$(TAG)'

# Builds TAG in Fedora COPR for the supported Fedoras and arches that lack it.
# COPR_FORCE=1 rebuilds; COPR_CHROOTS="fedora-44-x86_64 ..." picks chroots.
copr:
	scripts/copr-build.sh '$(TAG)'

# Publishes the Homebrew formula of the stable release TAG to the tap, from
# the release's signed SHA256SUMS; make release runs it.
brew:
	scripts/push-brew.sh '$(TAG)'

publish: release ppa copr

test:
	go test -race ./...

# Tests of the packaging scripts; no network.
test-ppa:
	ppa/build-ppa_test.sh
	scripts/pkg-changelog_test.sh
	scripts/install-units_test.sh
	scripts/deb_test.sh
	scripts/push-image_test.sh
	scripts/push-brew_test.sh
	scripts/doc-version_test.sh

test-copr:
	scripts/copr-build_test.sh
	scripts/pkg-changelog_test.sh

# Needs bitcoind in PATH; starts a throwaway regtest node.
integration:
	go test -race -tags integration -run Integration -count=1 ./...

lint:
	test -z "$$(gofmt -l .)" || (gofmt -l . && exit 1)
	go vet ./...
	go tool staticcheck ./...
	shellcheck $$(git ls-files '*.sh')

vuln:
	go tool govulncheck ./...

clean:
	rm -rf bin dist
