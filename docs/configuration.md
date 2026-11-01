# Configuration reference

node-tracker reads one YAML file:

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
targets[0].alert.threshold: required (lag in blocks)
```

Errors show the rejected value, with a hint for common mistakes (`$VAR`,
`<placeholder>`, a URL without `http://`). Secrets are never shown: the
`user:pass@` part of a URL is printed as `REDACTED@`.

Run `node-tracker check` after each change.

A commented example is in [examples/config.yaml](../examples/config.yaml).

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
| `node` | endpoint | *required* | The node being watched. Kinds: `bitcoind`. |
| `references` | list of endpoints | none | Independent views of the chain tip. Kinds: `esplora`, `bitcoind`. |
| `reference_outlier` | integer | `6` | With 3+ responding references, a reference more than this many blocks above all the others is ignored (and logged). `0` turns it off. |
| `alert.threshold` | integer | *required* | `LAGGING` when lag **>** this many blocks. |
| `alert.recover_threshold` | integer | `threshold` | Back to `OK` when lag **≤** this. Must be ≤ `threshold`. |
| `alert.for` | integer | `2` | Ticks in a row a change must be seen before the state changes (1–100). Bad states count together: `LAGGING, NODE_DOWN` leaves `OK` just like `LAGGING, LAGGING`, and the target takes the latest state. Returning to `OK` needs this many `OK` ticks in a row. |
| `notify` | list of names | *required* | Notifiers that get this target's alerts. |

**References:** without any, lag is measured against the node's own header
height only. That catches a node that is stuck validating, but not a node
that has lost contact with the network. A warning is logged in that case.
Two independent references are a good default, three are better. The tip
is the *highest* height reported, so a reference that is down or behind
never hides a problem.

The *highest* rule has one weakness: a single broken or tampered reference
(for example plain http with `allow_insecure`) could report a height far
ahead and keep the target `LAGGING` forever. `reference_outlier` guards
against that. With **three or more** responding references, a reference
that is more than that many blocks above *every other reference* is
ignored. Honest references are within a block or two of each other, so
this never hides a real lag.

With only two references that disagree, there is no way to tell which one
is wrong. One of them may be stuck along with your node, e.g. your own
electrs. So the higher one wins, and you get an alert to look at, never a
missed lag. The node's own headers are never part of this comparison,
because an isolated node's headers are behind by definition. To get
outlier protection, configure three references.

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
rpcauth=tracker:<salt>$<hash>        # from share/rpcauth/rpcauth.py
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
example a LAN or a WireGuard tunnel. A notifier's `api_url` must use
`https://` unless its host is loopback, `.onion` included.

## `notifiers[]`

| Key | Type | Meaning |
|---|---|---|
| `name` | string | Unique; used in `targets[].notify` and `test-notify <name>`. Same rules as target names. |
| `kind` | string | `telegram` |

### `kind: telegram`

| Key | Meaning |
|---|---|
| `bot_token_file` / `bot_token_env` | The token from @BotFather. Use exactly one. |
| `chat_id` | Numeric id (negative for groups, `-100…` for supergroups and channels) or `@channelname`. Quote it in YAML. |
| `api_url` | Optional. Default `https://api.telegram.org`. Set it to use a self-hosted Bot API server. |

[telegram.md](telegram.md) shows how to get the token and the chat id.

## Proxies and Tor

An endpoint with `proxy:` sends its requests through that proxy and
ignores the proxy environment variables:

```yaml
node:
  kind: bitcoind
  url: https://node.example.com:8332
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

Endpoints without `proxy:`, and notifiers, use the standard proxy variables
`HTTPS_PROXY`, `HTTP_PROXY` and `NO_PROXY`, and `socks5://` proxies are
supported. To send references and Telegram through Tor while the local
node is reached directly:

```sh
HTTPS_PROXY=socks5://127.0.0.1:9050 NO_PROXY=127.0.0.1,localhost node-tracker run
```

With systemd, use `Environment=HTTPS_PROXY=socks5://127.0.0.1:9050` in a
drop-in (`systemctl edit node-tracker`).

To watch a node that is reachable only as a Tor onion service, see
[Tor](operations.md#tor).
