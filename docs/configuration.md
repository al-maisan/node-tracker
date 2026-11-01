# Configuration reference

node-tracker reads one YAML file. `node-tracker setup` writes it for you,
with its secret files next to it (see the
[Quick start](../README.md#quick-start)); this page explains every key.
The file is read from:

1. the path given with `-config`, or
2. `$XDG_CONFIG_HOME/node-tracker/config.yaml`, which defaults to
   `~/.config/node-tracker/config.yaml`. This is the same on every OS,
   macOS included.

If there is no file, node-tracker says where it looked and how to create
one. The file is checked strictly. An unknown key, such as a typo, is an
error that names the closest known key, and every problem is reported in
one go:

```text
$ node-tracker check
node-tracker: /home/me/.config/node-tracker/config.yaml: invalid config:
line 12: unknown key "treshold" in targets[0].alert (did you mean "threshold"?)
line 2: interval "soon": must be a duration such as 30s or 5m
```

Once the YAML fits, the values are checked:

```text
$ node-tracker check
node-tracker: /home/me/.config/node-tracker/config.yaml: invalid config:
notifiers[0].chat_id "<chat id>": must be a numeric chat id or @channelname; this looks like a placeholder
notifiers[0].bot_token_env: environment variable NOPE_UNSET is not set or empty
targets[0].node.url "http://$ONION:8332": invalid character "$" in host name; YAML does not expand environment variables
targets[0].alert.recover_threshold: must not exceed threshold (3)
```

Errors show the rejected value, with a hint for common mistakes (`$VAR`,
`<placeholder>`, a URL without `http://`). Secrets are never shown: the
`user:pass@` part of a URL is printed as `REDACTED@`.

Run `node-tracker check` after each change.

A commented example is in [examples/config.yaml](../examples/config.yaml).

## Minimal config

For a bitcoind on the same machine, this is enough:

```yaml
# ~/.config/node-tracker/config.yaml
targets:
  - name: my-node
notifiers:
  - name: telegram
    kind: telegram
    bot_token_file: ~/.config/node-tracker/telegram-token
    chat_id: "123456789"
```

What a target leaves out gets a default:

- `node`: the local bitcoind, see [Auto-detection](#auto-detection).
- `references`: public Esplora APIs of the node's chain:

  | Chain | References |
  |---|---|
  | main | `https://mempool.space/api`, `https://blockstream.info/api` |
  | test (testnet3) | `https://mempool.space/testnet/api` |
  | testnet4 | `https://mempool.space/testnet4/api` |
  | signet | `https://mempool.space/signet/api` |
  | regtest | none |

  The chain is the detected one if `node` is left out, else the one whose
  default RPC port the node's URL uses (mainnet for any other port).
  `references: []` means none. These are queried over clearnet (or your
  proxy), and `run` and `check` log `using public references` at startup;
  for a Tor-only setup, set `references` yourself.
- `alert`: the balanced preset, `threshold: 3`, `recover_threshold: 1`,
  `for: 2` (see [Choosing the thresholds](#choosing-the-thresholds)).
- `notify`: every notifier.

A value you set is used as it is.

### Auto-detection

A target without `node` watches `http://127.0.0.1:<port>` with a cookie.
node-tracker reads the first readable `bitcoin.conf` of these datadirs
and finds the cookie in the first of them that has a readable one:

1. `$BITCOIN_DATADIR`
2. `~/.bitcoin`
3. `~/Library/Application Support/Bitcoin` (macOS)
4. `~/snap/bitcoin-core/common/.bitcoin`
5. `/var/lib/bitcoind`, with its config in `/etc/bitcoin/bitcoin.conf`

Without a home (`HOME` unset or `/`, as for the system service), 2–4 are
skipped. `install-service -system` checks the config the same way, with
5 only, as the service account will see it.

From `bitcoin.conf` it reads only the chain (`chain=`, `testnet=1`,
`testnet4=1`, `signet=1`, `regtest=1`), `datadir=`, `rpcport=` and
`rpccookiefile=`, in sections such as `[signet]` too. Credentials are
never read. The port is `rpcport`, else the chain's default: 8332 (main),
18332 (test), 48332 (testnet4), 38332 (signet), 18443 (regtest). The cookie
is in the chain's subdirectory, e.g. `~/.bitcoin/signet/.cookie`. If no
cookie is readable, the most likely path is used, and `check` shows why
it fails (missing, or permission denied) and how to fix it.

`run` and `check` log the result at start (`once`, `report` and
`test-notify` only at `-log-level debug`):

```text
level=INFO msg="detected local bitcoind" conf=/home/me/.bitcoin/bitcoin.conf chain=signet url=http://127.0.0.1:38332 cookie_file=/home/me/.bitcoin/signet/.cookie
```

For a node elsewhere, or another port or auth, set `node`.

A reference on another chain than the node (from `getblockchaininfo`)
counts as failed, and `check` warns about it. node-tracker knows the chain
of mempool.space and blockstream.info and of URLs with `/testnet`,
`/testnet4` or `/signet` in the path.

## Paths and secrets

- Every `*_file` path, `cookie_file` and `state_file` must be absolute or
  start with `~/`. Relative paths are rejected because cron, systemd and
  a shell each start in a different directory.
- **Secrets are never written in the config file.** Each secret comes from
  exactly one of:
  - `<name>_file`: a file holding only the secret. Surrounding whitespace is
    trimmed. If others (not owner, not group) can access the file, a warning
    is logged. Fix it with `chmod 600`, or with `chmod 640` plus a dedicated
    group.
  - `<name>_env`: the name of an environment variable.

  This way the config file can be shared, committed or pasted in a bug
  report.

## Top level

| Key | Type | Default | Meaning |
|---|---|---|---|
| `interval` | duration | `30s` | Time between ticks in `run` mode. Minimum `1s`. Ignored by `once`. |
| `timeout` | duration | `10s` | Timeout of each single call to a node, reference or notifier. Must be > 0 and ≤ `interval`. `.onion` endpoints get at least `30s` (see [Tor](#proxies-and-tor)). |
| `state_file` | path | `$XDG_STATE_HOME/node-tracker/state.json` (else `~/.local/state/…`) | Where states and undelivered alerts are kept. `-state` overrides it. |
| `targets` | list | *required* | Nodes to watch, at least one. |
| `notifiers` | list | | Alert channels. |

Durations use Go syntax: `90s`, `5m`, `1h30m`.

## `targets[]`

| Key | Type | Default | Meaning |
|---|---|---|---|
| `name` | string | *required* | Unique. 1–64 of `A–Z a–z 0–9 . _ -`. Shown in alerts. |
| `node` | endpoint | the [local bitcoind](#auto-detection) | The node being watched. Kinds: `bitcoind`. |
| `references` | list of endpoints | [by chain](#minimal-config) | Independent views of the chain tip. Kinds: `esplora`, `bitcoind`. `[]` for none. |
| `reference_outlier` | integer | `6` | With 3+ responding references, a reference more than this many blocks above all the others is ignored (and logged). `0` turns it off. |
| `alert.threshold` | integer | `3` | `LAGGING` when lag **>** this many blocks. |
| `alert.recover_threshold` | integer | `threshold`; `1` if `threshold` is left out too | Back to `OK` when lag **≤** this. Must be ≤ `threshold`. |
| `alert.for` | integer | `2` | Ticks in a row a change must be seen before the state changes (1–100). Bad states count together: `LAGGING, NODE_DOWN` leaves `OK` just like `LAGGING, LAGGING`, and the target takes the latest state. Returning to `OK` needs this many `OK` ticks in a row. |
| `notify` | list of names | every notifier | Notifiers that get this target's alerts. |

**References:** without any (`references: []`, or on regtest), lag is measured against the node's own header
height only. That catches a node that is stuck validating, but not a node
that has lost contact with the network. A warning is logged in that case.
Two independent references are a good default, three are better. The tip
is the *highest* height reported, so a reference that is down or behind
never hides a problem, as long as an independent one answers.

An Esplora reference on the node's host (the same host name, or both on
loopback), such as your own electrs, is fed by the node and stalls with
it. It still counts for the tip, but if the target also has independent
references and none of them answers, the state is `NO_REFERENCE`, not
`OK`. A config whose only references are such local ones gets a warning.

The *highest* rule has one weakness: a single broken or tampered reference
(for example plain http with `allow_insecure`) could report a height far
ahead and keep the target `LAGGING` forever. `reference_outlier` guards
against that. With **three or more** responding independent references,
one that is more than that many blocks above *every other one* is ignored.
References on the node's host don't vote here. Honest references are within a block or two of each other, so
this never hides a real lag.

With only two references that disagree, there is no way to tell which one
is wrong. One of them may be stuck along with your node, e.g. your own
electrs. So the higher one wins, and you get an alert to look at, never a
missed lag. The node's own headers are never part of this comparison,
because an isolated node's headers are behind by definition. To get
outlier protection, configure three references. They should not share a
backend: two references that are stuck at the same height outvote a
correct one above them.

### Choosing the thresholds

Bitcoin blocks arrive about every 10 minutes, at random. Right after a block,
a reference often knows it a few seconds before your node does. Suggested
values:

| | `threshold` | `recover_threshold` | `for` | with `interval` |
|---|---|---|---|---|
| alert fast | 2 | 0 | 2 | 30s |
| balanced (default) | 3 | 1 | 2 | 30s |
| quiet | 6 | 2 | 3 | 60s |

The time until an alert is about `for × interval`, plus the time the node
needs to fall `threshold` blocks behind.

## Endpoints

### `kind: bitcoind`

Bitcoin Core JSON-RPC. Only `getblockchaininfo` is called.

| Key | Meaning |
|---|---|
| `url` | e.g. `http://127.0.0.1:8332`. Must not contain credentials. |
| `cookie_file` | Bitcoin Core's `.cookie` (e.g. `~/.bitcoin/.cookie`, `/var/lib/bitcoind/.cookie`, on macOS `~/Library/Application Support/Bitcoin/.cookie`). It is re-read on every call, so a node restart, which writes a new cookie, needs no action. If it does not exist yet, a warning is logged, not an error. |
| `username` + `password_file` / `password_env` | Use these instead of the cookie, with `rpcauth` credentials. |
| `allow_insecure` | Allow plain `http://` to a non-loopback host (see below). |
| `proxy` | Optional proxy for this endpoint only (see [Proxies and Tor](#proxies-and-tor)). |

Set exactly one of `cookie_file` or `username`.

**Least privilege:** give node-tracker its own RPC user that may call only
one method. In `bitcoin.conf`:

```ini
rpcauth=tracker:<salt>$<hash>        # from node-tracker setup, or share/rpcauth/rpcauth.py
rpcwhitelist=tracker:getblockchaininfo
rpcwhitelistdefault=0                # else users without a whitelist are denied
```

### `kind: esplora`

Esplora REST API: `GET {url}/blocks/tip/height`.

| Key | Meaning |
|---|---|
| `url` | API root, e.g. `https://mempool.space/api`, `https://blockstream.info/api`, or your own esplora/mempool instance. |
| `allow_insecure` | Allow plain `http://` to a non-loopback host. |
| `proxy` | Optional proxy for this endpoint only. |

### Plain HTTP

`http://` is accepted without extra settings only for `localhost`,
`127.0.0.0/8`, `::1` and, for nodes and references, `.onion` hosts reached
through a loopback proxy (Tor encrypts the traffic itself). Any other `http://` endpoint URL needs
`allow_insecure: true`. Set it only when the network path is trusted, for
example a LAN or a WireGuard tunnel. A `proxy` set on an `http://`
endpoint must be loopback too, as it sees the traffic, unless
`allow_insecure: true`. A notifier's `api_url` must use
`https://` unless its host is loopback, `.onion` included.

## `notifiers[]`

| Key | Type | Meaning |
|---|---|---|
| `name` | string | Unique; used in `targets[].notify` and `test-notify <name>`. Same rules as target names. |
| `kind` | string | `telegram` |
| `proxy` | string | Optional proxy for this notifier only, e.g. `socks5://127.0.0.1:9050` (see [Proxies and Tor](#proxies-and-tor)). With a plain-http `api_url`, it must be a loopback proxy, as it would see the bot token. |

### `kind: telegram`

| Key | Meaning |
|---|---|
| `bot_token_file` / `bot_token_env` | The token from @BotFather. Use exactly one. |
| `chat_id` | Numeric id (negative for groups, `-100…` for supergroups and channels) or `@channelname`. Quote it in YAML. |
| `api_url` | Optional. Default `https://api.telegram.org`. Set it to use a self-hosted Bot API server. |

[telegram.md](telegram.md) shows how to get the token and the chat id.

## Proxies and Tor

An endpoint or a notifier with `proxy:` sends its requests through that
proxy and ignores the proxy environment variables:

```yaml
node:
  kind: bitcoind
  url: https://node.example.com:8332
  proxy: socks5://127.0.0.1:9050
```

```yaml
notifiers:
  - name: telegram
    kind: telegram
    bot_token_file: ~/.config/node-tracker/telegram-token
    chat_id: "123456789"
    proxy: socks5://127.0.0.1:9050
```

- Schemes: `socks5`, `socks5h`, `http`, `https`. Both SOCKS schemes send
  the host name to the proxy, so nothing is resolved locally.
- Only `scheme://host:port`. Proxy credentials are not supported, so a
  proxy URL never holds a secret.
- `node-tracker check` prints the proxy of every endpoint if any endpoint
  uses one (always with `-log-level debug`).

**`.onion` hosts** need nothing but the URL and the auth fields:

```yaml
node:
  kind: bitcoind
  url: http://<56 characters>.onion:8332
  username: tracker
  password_file: ~/.config/node-tracker/rpc-password
```

- Without `proxy:`, the proxy from the environment for that URL is used
  (`HTTP_PROXY` for `http://`, honouring `NO_PROXY`), as before per-endpoint
  proxies existed. If there is none, it is `socks5://127.0.0.1:9050`, Tor's
  SOCKS port. Set `proxy:` to use another one, e.g. Tor Browser's
  `socks5://127.0.0.1:9150`. `check` shows which one is used: `(default)`,
  `(environment)`, or the `proxy:` value.
- Plain `http://` is allowed without `allow_insecure` only if that proxy is
  on a loopback address (`127.0.0.0/8`, `::1`, `localhost`). From there on
  Tor encrypts, but the hop to a proxy elsewhere, e.g. Tor on another LAN
  host, carries the RPC credentials in clear. That needs
  `allow_insecure: true`.
- Each call gets `max(timeout, 30s)`, but at most `interval`, as Tor round
  trips often take several seconds.
- The address must be a valid v3 onion address (56 characters, with a
  checksum), so a typo is reported by `check` instead of a Tor timeout.

Endpoints and notifiers without `proxy:` use the standard proxy variables
`HTTPS_PROXY`, `HTTP_PROXY` and `NO_PROXY`, and `socks5://` proxies are
supported. On a host that reaches the internet only through Tor, set
`proxy: socks5://127.0.0.1:9050` on the notifier and on each reference.
`node-tracker setup` writes the notifier's when you pick
**[t]ry through Tor** (or give `-telegram-proxy`). The service then needs
no environment variables. The variables still work, e.g. for a run in a
terminal:

```sh
HTTPS_PROXY=socks5://127.0.0.1:9050 NO_PROXY=127.0.0.1,localhost node-tracker run
```

To watch a node that is reachable only as a Tor onion service, see
[Tor](operations.md#tor).
