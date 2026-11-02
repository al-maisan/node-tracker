# Contributing

Thanks for helping. Bug reports, docs fixes and new sources or notifiers are
all welcome.

## Before you start

- For anything bigger than a small fix, please open an issue first so we can
  agree on the approach.
- Read [docs/design.md](docs/design.md). It explains the decisions a change
  should fit into.

## Development

```sh
make test         # unit + end-to-end tests with the race detector
make lint         # gofmt, go vet, staticcheck, shellcheck
make vuln         # govulncheck
make integration  # needs bitcoind in PATH; starts a throwaway regtest node
make build        # bin/node-tracker with version, commit and build time
make dist         # release archives, .deb packages + SHA256SUMS in dist/ (GNU tar, dpkg-deb)
make test-ppa     # tests of the packaging and release scripts
make test-copr    # tests of the COPR scripts
make image        # container image of dist/: OCI layout in dist/image (podman or docker)
make test-image   # builds and runs a test image; skipped without podman or docker
make srpm         # source RPM of the newest stable tag (rpmbuild)
```

`make lint` needs shellcheck (Fedora: `sudo dnf install ShellCheck`;
Debian/Ubuntu: `sudo apt install shellcheck`).

CI runs `lint`, `test`, `test-ppa`, `test-copr`, `vuln` and `dist`, and
checks the Homebrew formula syntax, on every pull request.

A new command or flag also goes into the man page, `node-tracker.1`;
`make test` fails until it does. Check the rendering with
`man -l node-tracker.1`.

## Releasing

1. Set the version of the download examples in the docs (README,
   `docs/install.md`): `make doc-version V=1.2.3`, then merge that change.
   `make release` refuses a stable tag whose docs show another version.
2. On an up-to-date `main`, tag the release commit with a signed tag and
   push it: `git tag -s v1.2.3 -m v1.2.3 && git push origin v1.2.3`
3. Run `make release`. It stops unless the tree is clean, HEAD is on
   `main` of `origin`, and the one `v*` tag at HEAD is signed by the
   release key (`RELEASE_KEY`) and is the same tag object on GitHub. Then
   it runs `lint`, `test` and `vuln`, builds `dist/`, signs `SHA256SUMS`
   with the release key and creates the GitHub release with `gh`. A
   stable release also gets `.deb` packages for amd64, arm64 and armhf
   (`scripts/deb.sh`), so the release machine needs `dpkg-deb` (Fedora:
   `sudo dnf install dpkg`).
4. For a stable release (no `-PRE`), `make release` also writes the
   Homebrew formula `dist/node-tracker.rb` (`scripts/brew-formula.sh`) and
   prints a one-liner that commits it to `Formula/node-tracker.rb` in
   `al-maisan/homebrew-tap`. Check the formula, then run the one-liner.
   It also builds the container image from the linux archives
   (`scripts/image.sh`, to `dist/image`) before the GitHub release. Last,
   after the formula step is printed, it runs `make push-image`
   (`scripts/push-image.sh`): that rebuilds the image from the linux
   archives of the GitHub release, checked against its signed
   `SHA256SUMS`, pushes it to `ghcr.io/al-maisan/node-tracker` as
   `vX.Y.Z` with `skopeo`, checks that the pushed digest is the one in
   `dist/image/index.json`, and prints it. `latest` moves only if the tag
   is the highest stable tag on GitHub, so a fix release of an older
   version leaves it alone. This needs podman (or docker), skopeo and gpg
   (`sudo dnf install podman skopeo`) and a login to ghcr.io:
   `podman login ghcr.io -u al-maisan` with a personal access token
   (classic) that has `write:packages`. `make release` stops before the
   GitHub release if you are not logged in. If only the push fails, fix
   the cause and run `git checkout vX.Y.Z && make push-image TAG=vX.Y.Z`;
   it can be rerun at any time. After the first push, make the package
   public in its settings on GitHub, once.
5. Run `make ppa` to upload the tag to `ppa:al-maisan/node-tracker`. It
   builds the source package of the newest stable tag at or below HEAD
   (`TAG`) with the vendored modules, so Launchpad builds it offline, and
   signs it with the release key (two key touches per Ubuntu release).
   Ubuntu releases that already have the version are skipped; after a
   packaging fix, `make ppa PPA_REV=2` uploads the next revision with the
   same orig tarball. The packaging (`ppa/debian`,
   `scripts/install-units.sh`, `scripts/deb-layout.sh`,
   `deploy/sysusers.d`) comes from the checkout, so a fix also applies to
   older tags. The release `.deb` uses the same `deb-layout.sh` and a copy
   of the PPA package's maintainer scripts (`scripts/deb/`); `make
   test-ppa` fails when a unit change needs a fresh copy. It needs `go`,
   `curl`, `python3`, `debuild` and `dput`.
6. Run `make copr` to build the tag in COPR `al-maisan/node-tracker`. It
   builds for every Fedora that is released (fedoraproject.org) and still
   maintained (Bodhi), on x86_64 and aarch64, and enables new chroots in
   the project. Chroots that already have the version are skipped;
   `COPR_FORCE=1` rebuilds, `COPR_CHROOTS="fedora-44-x86_64"` picks
   chroots. It uploads a source RPM (`make srpm`; packaging from
   `node-tracker.spec` in the checkout) with `copr-cli`, so it needs a
   COPR API token in `~/.config/copr` but no key touch. It also needs
   `rpmbuild`.

`make publish` runs `release`, `ppa` and `copr`.

The package changelogs list the commits since the previous GitHub release,
so run `make ppa` and `make copr` after `make release`.

Release builds use the official Go toolchain pinned in
`scripts/release.sh` (`RELEASE_GO`; bump it with Go patch releases) and
the commit time as build time, so the archives are reproducible:
`git checkout v1.2.3 && GOTOOLCHAIN=<Go version from node-tracker version> make dist VERSION=v1.2.3 BUILD_TIME=<build time from node-tracker version>`
gives the same `SHA256SUMS` (for the `.deb` files, given the same
`dpkg-deb` and xz versions). Then `make image VERSION=v1.2.3` gives the
same image digest as the release printed, given the same podman (or
docker) version; `skopeo inspect --raw docker://ghcr.io/al-maisan/node-tracker:v1.2.3 | sha256sum`
shows the digest on ghcr.io. The CA certificates are pinned by URL and
hash in `scripts/image.sh`; update both from time to time.

## Guidelines

- Keep dependencies at the standard library plus the YAML parser. A new
  dependency needs a good reason.
- New behaviour comes with tests. Logic that can be pure (see
  `internal/health`) should be pure.
- Never log or return secrets. Wrap HTTP errors with `httpx.Redact`.
- Update the docs in the same pull request.

## Adding a source or notifier

1. Implement `source.Source` or `notify.Notifier` in its own package under
   `internal/source/` or `internal/notify/`, with `httptest`-based tests.
2. `internal/config`: add the kind, validate its fields and resolve its
   secrets.
3. `internal/app`: map the kind to its constructor. A test fails if you
   forget this step.
4. Document the kind in `docs/configuration.md`.
