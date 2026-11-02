# Telegram setup

You need two things: a **bot token**, which lets node-tracker send messages,
and a **chat id**, which says where the messages go.

`node-tracker setup` does most of this for you:

1. **A rerun** offers to keep the bot and chat of the existing config; yes
   sends a test message, and the Telegram step is done.
2. **The bot:** you create one (step 1 below), or for a bot you have, send
   `/mybots` in the chat with @BotFather (not with your bot), pick the bot
   and tap **API Token**. Paste the
   token; setup checks it with Telegram's `getMe` and shows the bot's name.
3. **Where the alerts go:**
   - **1 to you** or **2 a group:** setup shows a link like
     `https://t.me/my_node_alerts_bot?start=<code>` (`?startgroup=` for a
     group). Open it in Telegram on any device (e.g. Ctrl+click it, or send
     it to yourself in Saved Messages and tap it on your phone) and press
     Start; for a group, pick the group. This works in a chat with the bot
     that already exists, too. If nothing happens, send the message setup
     shows: `/start <code>`, or `/start@my_node_alerts_bot <code>` in a
     group. Setup sees it by itself; there is nothing to type. The code is new for each run, so
     old messages and messages from others are ignored.
   - **3 a channel:** type its `@name`; the bot must be one of its admins.
   - **4 a chat id you already know:** type it.

   The bot's own `@name` is refused: a bot can't message itself.
4. Last, it sends a test message.

If the bot has a webhook, Telegram does not hand out its messages
(HTTP 409): remove the webhook, or pick 4 and type the chat id.

If Telegram does not answer (blocked network, or a host that reaches the
internet only through Tor), setup offers **[r]etry**, **[k]eep the token
untested** (then type the chat id; send a test later with
`node-tracker test-notify`), or **[t]ry through Tor**
(`socks5://127.0.0.1:9050`). With Tor, setup writes
`proxy: socks5://127.0.0.1:9050` to the notifier, so the service uses it
too. In a config written by hand, add the same `proxy:` line to the
notifier (see [Proxies and Tor](configuration.md#proxies-and-tor)).

The rest of this page is the manual way.

## 1. Create a bot

1. In Telegram, open a chat with **@BotFather** and send `/newbot`.
2. Choose a display name (e.g. `My node alerts`) and a username ending in
   `bot` (e.g. `my_node_alerts_bot`).
3. BotFather replies with a token like `123456789:AAH…` (for a bot you
   have: `/mybots` to @BotFather, pick the bot, **API Token**). Treat it like a
   password: anyone with it can send messages as your bot.

Save the token in a file only you can read (`cat` waits for the token:
paste it, press Enter, then Ctrl-D):

```sh
mkdir -p ~/.config/node-tracker && rm -f ~/.config/node-tracker/telegram-token && (umask 077 && cat > ~/.config/node-tracker/telegram-token)
```

## 2. Find the chat id

A bot can't message you until you have messaged it.

**Direct messages:** open your bot in Telegram, press **Start** (or send any
message), then run:

```sh
curl -s "https://api.telegram.org/bot$(cat ~/.config/node-tracker/telegram-token)/getUpdates" | grep -o '"chat":{"id":-\?[0-9]*'
```

The number is your chat id, e.g. `"chat":{"id":123456789` → `chat_id: "123456789"`.

**A group:** add the bot to the group, send a message in the group, and run
the same command. Group ids are negative; supergroup ids start with `-100`.

**A channel:** add the bot to the channel as an administrator with "Post
messages" permission. You can then use `chat_id: "@yourchannel"` for a
public channel, or the numeric `-100…` id.

If `getUpdates` returns `"result":[]`, send the bot another message and try
again. Pending updates expire after 24 hours.

## 3. Configure and test

```yaml
notifiers:
  - name: telegram
    kind: telegram
    bot_token_file: ~/.config/node-tracker/telegram-token
    chat_id: "123456789"
```

```sh
node-tracker test-notify telegram
```

If the message doesn't arrive, `test-notify` prints why, after the
notifier's name, with a hint at the cause:

```
node-tracker: test message failed: telegram: HTTP 400: Bad Request: chat not found (wrong chat id, or the bot was never started in that chat)
```

| Error | Cause |
|---|---|
| `HTTP 401: Unauthorized` | wrong token |
| `HTTP 400: Bad Request: chat not found` | wrong chat id, or you never messaged the bot |
| `HTTP 403: Forbidden: bot was blocked by the user` | unblock the bot in Telegram |
| `HTTP 429` | rate limited; node-tracker waits and retries by itself |

The token never shows up in logs or errors.
