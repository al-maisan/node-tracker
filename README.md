<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="assets/logo-dark.svg">
    <img src="assets/logo.svg" alt="node-tracker logo: a gauge showing how far the node is behind the chain tip, and the name node-tracker" width="389">
  </picture>
</p>

# node-tracker

**Get a Telegram message when your Bitcoin node falls behind the chain tip, and another when it catches up.**

**Install:** the `.deb` on Debian, Ubuntu and Raspberry Pi OS,
`brew install al-maisan/tap/node-tracker` on macOS; see [Install](#install)
for these, the signed release archive (any Linux) and `go install`. Then
follow the [Quick start](#quick-start).

A node that has lost its peers, is eclipsed, or hangs after a crash often
*looks* healthy: the process runs, the RPC port answers, and the node thinks
it is in sync because it hears of no new blocks. node-tracker compares your
node with independent views of the chain (mempool.space, blockstream.info or
your own nodes) and tells you when the gap crosses a threshold, in either
direction.

```text
$ node-tracker check
TARGET   STATE    NODE    TIP     LAG  TIP FROM               ERROR
orendil  LAGGING  974079  974085  6    esplora:mempool.space

config             ok
target orendil     node reachable (cookie) · references 2/2 · LAGGING by 6 is the node's current state, not a setup error
notifier telegram  token valid (@orendil_bot) · chat not tested → run 'node-tracker test-notify telegram'
```

…and on your phone:

> **🔴 orendil is lagging**
>
> Node height: 974,079
> Network height: 974,085 (esplora:mempool.space)
> Lag: 6 blocks
> Was: in sync
> At: 2026-11-01 08:30:00 UTC

![node-tracker demo: check, test-notify, then two once runs raising a LAGGING alert](docs/demo/demo.gif)

The demo runs against local fakes of bitcoind, Esplora and Telegram; see
[`docs/demo/`](docs/demo/). Pointed at the real Telegram, the same run
delivers this:

![Telegram: the node-tracker test message, then "orendil is lagging" with node height 974,079, network height 974,085 and a lag of 6 blocks](docs/demo/telegram-alert.png)

## Features

- **Catches the silent failures.** Your node's height is checked against
  independent references *and* against its own header chain, so it catches
  an isolated node as well as one that is stuck validating.
- **Alerts on change, not on every check.** One message when the node falls
  behind and one when it is back. Changes are debounced, and an optional
  recovery threshold stops the alerts from flapping.
- **Never goes blind quietly.** If the node is unreachable, or every
  reference fails, you get an alert for that too.
- **Alerts are never lost.** Alerts wait in a queue in the state file until
  they are delivered. A Telegram outage or a restart delays an alert but
  never drops it.
- **Run it your way.** As a daemon, from cron, as a systemd timer, as a user
  service, or by hand. All of them behave the same, and a lock stops two
  copies from clashing.
- **Hourly status reports** (optional) so you know the alerting itself still
  works.
- **Secure by default.** Secrets come only from files or environment
  variables, never from the config file. Tokens are scrubbed from errors and
  logs. Plain HTTP is allowed only to localhost and to `.onion` hosts
  through a local Tor. Tor/SOCKS5 proxies are supported, per endpoint too,
  and only the read-only `getblockchaininfo` RPC is used.
- **One small static binary.** The Go standard library plus a YAML parser;
  no runtime and no database.

## Install

The easiest way first:

- **`.deb` package** (Debian, Ubuntu, Raspberry Pi OS; amd64, arm64,
  armhf): download the `.deb` for your system from the
  [releases page](https://github.com/al-maisan/node-tracker/releases),
  together with `SHA256SUMS` and `SHA256SUMS.asc`, into one directory.
  [Verify it](#verify-a-download), then install it (example for `v1.1.0`
  on arm64, e.g. a Raspberry Pi with a 64-bit OS):

  ```sh
  sudo apt install ./node-tracker_1.1.0-1_arm64.deb
  ```

  If apt notes `Download is performed unsandboxed as root … couldn't be
  accessed by user '_apt'`, that is harmless: your home directory is
  private, so apt reads the file as root.

  It installs `/usr/bin/node-tracker` with its man page, the systemd units
  (none is enabled) and a `node-tracker` system user. The docs and example
  config are in `/usr/share/doc/node-tracker`; see
  [docs/operations.md](docs/operations.md#from-a-package) for the setup.
  `.deb` files ship from v1.1.0 on.

- **Homebrew** (macOS, Linux amd64/arm64):

  ```sh
  brew install al-maisan/tap/node-tracker
  ```

  The docs are then in `$(brew --prefix)/share/doc/node-tracker`, the
  systemd units and example config in `$(brew --prefix)/share/node-tracker`,
  and `man node-tracker` works.

  To run it in the background (launchd on macOS, a systemd user service on
  Linux), put the config in `~/.config/node-tracker/config.yaml` (or run
  `node-tracker setup`), check it, then start the service:

  ```sh
  node-tracker check
  brew services start node-tracker
  ```

  Start it once `check` ends with `config ok`. A node that is behind (exit
  code 4) is no reason to wait: that is what the service alerts about.

  It logs to `$(brew --prefix)/var/log/node-tracker.log`. On Linux, also
  run `loginctl enable-linger` to keep it running after you log out.

- **Release archive** (any Linux: amd64, arm64, armv6 for a 32-bit OS on
  any Raspberry Pi, armv7 up to v1.0.0; macOS amd64, arm64): download the
  archive for your system from the
  [releases page](https://github.com/al-maisan/node-tracker/releases),
  together with `SHA256SUMS` and `SHA256SUMS.asc`, into one directory.
  [Verify it](#verify-a-download), then unpack it and put the binary on
  your `PATH` (example for `v1.1.0` on Linux amd64). `/usr/local/bin` also
  suits the system service, which needs a root-owned path:

  ```sh
  tar -xzf node-tracker_1.1.0_linux_amd64.tar.gz && sudo install -m 755 node-tracker_1.1.0_linux_amd64/node-tracker /usr/local/bin/
  ```

  The archive holds the binary, its man page (`node-tracker.1`), this
  README, the docs, the systemd units and an example config.

- **Go 1.26+:**

  ```sh
  go install github.com/al-maisan/node-tracker/cmd/node-tracker@latest
  ```

  This puts `node-tracker` in `~/go/bin`, which must be on your `PATH`. Or
  build from a clone: `make build` puts it in `bin/node-tracker`.

### Verify a download

In the directory with the download, `SHA256SUMS` and `SHA256SUMS.asc`:

1. Import the release key:

   ```sh
   curl -s https://github.com/al-maisan.gpg | gpg --import
   ```

   You should see `key 1E0A288397AE739E: public key "Muharem Hrnjadovic
   …" imported` (or `not changed`). Lines such as `420 signatures not
   checked due to missing keys` and `no ultimately trusted keys found`
   are normal.

2. Check the signature of the checksums:

   ```sh
   gpg --verify SHA256SUMS.asc SHA256SUMS
   ```

   You should see `Good signature from "Muharem Hrnjadovic …"` and
   `using RSA key 753B6ECF2B458FF3D19D568C1E0A288397AE739E`. The warning
   that the key is not certified is expected; any other key, or `BAD
   signature`, means stop.

3. Check the download against them:

   ```sh
   sha256sum --ignore-missing -c SHA256SUMS
   ```

   You should see your file's name followed by `OK`, e.g.
   `node-tracker_1.1.0-1_arm64.deb: OK`. On macOS use
   `shasum -a 256 --ignore-missing -c SHA256SUMS`.

For a script, this checks the same and fails unless the release key
signed:

```sh
gpg --status-fd 1 --verify SHA256SUMS.asc SHA256SUMS 2>/dev/null | grep -q '^\[GNUPG:\] VALIDSIG .* 753B6ECF2B458FF3D19D568C1E0A288397AE739E$' && sha256sum --ignore-missing -c SHA256SUMS
```

## Quick start

1. **Install** node-tracker; see [Install](#install).

2. **Run the setup wizard:**

   ```sh
   node-tracker setup          # or: sudo node-tracker setup, for the system service
   ```

   It finds your local bitcoind (or asks for your node's URL, `.onion`
   included, and makes the `rpcauth=` line for its `bitcoin.conf`), asks
   for the token of a Telegram bot you create with @BotFather, and shows a
   link: tap it, press Start, and the chat is found. Each answer is tested
   right away. Then it sends a test message, writes the config, runs
   `check` and offers to install and start the service. Nothing is written
   before the end.

3. **Done.** With the default preset, you get a message when the node is
   more than 3 blocks behind for two checks in a row, and another when it
   is back. `node-tracker check` shows the current state at any time.

To set it up by hand, or from a script (`node-tracker setup
-non-interactive`), see [docs/manual-setup.md](docs/manual-setup.md).

## Ways to run

| How | Command | Good for |
|---|---|---|
| systemd service (system or `--user`) | `node-tracker run` | always-on servers |
| systemd timer | `node-tracker once` | intervals down to seconds, no resident process |
| cron | `node-tracker once` | intervals of one minute or more |
| by hand | `check`, `once`, `report` | setup, debugging, ad-hoc checks |

Ready-to-use units for both modes are in
[deploy/systemd/system](deploy/systemd/system) and
[deploy/systemd/user](deploy/systemd/user); a crontab example is in
[deploy/cron](deploy/cron); `node-tracker install-service` installs and
enables the systemd units for you. [docs/operations.md](docs/operations.md)
walks through each setup.

## Commands

| Command | What it does |
|---|---|
| `setup` | interactive wizard: writes, tests and installs the config (`-non-interactive` for scripts) |
| `run` | daemon: one tick every `interval` |
| `once` | one tick, then exit |
| `check` | dry run: print a status table and a verdict on the setup; touches no state, sends no alerts |
| `report` | send a status report of all targets to every notifier |
| `test-notify [name]` | send a test message through one notifier or all |
| `install-service -user\|-system` | write the systemd unit for this binary and enable it (`-timer`, `-force`, `-dry-run`) |
| `version` | version, git commit, build time, Go version |

Common flags: `-config`, `-state` (`run`/`once` only), `-log-format text|json`,
`-log-level debug|info|warn|error`.

Exit codes: `0` ok · `1` runtime failure or alerts not yet delivered · `2` bad config or usage ·
`3` another instance holds the lock · `4` a target is not in sync
(`once`, `check`, `report`).

## How it works

```text
tick:  lock state ─▶ sample node + references in parallel ─▶ classify ─▶ debounce
          │                                                                 │
          └──── save state ◀── deliver outbox (retry, backoff) ◀── queue alert on change
```

- **Tip** = the highest of all reference heights and the node's own header
  height. Taking the highest means a stale or broken reference can never hide
  a lagging node.
- **Lag** = tip − the node's validated height.
- **States:** `OK`, `LAGGING`, `NODE_DOWN`, `NO_REFERENCE`. An alert is sent
  only on a debounced change of state. The first `OK` after setup is silent.

[docs/design.md](docs/design.md) explains every decision.

## Why not …?

| Alternative | Why node-tracker |
|---|---|
| **Prometheus + Alertmanager + a bitcoind exporter** | Great if you already run that stack, but it is a lot of moving parts for one node, and the exporter only knows what the node knows. node-tracker is one binary with independent references built in. |
| **Uptime Kuma / port checks** | They tell you the RPC port answers, not that the node is in sync. |
| **A homegrown cron script** | Usually alerts on every run while the node lags, loses alerts when Telegram is down, and has the bot token in the script. node-tracker debounces, queues alerts until delivered, and keeps secrets out of the config. |

## Documentation

- [Setting up by hand, or from a script](docs/manual-setup.md)
- [Configuration reference](docs/configuration.md)
- [Telegram setup](docs/telegram.md)
- [Operations: deployment, logs, state, Tor, troubleshooting](docs/operations.md)
- [Node boxes: Umbrel, StartOS, RaspiBlitz, myNode](docs/node-boxes.md)
- [Design](docs/design.md)

## Roadmap

Ethereum nodes (`eth_blockNumber` / `eth_syncing`) and Slack and email
notifiers. The design already has extension points for these: each new kind
is one source or notifier implementation.

## Contributing

Issues and pull requests are welcome; see [CONTRIBUTING.md](CONTRIBUTING.md).
Report security problems privately, see [SECURITY.md](SECURITY.md).

## License

[GPL-3.0-only](LICENSE): GNU General Public License, version 3 only.
