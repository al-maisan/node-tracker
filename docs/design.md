# node-tracker — design

Status: implemented (v1). This document explains *why* things are the way they are;
the [README](../README.md) and the other docs explain *how* to use them.

## 1. Goal

Watch a blockchain node and tell a human when it falls behind the chain tip,
and again when it has caught up.

v1 scope:

- chain: Bitcoin (`bitcoind` JSON-RPC)
- alerts: Telegram

Later (the design must allow these without reshaping the core):

- chain: Ethereum (JSON-RPC `eth_blockNumber`, `eth_syncing`)
- alerts: Slack, email, …

Non-goals: dashboards, alert history storage, managing or restarting the
node.

## 2. Key definitions

| Term | Meaning |
|---|---|
| **target** | one node being watched (e.g. `orendil`) |
| **node height** | the last block the node has fully validated (`getblockchaininfo.blocks`) |
| **reference** | an independent source of the chain tip height (another node, a public API) |
| **tip** | `max(reference heights, node header height)` |
| **lag** | `tip - node height`, in blocks, never below 0 |
| **threshold** | configured lag limit, in blocks |

### Why external references?

The node's own header height (`getblockchaininfo.headers`) only shows how far
the node is behind *what the node knows*. A node that lost its peers, or is
eclipsed, sees no new headers and reports lag 0 while the world moves on.
Independent references catch that. The node's own header height is still
used as one more input, so a node that knows it is behind is caught even when
every reference is down.

### Why `max` and not median? And what about a lying reference?

- A reference that is behind or stuck can never hide a lagging node: `max`
  ignores it.
- A reference that lies *high* can only cause a false alarm, never a missed
  one. A false alarm is visible and easy to debug; a missed alarm is not.
- The alert message names the reference that set the tip, so a bad reference
  is easy to spot.
- A single reference that is broken or tampered with (e.g. reached over
  plain http) could still pin the target in `LAGGING` with an absurd
  height. With **three or more** responding references, a reference more
  than `reference_outlier` blocks (default 6) **above every other
  reference** is ignored and logged.
  - Two references can't outvote each other: with two that disagree, the
    lower one may be stuck along with the node (a local electrs), so the
    higher one wins.
  - The node's own headers are not part of the comparison: an isolated
    node's headers are behind by definition, and must not make honest
    references look like outliers.

## 3. Health states

Each target is always in exactly one state:

| State | Meaning |
|---|---|
| `OK` | lag ≤ threshold |
| `LAGGING` | lag > threshold |
| `NODE_DOWN` | the node RPC call failed |
| `NO_REFERENCE` | the node is up, but every external reference failed |

Rules:

- An alert is sent **only on a state change** (no repeats while the state
  stays the same). This is the "crossed upward / downward" behaviour:
  `OK → LAGGING` is the upward crossing, `LAGGING → OK` the downward one.
- **Debounce:** a change must be seen for `for` samples in a row (default
  2) before the state changes. This stops flapping, e.g. when a reference
  sees a new block a few seconds before the node does. Samples count in two
  groups:
  - The bad states (`LAGGING`, `NODE_DOWN`, `NO_REFERENCE`) count together,
    and the target takes the latest one. Otherwise a node alternating
    between `LAGGING` and `NODE_DOWN` would reset the counter forever and
    never alert.
  - `OK` counts on its own: one good sample after trouble is not recovery.
- **Optional hysteresis:** `recover_threshold` (default = `threshold`). The
  state goes back to `OK` only when lag ≤ `recover_threshold`. The last
  OK/LAGGING verdict is stored separately from the current state, so the
  hysteresis also holds when a lagging node passes through `NODE_DOWN`
  (e.g. a restart): it is not reported "in sync" while still behind.
- **Precedence:** `NODE_DOWN`, then `LAGGING`, then `NO_REFERENCE`, then
  `OK`. When every reference fails, the node's own header height still
  counts, so a node that knows it is behind is reported `LAGGING`, with
  the reference errors attached.
- **First run:** state is saved to disk (§4), so this rule applies only the
  very first time a target is seen, or after its state file is deleted. That
  first stable state is set silently if it is `OK`; anything else is
  alerted. Restarts and cron runs continue from the saved state, so they
  never spam "recovered".
- `NO_REFERENCE` exists so the tracker never goes blind silently.
- Error text from sources is capped (300 characters) before it reaches the
  state file, logs or alerts. It can come from untrusted nodes.

The logic lives in package `health`, as two pure functions with no I/O, so
it is fully unit-testable:

