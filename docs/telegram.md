# Telegram setup

You need two things: a **bot token**, which lets node-tracker send messages,
and a **chat id**, which says where the messages go.

## 1. Create a bot

1. In Telegram, open a chat with **@BotFather** and send `/newbot`.
2. Choose a display name (e.g. `My node alerts`) and a username ending in
   `bot` (e.g. `my_node_alerts_bot`).
3. BotFather replies with a token like `123456789:AAH…`. Treat it like a
   password: anyone with it can send messages as your bot.

Save the token in a file only you can read:

```sh
mkdir -p ~/.config/node-tracker && install -m 600 /dev/null ~/.config/node-tracker/telegram-token && $EDITOR ~/.config/node-tracker/telegram-token
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

If the message doesn't arrive, the error log says why:

| Error | Cause |
|---|---|
| `HTTP 401: Unauthorized` | wrong token |
| `HTTP 400: Bad Request: chat not found` | wrong chat id, or you never messaged the bot |
| `HTTP 403: Forbidden: bot was blocked by the user` | unblock the bot in Telegram |
| `HTTP 429` | rate limited; node-tracker waits and retries by itself |

The token never shows up in logs or errors.
