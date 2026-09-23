# Up - Notifications

> Notifications are delivered by the
> [shoutrrr](https://github.com/nicholas-fedor/shoutrrr) engine. The UI exposes
> two channels, **SMTP** and **Webhook**, and each monitor can be linked to any
> number of them.

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

Available template data (the `notify.Message` struct):

| Placeholder | Meaning |
|---|---|
| `{{.Event}}` | `down`, `up` or `test` |
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

Link channels to a monitor through the monitor payload (`notification_ids`).

Only the writable fields belong in the body: `name`, `type`, `active`,
`is_default`, `resend_interval_seconds` and `config`. `id`, `created_at`,
`updated_at` and `monitor_ids` are read-only - sending an empty `created_at` back
is rejected with `ERR_INVALID_PAYLOAD` ("cannot parse ...") - and the numbers stay
numbers (`"port": 587`, never `"587"`). See `docs/api.md` §4.

## 4. Delivery log

Every attempt produces one row in `notification_logs`:

| Column | Meaning |
|---|---|
| `notification_id`, `monitor_id` | who/what |
| `event` | `down`, `up` or `test` |
| `success` | delivery accepted by the endpoint |
| `error` | reason when `success=0` (SMTP/shoutrrr message) |
| `duration_ms` | time of the attempt |
| `node_id` | cluster node that performed the send |

Query it directly for forensics:

```sql
SELECT created_at, event, notification_id, success, LEFT(error,120) AS error, node_id
FROM notification_logs ORDER BY id DESC LIMIT 20;
```

## 5. Testing a channel from the CLI

```bash
echo "notification test: $(curl -s -b cookies.txt -X POST localhost:3000/api/notifications/1/test)"
```

A local SMTP sink (Mailpit) and a webhook sink make this verifiable without any
external service; `docs/development.md` documents both commands. Remember that a
container must use `host.docker.internal` instead of `127.0.0.1` to reach
services running on the Docker host.

## 6. Common failures

| Log error | Cause |
|---|---|
| `smtp: error connecting to server: dial tcp ... connection refused` | wrong host/port, or `127.0.0.1` from inside a container (use `host.docker.internal`) |
| `smtp: ... x509: certificate signed by unknown authority` | self-signed relay: enable `skip_tls_verify` |
| `smtp: error applying params ...` | unknown parameter sent to the smtp service (a bug: only `title` is forwarded) |
| `generic: failed to send notification ... server returned unexpected response status code` | the webhook answered 4xx/5xx (the status is in the error) |
| Notification sent but not received and no log row | the monitor was never linked to the channel (`notification_ids`) |