- `Observe(sample, rule, current) → observation` classifies one sample.
  The current state is needed for hysteresis.
- `Step(target, observed, rule, now) → (target, alert)` debounces.

## 4. Architecture

### Two ways to run, one code path

The tool runs in one of two modes:

- `node-tracker run` is a daemon. It runs `tick` every `interval`.
- `node-tracker once` runs `tick` one time and exits. Use it from cron or a
  systemd timer. cron's smallest step is one minute; a systemd timer can go
  down to seconds.

Supported ways to start it (all are documented, and the systemd units ship
in `deploy/`):

| How | Mode | Notes |
|---|---|---|
| system systemd service | `run` | dedicated user; `StateDirectory=` holds the state |
| user systemd service (`systemctl --user`) | `run` | no root needed; `loginctl enable-linger` keeps it running after logout |
| systemd timer (system or user) | `once` | intervals down to seconds |
| cron | `once` | intervals of one minute or more |
| by hand in a terminal | `check` / `once` / `report` / `test-notify` | human-readable output |

Default paths follow the XDG base directory spec, so a user service and a
manual run need no flags:

- config: `$XDG_CONFIG_HOME/node-tracker/config.yaml`
  (falls back to `~/.config/node-tracker/config.yaml`)
- state: `$XDG_STATE_HOME/node-tracker/state.json`
  (falls back to `~/.local/state/node-tracker/state.json`)
- the system unit overrides both with `-config` and `-state`

The daemon and the timer/cron modes share one state format. You can
therefore switch between them, and the lock stops two of them from running
at the same time.

Both modes run the same `tick`:

```
tick:
  1. lock + load state file          (per-target state, debounce counters, outbox)
  2. sample all targets in parallel  (node + references, each call with its own timeout)
  3. state machine (pure)            → new state, events appended to the outbox
  4. deliver outbox                  (telegram; slack, email later), bounded by a deadline
  5. save state file atomically      (delivered events removed), unlock
```

The daemon is just `lock; load; for { tick; save; wait interval }`. It
takes the lock once and holds it for its whole lifetime, so a stray cron job
can't interleave. Both modes therefore behave the same, and a test checks
that they send identical alerts.

- **State file** (`state_file`, JSON) keeps, for each target: the current
  state, the candidate state with its "seen N times in a row" counter, and
  the time of the last change.
  - Writes are atomic (temp file + `fsync` + `rename`), so a crash never
    leaves a half-written file.
  - A corrupt or unreadable file is a hard error, with a clear message. It is
    never silently reset, because a reset could hide or repeat alerts.
- **Lock.** An exclusive `flock` on `<state_file>.lock` protects the whole
  tick. If cron runs overlap, or a daemon and a cron job share one state
  file, the second process logs it and exits with code 3. It never corrupts
  state and never sends a duplicate alert.
- **Outbox = no lost alerts.** A state change is first written to the
  outbox, then sent. Each event remembers which notifiers still have to
  get it.
  - A send that fails (Telegram down, network issue) stays in the outbox
    and is retried on the next tick. The state change itself is never
    lost.
  - Delivery is **at-least-once**: a crash between "sent" and "saved" can
    cause one duplicate, never a missed alert.
  - The outbox is capped (default 100 events per notifier). When full, the
    oldest event is dropped and an error is logged.
  - Messages carry the event time, so a late delivery is clearly late.
  - **A rejected message can't block the queue.** A permanent error is
    either about the channel (bad token: every message fails) or about
    one message (e.g. its content is refused). node-tracker doesn't guess
    from error texts; it tries the next event. If that one is delivered,
    the rejected event is dropped and an error is logged. If it fails too,
    the channel is broken and everything is kept.
  - `once` exits with code 1 while alerts are still undelivered, so
    cron and systemd notice a broken alert path.
- **Retries inside a tick.** Each send retries a few times with exponential
  backoff plus jitter, and honours Telegram's `429 retry_after`. All of it
  must finish before a delivery deadline (default: half the interval in
  `run` mode, `timeout × 3` in `once` mode). A slow notifier therefore can't
  stall monitoring, and a cron run always ends.
- **Shutdown.** SIGINT/SIGTERM cancel the context.
  - If that happens while sampling, the tick is discarded. A sample cut
    short by shutdown is not a failure, and must not become `NODE_DOWN`.
  - If it happens while delivering, the state is saved, and anything not
    yet delivered stays in the outbox for the next start.

### Interfaces (the extension points)

