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

`sudo node-tracker setup` writes the config and the secrets to
`/etc/node-tracker/` and runs `install-service -system` for you. By hand,
`node-tracker install-service -system` sets it up. Install the binary outside
`/home` first (the unit has `ProtectHome=yes`), and put the secrets in
`/etc/node-tracker/`:

```sh
sudo install -m 755 bin/node-tracker /usr/local/bin/
sudo install -d -m 750 /etc/node-tracker
sudo install -m 600 /dev/null /etc/node-tracker/telegram-token   # then put the token in it
cp examples/config.yaml config.yaml   # edit it; bot_token_file: /etc/node-tracker/telegram-token
sudo /usr/local/bin/node-tracker install-service -system -config config.yaml -dry-run   # check, then drop -dry-run
```

`install-service -system`:

- creates the `node-tracker` user and group if they are missing;
- copies the `-config` file to `/etc/node-tracker/config.yaml` (without
  `-config` it uses the one already there). Every `*_file` secret in the
  config must be in `/etc/node-tracker/`. The directory gets mode `750`, the
  config and the secrets `640`, all owned by `root:node-tracker`;
- writes `/etc/systemd/system/node-tracker.service` (the shipped unit, with
  its hardening, and `ExecStart=` set to the binary you ran), then runs
  `systemctl daemon-reload`, `enable --now` and `status`.

It refuses:

- a binary that is not owned by root, or that is group- or world-writable,
  or that sits in a directory like that (every directory up to `/` counts);
- a `cookie_file` under `/home`, `/root` or `/run/user` (`ProtectHome=yes`
  hides them), and a cookie that the `node-tracker` user can't read through
  its own groups or a `SupplementaryGroups=` drop-in (see below; ACLs are
  not considered). bitcoind must be running, so the cookie exists;
- a unit or config that differs from what it would write. `-force`
  replaces it and keeps the old one as `.bak`.

If a package installed the unit (`/usr/lib/systemd/system`) and its
`ExecStart=` already runs this binary, `install-service` writes no unit: it
installs the config and secrets and enables the packaged unit, so package
updates of the unit keep working. If the packaged unit runs another binary,
it stops, because a unit in `/etc/systemd/system` would shadow the packaged
one; `-force` writes it anyway. `-dry-run` prints the files and commands
and changes nothing.

By hand, the same is:

```sh
sudo useradd --system --no-create-home --shell /usr/sbin/nologin node-tracker
sudo install -d -m 750 -o root -g node-tracker /etc/node-tracker
sudo install -m 640 -o root -g node-tracker examples/config.yaml /etc/node-tracker/config.yaml
sudo install -m 640 -o root -g node-tracker /dev/null /etc/node-tracker/telegram-token   # then put the token in it
sudo cp deploy/systemd/system/node-tracker.service /etc/systemd/system/
sudo systemctl daemon-reload && sudo systemctl enable --now node-tracker
```

- In the config, point `bot_token_file` at `/etc/node-tracker/telegram-token`
  and `cookie_file` at bitcoind's cookie, e.g. `/var/lib/bitcoind/.cookie`:
  the service has no home, so the example's `~/` paths don't work for it.
  The secret files are mode `640` with group `node-tracker`, so node-tracker
  can read them and nobody else can (no permission warning).
- To read bitcoind's `.cookie`, give the service the group that owns it
  (here `bitcoin`) in a drop-in, before `install-service`:
  `sudo mkdir -p /etc/systemd/system/node-tracker.service.d && printf '[Service]\nSupplementaryGroups=bitcoin\n' | sudo tee /etc/systemd/system/node-tracker.service.d/groups.conf`
  (for `-timer`: `node-tracker-once.service.d`), or
  `sudo usermod -aG bitcoin node-tracker`. A drop-in, unlike an edit of the
  unit, does not stop `install-service` from updating the unit. The cookie
  must be readable by that group; on Bitcoin Core 28+ set
  `rpccookieperms=group` in `bitcoin.conf`.
- State is kept in `/var/lib/node-tracker/` (`StateDirectory=`).
- The unit is strongly hardened. Check its exposure score with
  `systemd-analyze security node-tracker`.

