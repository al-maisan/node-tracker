# Operations

This page covers deployment, logs, the state file, alerts, upgrades and
troubleshooting.

## Choosing a mode

| | `run` (daemon) | `once` (timer / cron) |
|---|---|---|
| Process | always running | starts, ticks, exits |
| Interval | `interval` in the config | the timer's or cron's schedule |
| Debounce `for: 2` means | 2 × `interval` | 2 consecutive runs |
| Alert delivery deadline | `interval / 2` per tick | `3 × timeout` per run |

Both modes share the same state file format, so you can switch between them
at any time. Use only one per state file: the second one would find the file
locked and exit with code 3.

## systemd system service

```sh
sudo install -m 755 bin/node-tracker /usr/local/bin/
sudo useradd --system --no-create-home --shell /usr/sbin/nologin node-tracker
sudo install -d -m 750 -o root -g node-tracker /etc/node-tracker
sudo install -m 640 -o root -g node-tracker examples/config.yaml /etc/node-tracker/config.yaml
sudo install -m 640 -o root -g node-tracker /dev/null /etc/node-tracker/telegram-token   # then put the token in it
sudo cp deploy/systemd/system/node-tracker.service /etc/systemd/system/
sudo systemctl daemon-reload && sudo systemctl enable --now node-tracker
```

- In the config, point `bot_token_file` at `/etc/node-tracker/telegram-token`.
  The secret files are mode `640` with group `node-tracker`, so node-tracker
  can read them and nobody else can (no permission warning).
- To read bitcoind's `.cookie`, uncomment `SupplementaryGroups=bitcoin` in
  the unit (use the group that owns the cookie). The cookie must be readable
  by that group; on Bitcoin Core 28+ set `rpccookieperms=group` in
  `bitcoin.conf`.
- State is kept in `/var/lib/node-tracker/` (`StateDirectory=`).
- The unit is strongly hardened. Check its exposure score with
  `systemd-analyze security node-tracker`.

**As a timer instead** (one tick every 30 s, no resident process): install
`node-tracker-once.service` and `node-tracker-once.timer`, then
`systemctl enable --now node-tracker-once.timer`. To change the interval,
edit `OnUnitActiveSec=`.

## systemd user service

No root needed. The config and state use the XDG defaults.

```sh
install -m 755 bin/node-tracker ~/.local/bin/
mkdir -p ~/.config/node-tracker ~/.config/systemd/user
cp examples/config.yaml ~/.config/node-tracker/config.yaml           # edit it
cp deploy/systemd/user/node-tracker.service ~/.config/systemd/user/
systemctl --user daemon-reload && systemctl --user enable --now node-tracker
loginctl enable-linger "$USER"     # keep it running when you are logged out
```

Logs: `journalctl --user -u node-tracker -f`. The timer variant works the
same way, using the `*-once.*` units.

## cron

```cron
* * * * *  $HOME/.local/bin/node-tracker once 2>&1 | logger -t node-tracker
```

- cron's smallest step is one minute. For shorter intervals, use a systemd
  timer.
- If two runs overlap, the later one exits with code 3 and does nothing.
- Exit code 4 means "this run observed a target not in sync". That is
  the state being monitored, not a failure. Alerts follow the debounced
  state (`alert.for`), not single runs.

See [deploy/cron/crontab.example](../deploy/cron/crontab.example).

## Status reports

`node-tracker report` sends a summary of every target to every notifier,
whatever the state:

```text
🔗 Node sync status

orendil
Network height: 974,085 (esplora:mempool.space)
✓ bitcoind:127.0.0.1:8332: 974,085 (0 behind)

✅ All nodes in sync
At: 2026-11-01 13:05:00 UTC
```

Run it hourly or daily from a timer (`node-tracker-report.timer`) or from
cron. A regular report also proves that the alert path works. A report does
not touch the state and is not queued: if it can't be delivered, the command
exits with code 1.

## Logs

- Logs go to stderr: human-readable text by default, or `-log-format json`
  for log shippers.
- `info` (default): starts and stops, state changes, delivered alerts.
- `warn`: a source call failed, a config warning, or a lock conflict.
- `error`: an alert could not be delivered (it will be retried), or saving
  the state failed.
- `-log-level debug`: one line per sample with all heights.

Secrets never appear in logs. URLs in errors are cut down to scheme and
host.

## The state file

- Default: `~/.local/state/node-tracker/state.json`; system service:
  `/var/lib/node-tracker/state.json`.
- Holds per target: the current state, since when, and the debounce
  counter. It also holds the **outbox**: alerts not yet delivered, per
  notifier.
- Written atomically (temp file, fsync, rename), with mode `0600`.
- **A corrupt file is never reset silently**, because that could resend or
  swallow alerts. node-tracker exits with an error. Inspect the file, then
  fix or delete it. Deleting it is safe: the next run starts fresh, an `OK`
  target is set silently, and anything else is alerted.
- Removed targets and notifiers are pruned from the file automatically.
- Each notifier keeps at most 100 undelivered alerts. When that is
  exceeded, the oldest are dropped and an error is logged.

## Alert delivery

- An alert is first written to the outbox, then sent. Delivery is
  **at-least-once**: a crash between "sent" and "saved" can cause one
  duplicate alert, never a lost one.
- Each send is retried with exponential backoff and jitter, and Telegram's
  `retry_after` is honoured. Errors that retrying can't fix, such as a bad
  token or chat id, are not retried within the tick.
- Undelivered alerts are retried on every tick, in order. Each alert shows
  the time of the state change, so late alerts are recognisable.
- A message the notifier rejects outright is dropped, with an error log,
  once a later message to the same notifier gets through. It can therefore
  never block the queue. If every message fails (e.g. a revoked token),
  nothing is dropped.
- `once` exits with code **1** while any alert is undelivered, so a broken
  alert path shows up as a failed timer unit or a cron error mail. It is
  not silent.

## Upgrades

Replace the binary and restart the service. The state file format is
versioned. If a future version changes it, the release notes will say how
to migrate; an unknown version is refused, never overwritten.

## Troubleshooting

| Symptom | Check |
|---|---|
| `NODE_DOWN` right after setup | `node-tracker check -log-level debug`. Common causes: wrong URL or port, the cookie isn't readable by node-tracker's user, the RPC user lacks `getblockchaininfo` in `rpcwhitelist`, or the node is still starting (`rpc error -28: Loading block index`). |
| `NO_REFERENCE` | Every reference failed: no internet, a proxy is needed, or a typo in the URL. One failing reference out of several only logs a warning. |
| Always `LAGGING` by 1–2 blocks | Normal right after a new block. Raise `threshold` or `for`. |
| exit code 1 from `once`, log says `alerts not delivered yet` | the notifier is failing; see the error above that line, then run `node-tracker test-notify` |
| exit code 3 every run | Another node-tracker (daemon or timer) uses the same state file. Run only one. |
| `state file … is corrupt` | See [The state file](#the-state-file). |
| no Telegram message | `node-tracker test-notify`, then see [telegram.md](telegram.md). |
| `plain http is only allowed for loopback hosts` | Use https, or set `allow_insecure: true` on that endpoint if the path is trusted (LAN, Tor). |
