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
```

CI runs `lint`, `test` and `vuln` on every pull request.

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