```go
// package source
type Height struct {
    Blocks  uint64 // fully validated blocks
    Headers uint64 // best known header; equal to Blocks if the source has no such notion
}

type Source interface {
    Name() string
    Height(ctx context.Context) (Height, error)
}

// package notify
type Message struct {
    Subject string // one line
    Body    string // plain text
}

type Notifier interface {
    Name() string
    Send(ctx context.Context, m Message) error
}
```

Notifiers get a rendered `Message`, not raw data. All wording therefore
lives in one place (`notify/message.go`), and each notifier only decides
the presentation: Telegram shows the subject in bold, an email notifier
would use it as the mail subject.

Alerts are stored in the outbox as an `Event`: plain data (no error values,
no pointers) that is safe to serialise as JSON. An event is rendered only
when it is sent, so changing the wording never invalidates stored state.

To add **Ethereum**, write an `ethrpc` source (`eth_blockNumber`, plus
`eth_syncing` for headers). Thresholds are per target, so they work at any
block time (≈10 min vs 12 s).

To add **Slack or email**, implement `Notifier`. Adding a new kind touches
two places:

1. `config`: list the kind, then validate and resolve its fields (secrets
   included).
2. `app`: map the kind to its constructor.

A test fails if a kind the config accepts has no constructor.

### Package layout

```
cmd/node-tracker/            subcommands, flags, exit codes, signals
internal/config/             YAML schema, defaults, XDG paths, validation, secrets
internal/app/                kind → implementation wiring
internal/health/             pure logic: classify a sample, debounce (no I/O)
internal/monitor/            tick: sample → classify → debounce → outbox → deliver
internal/state/              state file: flock, atomic save, outbox
internal/source/             Source interface, height sanity checks
internal/source/bitcoind/    JSON-RPC (cookie re-read per call, or user/password)
internal/source/esplora/     Esplora REST
internal/notify/             Message, Event, rendering, retry/backoff
internal/notify/telegram/    Bot API
internal/httpx/              hardened HTTP client, body limits, URL redaction
internal/service/            install-service: render, write and enable the units
deploy/                      the units (embedded by deploy.go, so install-service writes the shipped ones), cron example
examples/  docs/
```

Everything lives under `internal/`: the tool's code is not a public library
API, so it can change freely.

## 5. Configuration

One YAML file; the path comes from `-config` or the XDG default (§4).
Unknown keys are rejected so typos fail loudly, and every error is reported
in one go. The full reference is in [configuration.md](configuration.md),
and an annotated example in
[examples/config.yaml](../examples/config.yaml).

Required values have no hidden defaults: `alert.threshold` must be set,
because a sensible value depends on the chain and on the user's tolerance.

## 6. Security

- **Secrets** (RPC password, bot token) come only from a file (`*_file`) or
  an environment variable (`*_env`), never inline in the YAML. The config
  file can then be shared or committed safely. A secret file that others can
  access triggers a warning.
- **Secrets never reach logs or alerts:**
  - Resolved secrets have a `Secret` type that prints as `REDACTED`.
  - Every HTTP error is cut down to scheme and host before it leaves
    `httpx`. This matters because the Telegram token is part of the URL
    path.
- **Untrusted input:**
  - Response bodies are size-limited.
  - Heights must parse and lie in `[0, 1e9]`.
  - Redirects are never followed.
  - Telegram messages are HTML-escaped, so node data can't inject markup.
- **Transport:**
  - URLs may not contain credentials.
  - Plain `http://` is accepted only for loopback hosts, and for `.onion`
    hosts through a loopback proxy, unless `allow_insecure: true` is set
    (LAN).
- **Proxy / Tor:** `HTTPS_PROXY` / `NO_PROXY` are honoured, including
  `socks5://`, so references and Telegram can go through Tor without
  exposing the operator's IP. An endpoint with `proxy:` gets its own client,
  hardened the same way, that uses only that proxy. `.onion` endpoints
  without one use the environment's proxy, else Tor's
  `socks5://127.0.0.1:9050`, and a per-call timeout of at least 30s. Plain
  http to them needs no `allow_insecure` only through a loopback proxy.
- **Least privilege:**
  - The tool runs as an unprivileged user.
  - The system unit is hardened: empty capability set, `ProtectSystem=strict`,
    `SystemCallFilter=@system-service`, `MemoryDenyWriteExecute`, and more.
  - Only `getblockchaininfo` is called, and the docs show how to limit the
    RPC user to it with `rpcwhitelist`.
  - `install-service` runs `systemctl`, `loginctl` and `useradd` directly,
    without a shell. The unit gets only validated paths, and never a
    secret. For `-system` only root may be able to change the binary and
    the directories above it, since the service can read the secrets.
