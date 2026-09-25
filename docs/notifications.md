# Up - Notifications

> Notifications are delivered by the
> [shoutrrr](https://github.com/nicholas-fedor/shoutrrr) engine. The UI exposes
> five channels - **SMTP**, **Webhook**, **Slack**, **Discord** and
> **Telegram** - and each monitor can be linked to any number of them.

## 1. When a notification is sent

The trigger is a **status transition of the aggregated status**, never the raw
result of a single check:

```mermaid
flowchart LR
  A[heartbeat stored] --> B[aggregate the votes of the online nodes]
  B --> C{"state changed?\n(monitor_states)"}
  C -->|no| D{"resend interval\nelapsed and still down?"}
  C -->|yes| E[elect the sender node]
  D -->|yes| E
  D -->|no| F[do nothing]
  E --> G[deliver to every linked channel]
  G --> H["notification_logs(row per channel)"]
```

- `unknown -> down` (the very first check of a new monitor) **does not** notify:
  the first verdict only initialises `monitor_states`. This avoids a wave of
  alerts when a monitor is created.
- `up -> down`, `down -> up` and `degraded` transitions notify.
- Retries mark the heartbeat `important=false` and notifications are only
  evaluated after the retry budget is exhausted.
- `resend_interval_seconds` (monitor level and channel level, the larger value
  wins) repeats an alert while the monitor stays `down`/`degraded`.

### Certificate and domain events

`cert_expiring`, `cert_expired`, `domain_expiring` and `domain_expired` do not
come from a status transition: they come from the TLS certificate a probe read
(see `docs/monitors.md` §9) or from the registry expiration of the monitor's
domain (see `docs/monitors.md` §10). They are delivered through the same channels
and the same `on_down` link flag, and the transport is deduplicated per day and
per threshold, so several cluster nodes watching the same certificate or domain
produce a single message.

## 2. Channels

### SMTP (e-mail)

| Field | Notes |
|---|---|
| `host`, `port` | e.g. `smtp.example.com:587` |
| `username`, `password` | optional (relay without auth) |
| `from` | envelope sender |
| `to` | one or more addresses, comma separated |
| `secure` | `true` = implicit TLS from the first byte (port 465); `false` = STARTTLS when offered, plain otherwise |
| `use_html` | sends a small HTML table instead of plain text |
| `skip_tls_verify` | accepts self-signed certificates (internal relays) |
| `subject_prefix` | prepended to the subject, default `[Up]` |

Under the hood the settings are translated into a shoutrrr URL
(`internal/notify/urls.go`):

```
smtp://user:pass@host:port/?fromaddress=up@example.com&toaddresses=a@x,b@y&subject=[Up]%20...&usehtml=yes&encryption=Auto&usestarttls=yes
```

### Webhook

| Field | Notes |
|---|---|
| `url` | `http://` or `https://` (plain HTTP sets `disabletls=yes` on the shoutrrr side) |
| `method` | GET, POST, PUT, PATCH, DELETE (default POST) |
| `content_type` | default `application/json` |
| `headers[]` | extra headers (`@Header` query parameters internally); the dialog has a name/value editor for them |
| `body_template` | Go `text/template` rendered by Up before the request; empty means "use the default below", which is what the dialog sends |

Default body template (applied by the API when `body_template` is empty, so it is
what a channel created from the UI uses):

```json
{
  "event": "{{.Event}}",
  "monitor": "{{.MonitorName}}",
  "type": "{{.MonitorType}}",
  "target": "{{.MonitorURL}}",
  "status": "{{.Status}}",
  "message": "{{.Message}}",
  "latency_ms": {{.LatencyMS}},
  "node": "{{.NodeID}}",
  "timestamp": "{{.Timestamp}}"
}
```

### Slack

| Field | Notes |
|---|---|
| `token` | bot token (`xoxb-…`) or incoming webhook token (`hook:T…-B…-X…`) |
| `channel` | channel id (`C0123456789`) or `#channel`; with a webhook token it must be `webhook` |
| `bot_name` | overrides the app name of the message |
| `icon` | emoji (`:satellite:`) or an `https://` image URL |
| `thread_ts` | posts the message as a reply inside that thread (`1712345678.000100`) |

```
slack://xoxb-...@C0123456789?botname=Up&icon=%3Asatellite%3A
```

### Discord

| Field | Notes |
|---|---|
| `webhook_id` | first half of the webhook URL (`discord.com/api/webhooks/<webhook_id>/<token>`) |
| `token` | second half of that URL |
| `username` | overrides the webhook default username |
| `avatar_url` | overrides the webhook avatar |
| `thread_id` | posts the message into that thread |

```
discord://<token>@<webhook_id>?splitlines=no&username=Up
```

`splitlines=no` is forced by Up: shoutrrr would otherwise render one embed per
line of the body, turning a single alert into a stack of messages.

### Telegram

| Field | Notes |
|---|---|
| `token` | bot token from @BotFather, `<bot id>:<secret>` |
| `chats` | one or more chat ids, `@channel` names or `<chat id>:<thread id>` pairs, comma separated |
| `parse_mode` | `None` (default), `Markdown`, `HTML` or `MarkdownV2` |
| `disable_notification` | sends the message silently |
| `disable_preview` | hides the link previews of the URLs in the body |

```
telegram://123456789:AA...@telegram?chats=-1001234567890%2C%40mychannel&notification=yes&parsemode=None&preview=yes
```

`None` (what the UI pre-selects) is the safe choice: shoutrrr then renders the
bold title itself and escapes the body. With a Markdown mode the body travels
verbatim, so the check detail has to be escaped by the operator.

The three chat channels send the same plain text body as SMTP
(`Message.Text()`): the title becomes the Slack header, the Discord embed title
and the Telegram bold first line. The `body_template` of the Webhook channel is
not used by them.

Available template data (the `notify.Message` struct):

| Placeholder | Meaning |
|---|---|
| `{{.Event}}` | `down`, `up`, `test`, `cert_expiring` or `cert_expired` |
| `{{.Status}}` | aggregated status (`up`, `down`, `degraded`, `pending`, `maintenance`) |
| `{{.Title}}` | `[DOWN] Monitor name` |
| `{{.MonitorName}}`, `{{.MonitorType}}` | monitor identity |
| `{{.MonitorURL}}` | target (URL, `host:port` or `TYPE name @resolver`) |
| `{{.MonitorDescription}}` | description of the monitor |
| `{{.Message}}` | check result detail (`200 OK`, `connection refused`, ...) |
| `{{.LatencyMS}}` | latency in milliseconds (number) |
| `{{.NodeID}}` | node that produced the heartbeat |
| `{{.Timestamp}}` | event time (UTC, RFC3339 with `Format`) |
| `{{.InstanceURL}}` | `APP_URL` (useful for links) |

Template helpers: `json`, `urlquery`, `upper`, `lower`, `trim`,
`default "fallback" .Value`.

Example: Slack-compatible payload

```json
{"text": "{{.Title}}\nTarget: {{.MonitorURL}}\nDetail: {{.Message}}"}
```

Example: adding a shared secret header

```json
{"key":"Authorization","value":"Bearer my-token"}
```

Plus, for example, a dynamic value inside the body:

```json
{
  "event": "{{.Event}}",
  "severity": "{{if eq .Event \"down\"}}critical{{else}}info{{end}}"
}
```

## 3. Managing channels

| Action | Endpoint |
|---|---|
| List (with linked monitor ids) | `GET /api/notifications` |
| Read one | `GET /api/notifications/:id` |
| Create | `POST /api/notifications` |
| Update | `PUT /api/notifications/:id` |
| Delete (removes links and logs) | `DELETE /api/notifications/:id` |
| Send a test message | `POST /api/notifications/:id/test` |
| Delivery history | `GET /api/notifications/logs?limit=100` |

Create an SMTP channel:

```bash
curl -b cookies.txt -X POST http://localhost:3000/api/notifications \
  -H 'Content-Type: application/json' -d '{
    "name": "Ops SMTP",
    "type": "smtp",
    "active": true,
    "resend_interval_seconds": 3600,
    "config": {
      "smtp": {
        "host": "smtp.example.com", "port": 587,
        "username": "up@example.com", "password": "secret",
        "from": "up@example.com", "to": "ops@example.com,oncall@example.com",
        "secure": false, "use_html": true, "skip_tls_verify": false,
        "subject_prefix": "[Up]"
      }
    }
  }'
```

Create a webhook channel:

```bash
curl -b cookies.txt -X POST http://localhost:3000/api/notifications \
  -H 'Content-Type: application/json' -d '{
    "name": "Ops Webhook",
    "type": "webhook",
    "active": true,
    "config": {
      "webhook": {
        "url": "https://hooks.example.com/up",
        "method": "POST",
        "content_type": "application/json",
        "headers": [{"key": "X-Source", "value": "up"}],
        "body_template": "{\"alert\":\"{{.Event}}\",\"monitor\":\"{{.MonitorName}}\"}"
      }
    }
  }'
```

Create a Slack channel (bot token):

```bash
curl -b cookies.txt -X POST http://localhost:3000/api/notifications \
  -H 'Content-Type: application/json' -d '{
    "name": "Ops Slack",
    "type": "slack",
    "active": true,
    "config": {
      "slack": {
        "token": "xoxb-...",
        "channel": "C0123456789",
        "bot_name": "Up",
        "icon": ":satellite:",
        "thread_ts": ""
      }
    }
  }'
```

Create a Discord channel (the two halves of the webhook URL):

```bash
curl -b cookies.txt -X POST http://localhost:3000/api/notifications \
  -H 'Content-Type: application/json' -d '{
    "name": "Ops Discord",
    "type": "discord",
    "active": true,
    "config": {
      "discord": {
        "webhook_id": "123456789012345678",
        "token": "abcdefghijklmnopqrstuvwxyz",
        "username": "Up",
        "avatar_url": "",
        "thread_id": ""
      }
    }
  }'
```

Create a Telegram channel (bot token from @BotFather, one or more chats):

```bash
curl -b cookies.txt -X POST http://localhost:3000/api/notifications \
  -H 'Content-Type: application/json' -d '{
    "name": "Ops Telegram",
    "type": "telegram",
    "active": true,
    "config": {
      "telegram": {
        "token": "123456789:AA...",
        "chats": "-1001234567890,@mychannel",
        "parse_mode": "None",
        "disable_notification": false,
        "disable_preview": false
      }
    }
  }'
```

Only the credentials and the destination are mandatory (`slack.token` +
`slack.channel`, `discord.webhook_id` + `discord.token`, `telegram.token` +
`telegram.chats`); every other field is optional. A chat channel can be created
without the matching block only for `smtp`/`webhook`: for the other three the
API answers `config.slack is required for Slack notifications` (and the
equivalents).

Link channels to a monitor through the monitor payload (`notification_ids`).

Only the writable fields belong in the body: `name`, `type`, `active`,
`is_default`, `resend_interval_seconds` and `config`. `id`, `created_at`,
`updated_at` and `monitor_ids` are read-only - sending an empty `created_at` back
is rejected with `ERR_INVALID_PAYLOAD` ("cannot parse ...") - and the numbers stay
numbers (`"port": 587`, never `"587"`). See `docs/api.md` §5.

## 4. Delivery log

Every attempt produces one row in `notification_logs`:

| Column | Meaning |
|---|---|
| `notification_id`, `monitor_id` | who/what |
| `event` | `down`, `up`, `test`, `cert_expiring`, `cert_expired`, `domain_expiring` or `domain_expired` |
| `success` | delivery accepted by the endpoint |
| `error` | reason when `success=0` (the shoutrrr error message, whatever the channel) |
| `duration_ms` | time of the attempt |
| `node_id` | cluster node that performed the send |

Query it directly for forensics:

```sql
SELECT created_at, event, notification_id, success, LEFT(error,120) AS error, node_id
FROM notification_logs ORDER BY id DESC LIMIT 20;
```

The history is kept **forever by default**. Set `NOTIFICATION_LOG_RETENTION_DAYS`
to a positive number and the maintenance loop deletes the entries older than that
every six hours (the same opt-in convention as `HEARTBEAT_RETENTION_DAYS`).

The notification *de-duplication* rows in `notification_locks` are different:
they are meaningless after their own window (60 s for a status event, the day for
a certificate or domain reminder), so they are pruned automatically after
`models.NotificationLockRetentionHours` (24 h) with no configuration. The prune
compares `created_at`, never `bucket`, because a bucket is a minute window for
status events but a day number for certificate/domain events.

## 5. Testing a channel from the CLI

```bash
echo "notification test: $(curl -s -b cookies.txt -X POST localhost:3000/api/notifications/1/test)"
```

A local SMTP sink (Mailpit) and a webhook sink make this verifiable without any
external service; `docs/development.md` documents both commands. Remember that a
container must use `host.docker.internal` instead of `127.0.0.1` to reach
services running on the Docker host.

The chat channels have no local sink: the *Test* button (and the endpoint above)
talks to the real Slack/Discord/Telegram API, which is exactly what validates a
bot token, a webhook id or a chat id. The delivery log row keeps the API error
verbatim (`invalid token`, `Unknown Channel`, `chat not found`, ...), so a
failing configuration can be diagnosed without reading the application log.

## 6. Common failures

| Log error | Cause |
|---|---|
| `smtp: error connecting to server: dial tcp ... connection refused` | wrong host/port, or `127.0.0.1` from inside a container (use `host.docker.internal`) |
| `smtp: ... x509: certificate signed by unknown authority` | self-signed relay: enable `skip_tls_verify` |
| `smtp: error applying params ...` | unknown parameter sent to the smtp service (a bug: only `title` is forwarded) |
| `generic: failed to send notification ... server returned unexpected response status code` | the webhook answered 4xx/5xx (the status is in the error) |
| `slack: failed to send slack notification: invalid_auth` | wrong/revoked bot token, or the app was removed from the channel |
| `slack: ... channel_not_found` | `channel` is not the id of a channel the bot can post to (invite the app first) |
| `discord: 404 Not Found` | `webhook_id` or `token` truncated (copy both halves of the webhook URL) |
| `discord: 400 Bad Request` | `thread_id` that does not belong to the webhook channel |
| `telegram: invalid telegram token: ...` | `token` is not `<bot id>:<secret>` (the value from @BotFather, including the colon) |
| `telegram: ... chat not found` | bad `chats` entry, or the bot was never started/added to that chat |
| `telegram: ... can't parse entities` | a Markdown parse mode with unescaped characters in the check detail: use `None` |
| Notification sent but not received and no log row | the monitor was never linked to the channel (`notification_ids`) |
