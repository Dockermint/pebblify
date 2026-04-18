# Telegram Integration

Configure Pebblify daemon to send Telegram notifications when jobs start, complete, or fail.

## How it works

When `notify.enable = true` and `notify.mode = "telegram"`, the daemon calls the Telegram Bot API (`https://api.telegram.org/bot<token>/sendMessage`) after each job lifecycle event. No third-party Telegram library is used — the implementation uses `net/http` and `encoding/json` from the Go standard library (per `internal/daemon/notify/telegram.go`).

Notification failure is non-fatal. The worker logs the error and continues processing the next job.

## Step 1: Create a Telegram bot

1. Open Telegram and start a chat with `@BotFather`.
2. Send `/newbot`.
3. Follow the prompts to choose a name and username for your bot.
4. BotFather responds with a bot token in the format `123456789:ABCdefGhIJKlmNoPQRstUVwxyZ`.

Keep this token secret. It grants full control of your bot.

## Step 2: Obtain your channel or chat ID

### For a private chat with the bot

1. Start a conversation with your new bot by sending it any message.
2. Open `https://api.telegram.org/bot<YOUR_TOKEN>/getUpdates` in a browser.
3. Look for the `"chat"` object in the response. The `"id"` field is your chat ID. It is a negative integer for groups and channels, a positive integer for direct chats.

```json
{
  "ok": true,
  "result": [
    {
      "message": {
        "chat": {
          "id": 123456789,
          "type": "private"
        }
      }
    }
  ]
}
```

### For a Telegram channel

1. Add your bot as an administrator of the channel.
2. Send a test message to the channel.
3. Fetch `https://api.telegram.org/bot<YOUR_TOKEN>/getUpdates`. The channel `id` is a large negative integer (e.g. `-1001234567890`).

## Step 3: Set the bot token as an environment variable

The bot token must never be stored in `config.toml`. Set it as an environment variable:

```bash
export PEBBLIFY_TELEGRAM_BOT_TOKEN="123456789:ABCdefGhIJKlmNoPQRstUVwxyZ"
```

For systemd deployments, add it to `/etc/pebblify/.env`:

```ini
PEBBLIFY_TELEGRAM_BOT_TOKEN=123456789:ABCdefGhIJKlmNoPQRstUVwxyZ
```

For Podman Quadlet deployments, add it to `~/.pebblify/.env`.

## Step 4: Configure config.toml

```toml
[notify]
enable = true
mode = "telegram"
channel_id = "123456789"
```

Set `channel_id` to the chat or channel ID obtained in Step 2. For group or channel IDs beginning with `-`, include the minus sign.

## Step 5: Start the daemon

```bash
pebblify daemon
```

The startup log confirms the notifier is active:

```
{"level":"INFO","msg":"pebblify daemon started","notify_enabled":true,...}
```

## Retry behavior

Per `internal/daemon/notify/telegram.go`:

- On HTTP 5xx from the Telegram API: one retry after 500 ms.
- On HTTP 4xx: permanent failure, no retry. The error is logged at ERROR level.
- The bot token is never included in log lines. Log entries reference only the `channel_id` and HTTP status code.

## Notification message format

Notifications are sent as HTML-formatted messages. The format is:

```
Pebblify job completed
ID: <job_id>
URL: <snapshot_url>
```

For failed jobs, an `Error:` line is appended. All fields originating from untrusted input are HTML-escaped before inclusion (per `internal/daemon/notify/notifier.go:renderMessage`).

## Validation

Missing `PEBBLIFY_TELEGRAM_BOT_TOKEN` or an empty `notify.channel_id` when `notify.enable = true` causes a fatal startup error (exit 1). The daemon does not start partially configured.

## Troubleshooting

| Symptom                              | Likely cause                                         | Fix                                                    |
| :----------------------------------- | :--------------------------------------------------- | :----------------------------------------------------- |
| No notifications received            | `notify.enable = false`                              | Set `enable = true` in `[notify]`.                    |
| 401 from Telegram on startup test    | Invalid bot token                                    | Regenerate token via BotFather (`/revoke` then `/token`). |
| 400 from Telegram — chat not found   | Wrong `channel_id` or bot not added to channel       | Verify `channel_id`; ensure bot is a channel admin.   |
| Notifications delayed or dropped     | Telegram API temporary outage (5xx)                  | One retry is automatic. Further failures are logged.  |
| Token logged in plaintext somewhere  | Custom log processor intercepting stderr             | Ensure log redaction is applied at the collector level. The daemon never logs the token. |
