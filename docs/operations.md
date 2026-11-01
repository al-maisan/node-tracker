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

### From a package

The deb or rpm package has installed the binary, the units and the
`node-tracker` user, so only the config and the token are left:

```sh
sudo install -d -m 750 -o root -g node-tracker /etc/node-tracker
sudo install -m 640 -o root -g node-tracker /usr/share/doc/node-tracker/examples/config.yaml /etc/node-tracker/config.yaml
sudo install -m 640 -o root -g node-tracker /dev/null /etc/node-tracker/telegram-token   # then put the token in it
sudo systemctl enable --now node-tracker
```

- Change a packaged unit with `sudo systemctl edit node-tracker` (e.g. for
  `SupplementaryGroups=`). An upgrade overwrites the files in
  `/usr/lib/systemd`, but not your drop-ins.
- An upgrade does not restart the service: run
  `sudo systemctl restart node-tracker` afterwards.
- The user units are installed too: copy the example config to
  `~/.config/node-tracker/config.yaml`, then
  `systemctl --user enable --now node-tracker`.

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

## Tor

node-tracker can watch a node whose RPC is reachable only as a Tor onion
service, e.g. a home node watched from a VPS. It needs a local Tor SOCKS
proxy, an onion service for the RPC port on the node, and an `rpcauth` user
(the cookie is only readable on the node's host).

1. **On the node**, create the onion service. Install Tor (Debian/Ubuntu:
   `sudo apt install -y tor`) and add two lines to its config. If
   `/etc/tor/torrc` has a `%include /etc/tor/torrc.d/*.conf` line (Fedora
   has it), write them to `/etc/tor/torrc.d/bitcoin-rpc.conf` (replace
   `-a /etc/tor/torrc` below with that path); otherwise append them to
   `/etc/tor/torrc`:

   ```sh
   printf 'HiddenServiceDir /var/lib/tor/bitcoin-rpc/\nHiddenServicePort 8332 127.0.0.1:8332\n' | sudo tee -a /etc/tor/torrc
   ```

   Restart Tor and read the onion address:

   ```sh
   sudo systemctl restart tor && sudo cat /var/lib/tor/bitcoin-rpc/hostname
   ```

   Tor connects to bitcoind at `127.0.0.1:8332`. With the default
   `rpcbind` (localhost only) nothing else is needed: localhost is always
   allowed, whatever `rpcallowip` says. If you set `rpcbind` to another
   address, also add `rpcbind=127.0.0.1`. Keep the RPC off the clearnet: no
   `rpcbind=0.0.0.0` on a public host and no router port forward for 8332.

   Treat the onion address like a semi-secret: anyone who has it can reach
   the RPC port and try to log in. The `rpcauth` password and the
   `rpcwhitelist` below protect it.

2. **On the node**, give node-tracker its own read-only RPC user in
   `bitcoin.conf` (`rpcauth` line from Bitcoin Core's
   `share/rpcauth/rpcauth.py nodetracker`), then restart bitcoind:

   ```ini
   rpcauth=nodetracker:<salt>$<hash>
   rpcwhitelist=nodetracker:getblockchaininfo
   rpcwhitelistdefault=0
   ```

   `rpcwhitelistdefault=0` matters: once any `rpcwhitelist` is set, Bitcoin
   Core otherwise denies every user without one (the cookie, `bitcoin-cli`,
   electrs, LND, …).

3. **On the monitoring host**, install Tor and check that its SOCKS port
   works (Debian/Ubuntu):

   ```sh
   sudo apt install -y tor && sudo systemctl enable --now tor && curl -s --socks5-hostname 127.0.0.1:9050 https://check.torproject.org/api/ip
   ```

   It must print `"IsTor":true`. Then check the onion RPC itself; set the
   two variables first, curl asks for the password:

   ```sh
   ONION=abc…xyz.onion; RPCUSER=nodetracker
   curl -s --socks5-hostname 127.0.0.1:9050 -u "$RPCUSER" --data-binary '{"jsonrpc":"1.0","id":"t","method":"getblockchaininfo","params":[]}' -H 'Content-Type: application/json' "http://$ONION:8332/" | head -c 300
   ```

4. **Optional, stronger:** lock the onion service to this host with Tor
   client authorization. Then a leaked onion address is useless without
   the key: no one else can even reach the RPC port. node-tracker needs no
   change; Tor on the monitoring host handles it. On the monitoring host,
   make a key pair:

   ```sh
   umask 077 && openssl genpkey -algorithm x25519 -out tor-auth.pem && echo "public: $(openssl pkey -in tor-auth.pem -pubout -outform DER | tail -c 32 | base32 | tr -d =)" && echo "private: $(openssl pkey -in tor-auth.pem -outform DER | tail -c 32 | base32 | tr -d =)"
   ```

   On the node, authorize the public key (set `PUB` to the printed
   value), then restart Tor:

   ```sh
   PUB=ABCD…WXYZ; sudo mkdir -p /var/lib/tor/bitcoin-rpc/authorized_clients && echo "descriptor:x25519:$PUB" | sudo tee /var/lib/tor/bitcoin-rpc/authorized_clients/monitor.auth >/dev/null && sudo chown -R --reference=/var/lib/tor/bitcoin-rpc /var/lib/tor/bitcoin-rpc/authorized_clients && sudo systemctl restart tor
   ```

   On the monitoring host, add `ClientOnionAuthDir /var/lib/tor/onion-auth`
   to its torrc (as in step 1) and store the private key there (set `PRIV`
   and `ONION`); this restarts Tor and deletes `tor-auth.pem`:

   ```sh
   ONION=abc…xyz.onion; PRIV=EFGH…STUV; sudo install -d -m 700 -o "$(stat -c %U /var/lib/tor)" /var/lib/tor/onion-auth && echo "${ONION%.onion}:descriptor:x25519:$PRIV" | sudo tee /var/lib/tor/onion-auth/node.auth_private >/dev/null && sudo chown --reference=/var/lib/tor/onion-auth /var/lib/tor/onion-auth/node.auth_private && sudo chmod 600 /var/lib/tor/onion-auth/node.auth_private && sudo systemctl restart tor && rm tor-auth.pem
   ```

   The curl check from step 3 must still work; from any other host the
   onion address no longer answers.

5. **Config:** write the onion address out in full (YAML does not expand
   `$ONION`) and use `username` + `password_file` instead of `cookie_file`.
   Nothing else is needed: for a `.onion` URL, node-tracker goes through
   Tor's SOCKS port `127.0.0.1:9050` (or `HTTP_PROXY`, if set), allows plain
   http (Tor encrypts the traffic itself) and gives each call at least 30s
   (at most `interval`).
   The references and Telegram stay direct.

   ```yaml
   targets:
     - name: my-node
       node:
         kind: bitcoind
         url: http://abc…xyz.onion:8332
         username: nodetracker
         password_file: ~/.config/node-tracker/node-rpc-password   # chmod 600
       references:
         - kind: esplora
           url: https://mempool.space/api
         - kind: esplora
           url: https://blockstream.info/api
       alert:
         threshold: 3
       notify: [telegram]
   ```

   If Tor's SOCKS port is elsewhere, set `proxy: socks5://host:port` on the
   node. If that host is not loopback, e.g. Tor on another LAN machine, the
   RPC credentials cross the LAN in clear, so node-tracker also requires
   `allow_insecure: true`. `node-tracker check` shows the proxy each
   endpoint uses. The services need no environment variable or drop-in.

If Tor on the monitoring host stops, the node shows as `NODE_DOWN`: it
can't be watched then, so that is the right alert.

For the opposite case, a local node reached directly and the references and
Telegram through Tor, see
[Proxies and Tor](configuration.md#proxies-and-tor).

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

Replace the binary (or upgrade the package) and restart the service. The
state file format is versioned. If a future version changes it, the release
notes will say how to migrate; an unknown version is refused, never
overwritten.

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
| `plain http is only allowed for loopback and .onion hosts` | Use https, or set `allow_insecure: true` on that endpoint if the path is trusted (LAN). |
| `.onion` node is `NODE_DOWN` | Is Tor running (`systemctl status tor`)? `check` shows the proxy in use; set `proxy:` if Tor's SOCKS port is not `127.0.0.1:9050`. |
| `plain http to .onion needs a loopback proxy` | The proxy is on another host, which would see the RPC credentials. Run Tor locally, or set `allow_insecure: true` if that network is trusted. |
