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
make lint         # gofmt, go vet, staticcheck
make vuln         # govulncheck
make integration  # needs bitcoind in PATH; starts a throwaway regtest node
make build        # bin/node-tracker with version, commit and build time
make dist         # release archives + SHA256SUMS in dist/ (needs GNU tar)
```

CI runs `lint`, `test`, `vuln` and `dist`, and checks the Homebrew formula
syntax, on every pull request.

## Releasing

1. On an up-to-date `main`, tag the release commit with a signed tag and
   push it: `git tag -s v1.2.3 -m v1.2.3 && git push origin v1.2.3`
2. Run `make release`. It stops unless the tree is clean, HEAD is on
   `main` of `origin`, and the one `v*` tag at HEAD is signed by the
   release key (`RELEASE_KEY`) and is the same tag object on GitHub. Then
   it runs `lint`, `test` and `vuln`, builds `dist/`, signs `SHA256SUMS`
   with the release key and creates the GitHub release with `gh`.
3. For a stable release (no `-PRE`), `make release` also writes the
   Homebrew formula `dist/node-tracker.rb` (`scripts/brew-formula.sh`) and
   prints a one-liner that commits it to `Formula/node-tracker.rb` in
   `al-maisan/homebrew-tap`. Check the formula, then run the one-liner.

Release builds use the official Go toolchain pinned in
`scripts/release.sh` (`RELEASE_GO`; bump it with Go patch releases) and
the commit time as build time, so the archives are reproducible:
`git checkout v1.2.3 && GOTOOLCHAIN=<Go version from node-tracker version> make dist VERSION=v1.2.3 BUILD_TIME=<build time from node-tracker version>`
gives the same `SHA256SUMS`.

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
