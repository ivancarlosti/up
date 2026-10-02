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
- `resend_interval_seconds` exists at the monitor level and at the channel level,
  and the **larger** value wins (`models.EffectiveResendInterval`): a channel can
  ask to repeat more often than its monitor does, never less often. Only the
  channels that can actually receive a "down" alert count (active ones whose link
  has `on_down`), because the interval decides whether *they* are attempted again.
- The re-notification clock (`monitor_states.notified_at`) advances only when at
  least one channel was attempted. An incident evaluated while the monitor has no
  eligible channel therefore stays re-evaluable, so linking a channel while the
  monitor is down sends the alert on the next check (at most one attempt per
  notification-lock window, see `NotificationLockWindowSeconds`). A channel that was
  attempted and **failed** does advance the clock: the error is in the delivery log,
  and re-sending a broken endpoint on every check is what the resend interval is for.

### Certificate and domain events

`cert_expiring`, `cert_expired`, `domain_expiring` and `domain_expired` do not
come from a status transition: they come from the TLS certificate a probe read
(see `docs/monitors.md` §9) or from the registry expiration of the monitor's
domain (see `docs/monitors.md` §10). They are delivered through the same channels
and the same `on_down` link flag, and the transport is deduplicated per day and
per threshold, so several cluster nodes watching the same certificate or domain
produce a single message.

Every message also carries the URL of the page it is about - the monitor detail
for a status event, the expiry worklist for these reminders - see
*Where a notification links to* in §2.

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
| `url` | `http://` or `https://` (plain HTTP sets `disabletls=yes` on the shoutrrr side). The URL reaches the target **unchanged**: a token in the query (`https://target/hook?token=…`) and userinfo Basic auth (`https://user:pass@target/…`) are both delivered, so targets that authenticate through the URL keep working. A query parameter named after a shoutrrr generic setting (`method`, `contenttype`, `template`, `title`, `titlekey`, `messagekey`, `disabletls`) is escaped with the `__` prefix (`format.EscapeKey`) and restored by shoutrrr, so `?method=GET` arrives at the target as `method=GET` while the `method` field keeps controlling the request verb. Parameters prefixed with `@` or `$` are shoutrrr's own syntax for headers and template data: they arrive as request headers (or as template data), not as query parameters |
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
  "dashboard_url": "{{.DashboardURL}}",
  "status": "{{.Status}}",
  "message": "{{.Message}}",
  "tags": {{json .MonitorTags}},
  "group": "{{.MonitorGroup}}",
  "group_id": {{.MonitorGroupID}},
  "latency_ms": {{.LatencyMS}},
  "node": "{{.NodeID}}",
  "timestamp": "{{.Timestamp}}"
}
```

The API fills `body_template` with the block above whenever it is left empty
(`Notification.Normalize`), so this is what a channel created or saved from the
dialog renders. A row with no template at all falls back to
`Message.jsonPayload()`: the same fields plus `instance_url`, `dashboard_url` and
`tags_csv`, which a template can reach through `{{.InstanceURL}}`,
`{{.DashboardURL}}` and `{{.TagsCSV}}`. The group is a single value: `group` and
`group_id` are the group of the monitor (`""` and `0` when it belongs to none).
Adding `group` and `group_id` to the block only affects the channels saved after
the change, for the same reason as the fields below.
Adding `dashboard_url` to the block only affects the channels saved after it
appeared: a row that already stored a template keeps it verbatim (the dialog
textarea has no *reset* button), so an existing channel picks the new field up
only when the line is added by hand or the field is emptied before saving.

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
| `{{.MonitorTags}}` | tags of the monitor, as an array (`["web","api"]`) |
| `{{.MonitorGroup}}`, `{{.MonitorGroupID}}` | the group of the monitor (name and id; `""` and `0` when it belongs to none) |
| `{{.TagsCSV}}` | the tags as one comma separated string (`web,api`) |
| `{{.Message}}` | check result detail (`200 OK`, `connection refused`, ...) |
| `{{.LatencyMS}}` | latency in milliseconds (number) |
| `{{.NodeID}}` | node that produced the heartbeat |
| `{{.Timestamp}}` | event time (UTC, RFC3339 with `Format`) |
| `{{.InstanceURL}}` | `APP_URL`, the root of the instance (useful to build your own link) |
| `{{.DashboardURL}}` | the page of the instance the alert is about (see *Where a notification links to* below): the monitor detail for a status event, the expiry worklist for a certificate/domain reminder. Empty when `APP_URL` is not set |

### Where a notification links to

Every notification carries the URL of the page that shows the thing it is about,
so an operator (or a chat client that renders links) can jump straight to it
instead of landing on the instance root and searching:

| Notification | `DashboardURL` | Why |
|---|---|---|
| `down`, `up` (and any other status event) | `<APP_URL>/monitors/<id>` | The monitor detail page holds the statistics, the event log and the heartbeats of the alerting monitor - whatever its type is (`http`, `keyword`, `tcp`, `dns`, `ssl`) |
| `cert_expiring`, `cert_expired`, `domain_expiring`, `domain_expired` | `<APP_URL>/admin/expiry` | The reminder is about a *target*: the expiry worklist deduplicates the certificates and the domains, and the same entry can back many monitors, so no single monitor page shows the whole story |
| `test` | `<APP_URL>` | The sample monitor of a test message is not stored, so there is no detail page to open (`/monitors/0` would be a dead link) |

`APP_URL` is the instance origin (`https://br01.up.icc.gg`). It is trimmed of a
trailing slash and the path is appended, so `https://br01.up.icc.gg` and
`https://br01.up.icc.gg/` both produce `https://br01.up.icc.gg/monitors/445`.
With no `APP_URL` the field is empty (the link is simply absent) and
`{{.DashboardURL}}` renders `""` - never a bare path, which a receiver could not
resolve. The plain text and HTML bodies used by SMTP, Slack, Discord and
Telegram print the same URL as a `Dashboard:` line (or an `<a href>` in HTML)
when it is not empty; `{{.InstanceURL}}` stays available for a template that
wants the instance root itself.

