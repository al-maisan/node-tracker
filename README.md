<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="assets/logo-dark.svg">
    <img src="assets/logo.svg" alt="node-tracker logo: an NT shield above a chain of blocks, the last one hollow (a node behind the tip), and the name NODETRACKER following the shield outline" width="200">
  </picture>
</p>

# node-tracker

**Get a Telegram message when your Bitcoin node falls behind the chain tip, and another when it catches up.**

A node that has lost its peers, is eclipsed, or hangs after a crash often
*looks* healthy: the process runs and the RPC port answers. node-tracker
compares its height with independent views of the chain (mempool.space,
blockstream.info or your own nodes) and alerts when the gap crosses a
threshold, in either direction.

![node-tracker demo: check, test-notify, then two once runs raising a LAGGING alert](docs/demo/demo.gif)

The demo runs against local fakes ([docs/demo](docs/demo/)). In Telegram,
the alerts look like this:

![Telegram: the node-tracker test message, then "orendil is lagging" with node height 974,079, network height 974,085 and a lag of 6 blocks](docs/demo/telegram-alert.png)

## Features

- **Catches the silent failures:** an isolated node (against independent
  references) as well as one stuck validating (against its own headers).
- **Alerts on change, not on every check:** debounced, with a separate
  recovery threshold, so no flapping. Unreachable nodes and failing
  references are alerted too.
- **Never loses an alert:** alerts wait in the state file until delivered.
- **Runs your way:** daemon, systemd timer, cron or by hand, all with the
  same behavior; optional hourly reports prove the alerting works. Several
  trackers can share a chat: `name:` puts each one's name in front of its
  alerts.
- **Secure by default:** secrets only from files or the environment, only
  the read-only `getblockchaininfo` RPC, plain http only to localhost or
  `.onion` through Tor.
- **One small static binary:** no runtime, no database.

## Install

| System | Command |
|---|---|
| Ubuntu 26.04 | `sudo add-apt-repository ppa:al-maisan/node-tracker && sudo apt install node-tracker` |
| Fedora | `sudo dnf copr enable al-maisan/node-tracker && sudo dnf install node-tracker` |
| Debian, Raspberry Pi OS | the `.deb` from the [releases](https://github.com/al-maisan/node-tracker/releases): `sudo apt install ./node-tracker_1.3.2-1_arm64.deb` |
| macOS, Linux | `brew install al-maisan/tap/node-tracker` |
| Container | `ghcr.io/al-maisan/node-tracker`, with [deploy/compose](deploy/compose) |
| Go 1.26+ | `go install github.com/al-maisan/node-tracker/cmd/node-tracker@latest` |

Release archives for any Linux or macOS, how to verify a download, and the
details of each way: [docs/install.md](docs/install.md).

## Quick start

1. **Install** node-tracker, see [Install](#install).
2. **Run the setup wizard:**

   ```sh
   node-tracker setup          # or: sudo node-tracker setup, for the system service
   ```

   Run it as the user bitcoind runs as; `sudo` only if bitcoind is a
   system service too, see
   [System or user service](docs/operations.md#system-or-user-service).

   It finds your local bitcoind or asks for your node (`.onion` included),
   takes the token of a new or existing Telegram bot (@BotFather), and finds
   your chat when you tap a link (or takes a channel or a chat id). Every
   answer is tested, including a test message; then it writes the config
   and offers to start the service.
3. **Done.** By default you get a message when the node is more than 3
   blocks behind for two checks in a row, and another when it is back.
   `node-tracker check` shows the current state at any time.

By hand or from a script: [docs/manual-setup.md](docs/manual-setup.md).

## Commands

| Command | What it does |
|---|---|
| `setup` | interactive wizard: writes, tests and installs the config |
| `run` | daemon: one check every `interval` |
| `once` | one check, then exit (for cron or a systemd timer) |
| `check` | dry run: status table and a verdict on the setup; changes nothing |
| `report` | send a status report of every target |
| `test-notify [name]` | send a test message |
| `install-service -user\|-system` | write and start the systemd unit |

Flags, exit codes and examples: `man node-tracker` or `node-tracker <command> -h`.

## How it works

Each check reads the node and the references in parallel. The tip is the
*highest* height seen (references and the node's own headers), so a stale
reference can never hide a lagging node; the lag is the tip minus the
node's height. A state change (`OK`, `LAGGING`, `NODE_DOWN`,
`NO_REFERENCE`) must hold for a few checks before it is alerted. Details:
[docs/design.md](docs/design.md).

## Why not …?

- **Prometheus + Alertmanager + an exporter:** fine if you run that stack,
  but many parts for one node, and the exporter only knows what the node
  knows.
- **Uptime Kuma, port checks:** they tell you the RPC answers, not that the
  node is in sync.
- **A cron script:** usually alerts on every run while the node lags, loses
  alerts when Telegram is down, and keeps the bot token in the script.

## Documentation

- [Install](docs/install.md)
- [Setting up by hand, or from a script](docs/manual-setup.md)
- [Configuration reference](docs/configuration.md)
- [Telegram](docs/telegram.md)
- [Operations: services, containers, logs, state, Tor, troubleshooting](docs/operations.md)
- [Node boxes: Umbrel, StartOS, RaspiBlitz, myNode](docs/node-boxes.md)
- [Design](docs/design.md)

## Contributing

Issues and pull requests are welcome, see [CONTRIBUTING.md](CONTRIBUTING.md);
security problems privately, see [SECURITY.md](SECURITY.md). Planned:
Ethereum nodes, Slack and email notifiers.

## License

[GPL-3.0-only](LICENSE)
