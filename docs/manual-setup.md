# Setting up by hand

`node-tracker setup` does all of this for you (see the
[Quick start](../README.md#quick-start)). This page is for doing it by hand,
and for scripts.

## 1. Telegram

Create a bot and find your chat id; this takes about two minutes, see
[telegram.md](telegram.md). Save the token in a file only you can read
(`cat` waits for the token: paste it, press Enter, then Ctrl-D):

```sh
mkdir -p ~/.config/node-tracker && rm -f ~/.config/node-tracker/telegram-token && (umask 077 && cat > ~/.config/node-tracker/telegram-token)
```

## 2. Configure

Copy [examples/config.yaml](../examples/config.yaml) to
`~/.config/node-tracker/config.yaml`, then set your node's URL,
`cookie_file` and `chat_id`. For a local bitcoind, the
[minimal config](configuration.md#minimal-config) is 8 lines. For a node
elsewhere, e.g. over Tor, see [operations.md](operations.md#tor). If
this host reaches Telegram only through Tor, add
`proxy: socks5://127.0.0.1:9050` to the notifier (see
[Proxies and Tor](configuration.md#proxies-and-tor)).

## 3. Check

```sh
node-tracker check          # samples every target, prints a table, sends nothing
node-tracker test-notify    # sends a test message to Telegram
```

## 4. Run it

```sh
node-tracker run            # in the foreground
node-tracker install-service -user    # or as a service; see operations.md
```

## From a script

`node-tracker setup -non-interactive` takes the answers as flags, tests
them, and writes the same files as the wizard, e.g. for a container or a
node box's install script:

```sh
node-tracker setup -non-interactive -token-file /run/secrets/telegram-token -chat-id 123456789 -install-service
```

Without `-node-url` it watches the detected local bitcoind with its
cookie. For a node elsewhere add `-node-url`, and `-cookie-file`, or
`-rpc-user` with `-rpc-password-file`; for Tor on another port than
9050, `-proxy socks5://127.0.0.1:9150`. To reach Telegram through Tor,
`-telegram-proxy socks5://127.0.0.1:9050`. `-references` (comma-separated
Esplora API URLs, or `none`), `-preset fast|balanced|quiet`, `-name` and
`-down-for` (checks before "node unreachable"; about 5 minutes for a
`.onion` node unless set) are optional. A file that exists and differs is replaced only with
`-force`, keeping the old config (not old secrets) as `.bak`. The node and references are tested and a test
message is sent, but only a bot token that Telegram rejects stops it, as
the node or the network may not be up yet. `node-tracker setup -h` lists
the flags.
