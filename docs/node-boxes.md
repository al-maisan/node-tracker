# Node boxes

Umbrel, StartOS (Start9), RaspiBlitz and myNode run Bitcoin Core for you.
node-tracker works with all of them. It needs the RPC address, a user name
and a password. This page shows where each box keeps them.

App-store packages (an Umbrel or StartOS app) are out of scope for now. The
container image ([#35](https://github.com/al-maisan/node-tracker/issues/35))
comes first.

The details below come from each project's source code or docs. Items marked
**(check on your box)** could not be confirmed there; box UIs also change
between versions.

## Where to run node-tracker

- **On the box**, over ssh. node-tracker reads the RPC on the same host.
  Install it like on any Linux host (see [Quick start](../README.md#quick-start)
  and [Operations](operations.md)); on a Raspberry Pi use the arm64 build, or
  armv7 on a 32-bit OS.
- **On another machine**, e.g. a VPS. It then also alerts when the whole box
  is down or offline, which a node-tracker on the box can't do. Reach the RPC
  through the box's Tor onion service; the monitoring host needs Tor, see
  [Tor](operations.md#tor).

Either way, use `username` + `password_file`, not `cookie_file`. Put the
password in a file only node-tracker's user can read:

```sh
mkdir -p ~/.config/node-tracker && install -m 600 /dev/null ~/.config/node-tracker/node-rpc-password && $EDITOR ~/.config/node-tracker/node-rpc-password
```

The node part of the config has one of these shapes (Umbrel values; the
sections below have each box's user name and address):

```yaml
# From another machine, over Tor. Nothing else is needed for a .onion URL.
node:
  kind: bitcoind
  url: http://abc…xyz.onion:8332
  username: umbrel
  password_file: ~/.config/node-tracker/node-rpc-password
```

```yaml
# On the box, bitcoind in a container with its own IP (Umbrel).
node:
  kind: bitcoind
  url: http://10.21.21.8:8332
  username: umbrel
  password_file: ~/.config/node-tracker/node-rpc-password
  allow_insecure: true
```

`allow_insecure: true` is needed because the URL is plain `http://` to a
host that is not loopback (see [Plain HTTP](configuration.md#plain-http)).
That is fine here: the traffic never leaves the box. For bitcoind on the
box itself, use `http://127.0.0.1:8332` and leave `allow_insecure` out.

Check the RPC before you start the service; set the two variables first,
curl asks for the password:

```sh
RPCHOST=10.21.21.8; RPCUSER=umbrel
curl -s -u "$RPCUSER" --data-binary '{"jsonrpc":"1.0","id":"t","method":"getblockchaininfo","params":[]}' -H 'Content-Type: application/json' "http://$RPCHOST:8332/" | head -c 300
```

For an onion address, use the curl check in [Tor](operations.md#tor). Then
run `node-tracker check`.

**Own read-only user:** where the box allows it, give node-tracker its own
`rpcauth` user that may call only `getblockchaininfo` (see
[`kind: bitcoind`](configuration.md#kind-bitcoind)). Keep
`rpcwhitelistdefault=0`: the box's other apps use the box's RPC user, and
without it they would be denied.

## Umbrel

- **Credentials:** in the Bitcoin Node app, click **Connect**, then **RPC
  Details**. It shows host, port (8332), user name (`umbrel`) and password,
  for Tor and for the local network. The app keeps them as
  `APP_BITCOIN_RPC_USER` and `APP_BITCOIN_RPC_PASS` in a `.env` file in its
  app directory (path: check on your box).
- **Network:** bitcoind runs in a Docker container at `10.21.21.8`. It
  accepts RPC only from Umbrel's app network and its own loopback
  (`rpcallowip`). Users report that the RPC port is not reachable from the
  LAN.
- **From another machine:** use the Tor host from **RPC Details**.
- **On the box:** ssh in and use `http://10.21.21.8:8332` with
  `allow_insecure: true`, as above. Whether the host's own connection
  passes `rpcallowip`: run the curl check **(check on your box)**.
- **Own read-only user:** add the `rpcauth`, `rpcwhitelist` and
  `rpcwhitelistdefault=0` lines under the app's settings, **Custom
  bitcoin.conf overrides** (which settings tab: check on your box).

Sources: [bitcoin/exports.sh](https://github.com/getumbrel/umbrel-apps/blob/master/bitcoin/exports.sh),
[bitcoin/docker-compose.yml](https://github.com/getumbrel/umbrel-apps/blob/master/bitcoin/docker-compose.yml),
umbrel-bitcoin's [connect.ts](https://github.com/getumbrel/umbrel-bitcoin/blob/master/apps/backend/src/modules/connect/connect.ts),
[config.ts](https://github.com/getumbrel/umbrel-bitcoin/blob/master/apps/backend/src/modules/config/config.ts),
[ConnectionDetails.tsx](https://github.com/getumbrel/umbrel-bitcoin/blob/master/apps/ui/src/components/ConnectionDetails.tsx),
[CustomConfigEditor.tsx](https://github.com/getumbrel/umbrel-bitcoin/blob/master/apps/ui/src/pages/settings/CustomConfigEditor.tsx);
LAN report: [community thread](https://community.umbrel.com/t/rpc-to-my-umbrel-btc-node/7644).

## StartOS (Start9)

Run node-tracker on another machine and use the Tor address. Running it on
StartOS itself and the LAN address were not tested.

- **StartOS 0.3.5:** the Tor address is in **Services > Bitcoin Core >
  Interfaces**; add `:8332`. User name and password are in **Services >
  Bitcoin Core > Config > RPC Settings**.
- **StartOS 0.4** (Bitcoin Core package 31.x): run the action **Generate
  RPC User Credentials**. It adds an `rpcauth` user and shows the password
  once; only a hash is kept, so put it in the password file right away.
  `rpcuser`/`rpcpassword` lines are not supported. Where the RPC Tor
  address is shown: check on your box.
- **Own read-only user:** the 0.3.5 docs say per-user RPC restrictions are
  not available. Whether `rpcwhitelist` lines can be added on 0.4: check on
  your box.

Sources: [Bitcoin integrations (0.3.5.x docs)](https://docs.start9.com/0.3.5.x/service-guides/bitcoin/bitcoin-integrations),
[bitcoin-core-startos README](https://github.com/Start9Labs/bitcoin-core-startos)
and [instructions.md](https://github.com/Start9Labs/bitcoin-core-startos/blob/31.x/instructions.md).

## RaspiBlitz

- **Credentials:** user `raspibolt`, password: your Password B. Both are in
  `bitcoin.conf`: `/mnt/hdd/app-data/bitcoin/bitcoin.conf` on v1.12,
  `/mnt/hdd/bitcoin/bitcoin.conf` on v1.11 and older. RPC listens on
  `127.0.0.1:8332` only.
- **Menu:** **CONNECT** (Connect Apps & Show Credentials) > **bitcoinRPC**
  (Connect Specter Desktop or JoinMarket). **Show how to connect** prints
  user, password, Tor host and port; **Add a Hidden Service** creates the
  onion service for port 8332. Note: both entries that add access also turn
  on bitcoind's wallet.
- **On the box:** ssh in and use `http://127.0.0.1:8332`; no
  `allow_insecure`.
- **From another machine:** add the hidden service in the menu, then use
  its Tor host.
- **Own read-only user:** add the lines to `bitcoin.conf` and run
  `sudo systemctl restart bitcoind`. Whether RaspiBlitz updates keep them:
  check on your box.

Sources (v1.12.1): [00mainMenu.sh](https://github.com/raspiblitz/raspiblitz/blob/v1.12.1/home.admin/00mainMenu.sh),
[99connectMenu.sh](https://github.com/raspiblitz/raspiblitz/blob/v1.12.1/home.admin/99connectMenu.sh),
[assets/bitcoin.conf](https://github.com/raspiblitz/raspiblitz/blob/v1.12.1/home.admin/assets/bitcoin.conf),
[tor.onion-service.sh](https://github.com/raspiblitz/raspiblitz/blob/v1.12.1/home.admin/config.scripts/tor.onion-service.sh);
v1.11 path: [assets/bitcoin.conf](https://github.com/raspiblitz/raspiblitz/blob/v1.11/home.admin/assets/bitcoin.conf).

## myNode

- **Credentials:** user `mynode`; the password is in
  `/mnt/hdd/mynode/settings/.btcrpcpw` (owner `bitcoin`, mode 600). The web
  UI's Bitcoin page shows both. On the box, copy the password into your own
  file:

  ```sh
  mkdir -p ~/.config/node-tracker && install -m 600 /dev/null ~/.config/node-tracker/node-rpc-password && sudo cat /mnt/hdd/mynode/settings/.btcrpcpw > ~/.config/node-tracker/node-rpc-password
  ```

- **Own read-only user: not possible.** myNode rewrites every `rpcauth=`
  line in `bitcoin.conf` to its own user at startup, so use `mynode`.
- **Network:** RPC listens on all interfaces and accepts loopback and
  private LAN addresses.
- **On the box:** use `http://127.0.0.1:8332`; no `allow_insecure`.
- **From the LAN:** `http://<box IP>:8332` with `allow_insecure: true`. The
  credentials cross the LAN in clear, so do this only on a trusted LAN.
- **Over Tor:** myNode's Tor config has an onion service for port 8332.
  Read its address with `sudo cat /var/lib/tor/mynode_btc/hostname`. The web
  UI shows it only on myNode Premium with Tor remote access on (Tor page:
  check on your box).

Sources: [mynode_startup.sh](https://github.com/mynodebtc/mynode/blob/master/rootfs/standard/usr/bin/mynode_startup.sh),
[mynode_gen_bitcoin_config.sh](https://github.com/mynodebtc/mynode/blob/master/rootfs/standard/usr/bin/mynode_gen_bitcoin_config.sh),
[bitcoin.conf](https://github.com/mynodebtc/mynode/blob/master/rootfs/standard/usr/share/mynode/bitcoin.conf),
[torrc.d/mynode_btc](https://github.com/mynodebtc/mynode/blob/master/rootfs/standard/etc/torrc.d/mynode_btc),
[device_info.py](https://github.com/mynodebtc/mynode/blob/master/rootfs/standard/var/pynode/device_info.py),
[bitcoin.html](https://github.com/mynodebtc/mynode/blob/master/rootfs/standard/var/www/mynode/templates/bitcoin.html).