- **State file** is created `0600` in a `0700` directory. It holds heights
  and states only, never secrets.

## 7. Observability

### Command line

| Command | What it does |
|---|---|
| `node-tracker run` | the daemon (§4) |
| `node-tracker once` | one tick, then exit (cron / timer / manual) |
| `node-tracker check` | dry run: validate the config, sample every target, print a table. Does not touch state and sends no alerts |
| `node-tracker report` | send a status report of every target to every notifier, whatever the state. Not queued: a report is worthless if late |
| `node-tracker test-notify [name]` | send a test message through one notifier, or all of them. Useful during setup |
| `node-tracker install-service -user\|-system` | write the shipped unit with `ExecStart=` set to the running binary and enable it; `-system` also creates the user and installs the config and secrets (see operations.md) |
| `node-tracker version` | version, short git commit, build time (from `make build` ldflags; a plain `go install` falls back to the build info Go embeds), Go version |

`check` prints a table like this:

```
TARGET   STATE  NODE    TIP     LAG  TIP FROM               ERROR
orendil  OK     974084  974085  1    esplora:mempool.space

TARGET   ENDPOINT                 PROXY
orendil  bitcoind:127.0.0.1:8332  none
orendil  esplora:mempool.space    socks5://127.0.0.1:9050 (environment)
```

Exit codes are chosen so cron and systemd can act on them:

| Code | Meaning |
|---|---|
| 0 | ok (for `once` / `check` / `report`: every target was observed `OK`) |
| 1 | runtime failure (state file unreadable, alerts not delivered yet, etc.) |
| 2 | bad config or bad usage |
| 3 | another instance holds the lock |
| 4 | `once` / `check` / `report`: at least one target was observed not `OK` in this run |

Config errors go to stderr as plain multi-line text, not as a log line,
because a person reads them.

### Logs

- Logs use `log/slog` and go to stderr. The format is `text` by default
  and `json` with `-log-format json`. journald adds its own timestamps.
- Each sample is logged at debug level; state changes at info; failures of
  sources or deliveries at warn/error.

## 8. Dependencies

- Go standard library only, plus one YAML parser (`go.yaml.in/yaml/v3`).
- No Telegram, JSON-RPC or retry libraries: each is a few dozen lines and
  easier to audit than a dependency.

## 9. Testing and quality

- **State machine:** table-driven tests for every transition, debounce,
  hysteresis and the startup rule.
- **Sources and notifiers:** tested against `httptest` servers, covering
  happy path, auth failure, timeout, malformed and oversized responses,
  and 429 handling.
- **Config:** valid and invalid fixtures, plus a check that no secret
  appears in any error message.
- **Tick:** fake sources and notifiers plus a fake clock, so tests are
  deterministic and need no sleeps. Each case runs as N separate `once`
  runs and as one daemon running N ticks, and the alerts must be identical.
- **State file:** atomic write, a corrupt file causes a hard error, lock
  conflicts give exit code 3, failed deliveries stay in the outbox and are
  redelivered, the outbox cap is enforced.
- **CLI end to end:** `run()` against fake bitcoind, Esplora and Telegram
  servers: crossing up and down over several `once` runs, `check`,
  `report`, `test-notify`, lock conflicts, usage errors.
- **Integration (build tag `integration`, `make integration`):** starts a
  throwaway `bitcoind -regtest`, mines blocks and checks the reported
  heights.
- **CI (GitHub Actions):** `gofmt`, `go vet`, `staticcheck`, `go test
  -race`, `govulncheck`.

## 10. Out of scope for v1 (possible later)

- Ethereum source, Slack and email notifiers (designed for, see §4)
- Prometheus `/metrics` endpoint
- Repeat reminders while `LAGGING`
- "Watch the watcher" heartbeat (dead-man switch)
- Container image
- Windows support (the state lock uses `flock`)

Release archives and signed checksums: `make dist` / `make release`, see
CONTRIBUTING.md. Homebrew: `brew install al-maisan/tap/node-tracker`, the
formula is written by `make release`. Ubuntu: `ppa:al-maisan/node-tracker`,
uploaded by `make ppa`. Fedora: COPR `al-maisan/node-tracker`, built by
`make copr`. Other packaging (AUR) is tracked in issue #8.