**As a timer instead** (one tick every 30 s, no resident process): add
`-timer`, which installs `node-tracker-once.service` and
`node-tracker-once.timer` and enables the timer (by hand: copy both, then
`systemctl enable --now node-tracker-once.timer`). Use the timer or the
daemon, not both. To change the interval, set `OnUnitActiveSec=` in a
drop-in (`systemctl edit node-tracker-once.timer`).

**Proxy / Tor:** set `proxy:` on the endpoint in the config (`.onion` URLs
default to Tor); the unit needs nothing. See
[configuration.md](configuration.md#proxies-and-tor).

### From a package

The package (PPA, COPR or the release `.deb`) has installed the binary,
the units and the `node-tracker` user, so only the config and the token
are left.
`sudo node-tracker setup` writes them and enables the packaged unit; by
hand:

```sh
sudo install -d -m 750 -o root -g node-tracker /etc/node-tracker
sudo install -m 640 -o root -g node-tracker /usr/share/doc/node-tracker/examples/config.yaml /etc/node-tracker/config.yaml
sudo install -m 640 -o root -g node-tracker /dev/null /etc/node-tracker/telegram-token   # then put the token in it
sudo systemctl enable --now node-tracker
```

- Set `bot_token_file` and `cookie_file` as [above](#systemd-system-service):
  absolute paths, not the example's `~/`.
- Change a packaged unit with `sudo systemctl edit node-tracker` (e.g. for
  `SupplementaryGroups=`). An upgrade overwrites the files in
  `/usr/lib/systemd`, but not your drop-ins.
- An upgrade restarts the system units that are running, not user units:
  run `systemctl --user restart node-tracker` (or the timer) for those.
- The user units are installed too: copy the example config to
  `~/.config/node-tracker/config.yaml`, then
  `systemctl --user enable --now node-tracker`.

## systemd user service

No root needed. The config and state use the XDG defaults.
`node-tracker setup` ends with `install-service -user`; by hand:

```sh
install -m 755 bin/node-tracker ~/.local/bin/
mkdir -p ~/.config/node-tracker
cp examples/config.yaml ~/.config/node-tracker/config.yaml           # edit it
~/.local/bin/node-tracker install-service -user
```

`install-service -user` checks the config, writes
`~/.config/systemd/user/node-tracker.service` with `ExecStart=` set to the
binary you ran (so it can live anywhere, e.g. `~/bin`), runs
`loginctl enable-linger` (keeps the service running when you are logged
out; if that needs privileges it tells you to run
`sudo loginctl enable-linger "$USER"`), then `systemctl --user
daemon-reload`, `enable --now` and `status`. `-config` makes the unit use
another config file. `-timer`, `-force` and `-dry-run` work
as for the system service, and so does a packaged user unit
(`/usr/lib/systemd/user`): it is enabled instead of written when it runs
this binary with the same flags.

By hand, the same is:

```sh
mkdir -p ~/.config/systemd/user
cp deploy/systemd/user/node-tracker.service ~/.config/systemd/user/   # fix ExecStart= if the binary is not in ~/.local/bin
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

## Container

Each stable release after v1.1.0 has a container image for linux/amd64,
linux/arm64 and linux/arm/v6 (any Raspberry Pi):
`ghcr.io/al-maisan/node-tracker:v1.2.3`, and `:latest` for the newest. It
holds only the binary of the signed release archive and Mozilla's CA
certificates, and runs `node-tracker run` as user 65532 with:

- the config at `/config/config.yaml`, mounted read-only;
- the state in the volume `/state`;
- secrets from files (`*_file`, e.g. `/run/secrets/telegram-token`) or
  environment variables (`*_env`).

There is no local bitcoind to detect in the container, so set `node`, with
`username` + `password_file` (a cookie file would have to be readable by
user 65532). Paths in the config are paths in the container:

```yaml
targets:
  - name: my-node
    node:
      kind: bitcoind
      url: http://127.0.0.1:8332
      username: tracker
      password_file: /run/secrets/node-rpc-password
notifiers:
  - name: telegram
    kind: telegram
    bot_token_file: /run/secrets/telegram-token
    chat_id: "123456789"
```

[deploy/compose](../deploy/compose) (also in the release archive) has a
Compose file with a read-only root, no capabilities and
`no-new-privileges`. Put it in a directory with `config.yaml` and a
`secrets/` directory with one file per secret:

```sh
mkdir -p node-tracker/secrets && cd node-tracker && cp <release archive>/deploy/compose/compose*.yaml .
(umask 077 && cat > secrets/telegram-token)   # paste the token, Enter, Ctrl-D; the same for node-rpc-password
```

User 65532 in the container must own the secrets. Which host user that
is depends on how the containers run:

| Containers run by | Command |
|---|---|
| root (docker, or `sudo podman`) | `sudo chown -R 65532:65532 secrets` |
| you, with podman (rootless) | `podman unshare chown -R 65532:65532 secrets` |
| you, with rootless docker (`docker info` shows `rootless`) | `nsenter -U --preserve-credentials -t "$(cat "$XDG_RUNTIME_DIR/docker.pid")" chown -R 65532:65532 secrets` |

Then check the setup and start it:

```sh
docker compose run --rm --entrypoint /node-tracker node-tracker check -config /config/config.yaml
docker compose up -d
```

- **bitcoind or Tor on the same host:** add
  `-f compose.yaml -f compose.host.yaml` to every `docker compose`
  command. The container then uses the host's network, so
  `http://127.0.0.1:8332` and the host's Tor at `127.0.0.1:9050` work
  (Linux only). Without it, `127.0.0.1` is the container itself: a
  `.onion` node is `NODE_DOWN` with `connect: connection refused` for
  `127.0.0.1:9050`.
- **Tor in another container** (no Tor on the host): set
  `proxy: socks5://<tor container>:9050` on the node and
  `allow_insecure: true`. A `.onion` URL with plain http is otherwise
  allowed only through a loopback proxy, because the hop to the proxy
  carries the RPC credentials in clear; here that hop stays on the
  Compose network.
- **Umbrel:** add `-f compose.yaml -f compose.umbrel.yaml` instead. The
  container joins Umbrel's app network, where bitcoind is
  `http://10.21.21.8:8332`; set `allow_insecure: true` and the user
  `umbrel` (see [Umbrel](node-boxes.md#umbrel)). Not tested on an Umbrel
  yet **(check on your box)**.
- **A node elsewhere over https or a LAN:** the base file is enough.
- **podman:** `podman compose` works the same. Without Compose:
  `podman run -d --name node-tracker --read-only --cap-drop ALL --security-opt no-new-privileges -v ./config.yaml:/config/config.yaml:ro,z -v ./secrets:/run/secrets:ro,z -v node-tracker-state:/state ghcr.io/al-maisan/node-tracker:latest`
- **Other commands:** the image runs the daemon. For another command,
  give `--entrypoint /node-tracker` and the paths, e.g. with `docker
  compose run --rm --entrypoint /node-tracker node-tracker`:
  `check -config /config/config.yaml`,
  `test-notify -config /config/config.yaml`,
  `report -config /config/config.yaml`, or
  `once -config /config/config.yaml -state /state/state.json` (without
  `-state` it fails on the read-only root).
- **Logs:** `docker compose logs -f`.
- **Upgrades:** `docker compose pull && docker compose up -d`. To pin a
  release, set `NODE_TRACKER_IMAGE=ghcr.io/al-maisan/node-tracker:v1.2.3`,
  or `…@sha256:<digest>` for the exact image.
- **Trust:** the image itself is not signed, but anyone can rebuild it
  from the signed archives and get the same digest; see
  [CONTRIBUTING.md](../CONTRIBUTING.md#releasing).

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
   `bitcoin.conf`, then restart bitcoind. `node-tracker setup` on the
   monitoring host prints these lines with a new password (it also checks
   Tor's SOCKS port and writes the config of step 5); by hand, the `rpcauth` line comes from Bitcoin Core's
   `share/rpcauth/rpcauth.py nodetracker`:

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
         down_for: 10   # NODE_DOWN only after 10 ticks (5 min at 30s): Tor stalls at times
       notify: [telegram]
   ```

   If Tor's SOCKS port is elsewhere, set `proxy: socks5://host:port` on the
   node. If that host is not loopback, e.g. Tor on another LAN machine, the
   RPC credentials cross the LAN in clear, so node-tracker also requires
   `allow_insecure: true`. `node-tracker check` shows the proxy each
   endpoint uses. The services need no environment variable or drop-in.

If Tor on the monitoring host stops, the node shows as `NODE_DOWN`: it
can't be watched then, so that is the right alert.

Onion connections stall now and then for a minute or two (a failed
circuit, `general SOCKS server failure`), so the node can flap between
`NODE_DOWN` and `OK`. `alert.down_for` waits longer before `NODE_DOWN`
than `for` does before `LAGGING`; `node-tracker setup` sets it for `.onion`
nodes to about 5 minutes. A node that answers but is behind still alerts
after `for`.

For Umbrel, StartOS, RaspiBlitz and myNode, see [Node boxes](node-boxes.md):
it shows where each box keeps the RPC credentials and its onion address.

For the opposite case, a local node reached directly and the references and
Telegram through Tor, see
[Proxies and Tor](configuration.md#proxies-and-tor).

## Several trackers

Two trackers can watch the same node, e.g. one on the node box and one on
a VPS, which also notices when the whole box is down
([Node boxes](node-boxes.md)). To tell their messages apart in a shared
chat, give each a `name` at the top of its config:

```yaml
name: shb
```

Every message then starts with it: `[shb] 🔴 orendil is lagging`.
`node-tracker setup` asks for it, with the host name as the default.

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
exits with code 1. It shows each target as one sample sees it, against
`threshold`, without the debouncing (`for`) or `recover_threshold` of the
alerts: a target that just caught up can show ✓ while its alert state is
still `LAGGING`.

## Logs

- Logs go to stderr: human-readable text by default, or `-log-format json`
  for log shippers.
- `info` (default): starts and stops, state changes, delivered alerts.
- `warn`: a source call failed, a config warning (`check` lists those in
  its verdict instead), or a lock conflict.
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

Replace the binary and restart the service. A package upgrade restarts
the running system units itself, not user units (`systemctl --user restart
node-tracker`). The
state file format is versioned. If a future version changes it, the release
notes will say how to migrate; an unknown version is refused, never
overwritten.

## Troubleshooting

| Symptom | Check |
|---|---|
| `NODE_DOWN` right after setup | `node-tracker check -log-level debug`; its ERROR column hints at the likely cause. Common causes: wrong URL or port, the cookie isn't readable by node-tracker's user (join bitcoind's group: `sudo usermod -aG <group> "$USER"`, log in again; on Bitcoin Core 28+ also set `rpccookieperms=group`), the RPC user lacks `getblockchaininfo` in `rpcwhitelist`, or the node is still starting (`rpc error -28: Loading block index`). |
| `NO_REFERENCE` | No independent reference answered: no internet, a proxy is needed, or a typo in the URL; `check` shows each error. One failing reference out of several only logs a warning, but a reference on the node's host (your own electrs) answering alone is not enough. |
| Always `LAGGING` by 1–2 blocks | Normal right after a new block. Raise `threshold` or `for`. |
| exit code 1 from `once`, log says `alerts not delivered yet` | the notifier is failing; see the error above that line, then run `node-tracker test-notify` |
| exit code 3 every run | Another node-tracker (daemon or timer) uses the same state file. Run only one. |
| `state file … is corrupt` | See [The state file](#the-state-file). |
| no Telegram message | `node-tracker test-notify` prints why, with a hint; see [telegram.md](telegram.md). |
| `plain http is only allowed for loopback and .onion hosts` | Use https, or set `allow_insecure: true` on that endpoint if the path is trusted (LAN). |
| `.onion` node is `NODE_DOWN` | Is Tor running (`systemctl status tor`)? `check` shows the proxy in use; set `proxy:` if Tor's SOCKS port is not `127.0.0.1:9050`. |
| `plain http to .onion needs a loopback proxy` | The proxy is on another host, which would see the RPC credentials. Run Tor locally, or set `allow_insecure: true` if that network is trusted. |