Template helpers: `json`, `urlquery`, `upper`, `lower`, `trim`,
`default "fallback" .Value`.

`{{.MonitorTags}}` is a slice, so use `{{json .MonitorTags}}` to embed it in a
JSON body or `{{range .MonitorTags}}…{{end}}` to walk it. The group is a plain
string (`{{.MonitorGroup}}`) with its id next to it (`{{.MonitorGroupID}}`). Use
`{{.TagsCSV}}` when a single flat string is easier to consume: it keeps the same
order as the array (a monitor with no tag renders `[]` and `""`, never `null`). The
tags and the group of the monitor
are also listed in the plain text/HTML body used by SMTP, Slack, Discord and
Telegram, when they are not empty, followed by the `Dashboard:` link of the event
when `APP_URL` is set.

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

Link channels to a monitor through the monitor payload (`notification_ids`), or
with the channel checkboxes of the monitor editor in the UI: that is the **only**
place a link is created (besides the cluster sync). A channel write ignores
`monitor_ids`, and `GET /api/notifications` computes it from the links, which is
what the "linked monitors" badge on each card counts.

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
every six hours. The variable is the only place this policy is read from (the
heartbeat retention works differently: its live value is the shared setting and
the variable is only the fallback of a deployment that never opened
Admin > Settings, see `docs/database.md`).

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
| `generic: failed to send notification ... server returned unexpected response status code` | the webhook answered 4xx/5xx (the status is in the error); with a credential in the URL, check that it is in the query the target expects (`?token=…`) - the whole query is forwarded |
| `slack: failed to send slack notification: invalid_auth` | wrong/revoked bot token, or the app was removed from the channel |
| `slack: ... channel_not_found` | `channel` is not the id of a channel the bot can post to (invite the app first) |
| `discord: 404 Not Found` | `webhook_id` or `token` truncated (copy both halves of the webhook URL) |
| `discord: 400 Bad Request` | `thread_id` that does not belong to the webhook channel |
| `telegram: invalid telegram token: ...` | `token` is not `<bot id>:<secret>` (the value from @BotFather, including the colon) |
| `telegram: ... chat not found` | bad `chats` entry, or the bot was never started/added to that chat |
| `telegram: ... can't parse entities` | a Markdown parse mode with unescaped characters in the check detail: use `None` |
| `no notification channel accepted the event` (warning, logged with `channels=0`) | the incident was evaluated with no eligible channel: no link, the channel is disabled, or the link has `on_down` off. Nothing is recorded as reported, so the alert is sent as soon as a channel becomes eligible |
| Notification sent but not received and no log row | the monitor was never linked to the channel (`notification_ids`) |
