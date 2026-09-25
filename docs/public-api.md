# Up - Public REST API

> Token protected, read-mostly API for scripts, CI pipelines and dashboards.
> Everything is JSON and identical to what the UI consumes.

## 1. Creating a token

Admin UI: **Admin > Security > API tokens > New token**.

```bash
curl -s -b cookies.txt -X POST http://localhost:3000/api/tokens \
  -H 'Content-Type: application/json' \
  -d '{"name":"ci-pipeline","scopes":["read"],"expires_in_days":90}'

# {
#   "id": 3, "name": "ci-pipeline", "prefix": "3ea66d25",
#   "scopes": ["read"], "expires_at": "2026-12-21T18:00:00Z",
#   "token": "up_3ea66d25_5ade146b1d2ebe20a98e4c211fa882bfa493d3cb3961dddc"
# }
```

The plain `token` is returned **only** in this response. Store it in a secret
manager; only its SHA-256 hash lives in the database.

Scopes:

| Scope | Allows |
|---|---|
| `read` | status, monitors, heartbeats, statistics, status pages |
| `write` | additionally pause/resume monitors |

## 2. Using the token

```bash
TOKEN=up_3ea66d25_5ade...
curl -H "Authorization: Bearer $TOKEN" https://up.example.com/api/v1/status
```

| Error | Meaning |
|---|---|
| `403 ERR_TOKEN_REQUIRED` | header missing |
| `403 ERR_TOKEN_INVALID` | unknown prefix or hash mismatch |
| `403 ERR_TOKEN_EXPIRED` | past `expires_at` or revoked |
| `403 ERR_TOKEN_SCOPE_INSUFFICIENT` | the scope is missing (e.g. pausing with `read`) |
| `429 ERR_RATE_LIMITED` | above `SECURITY_PUBLIC_RATE_LIMIT` |
| `403 ERR_IP_BLOCKED` | an IP rule blocks the `api` scope |

## 3. Endpoints

### `GET /api/v1/status`

Cheap aggregate, ideal for a badge or a chat command:

```json
{
  "overall": "down", "total": 5, "up": 4, "down": 1, "degraded": 0, "unknown": 0,
  "node_id": "up-node-1", "generated_at": "2026-09-22T18:00:00Z", "monitors": []
}
```

Query: `?include_monitors=true` adds the (redacted) monitor list.

### `GET /api/v1/monitors`

Query: `search`, `type`, `tag`, `active`, `include_secrets`.

```json
[{"id":6,"name":"HTTP Health","type":"http","active":true,
  "status":"up","uptime":99.99,"uptime_hours":24,"uptime_24h":99.99,
  "last_latency_ms":12,"last_check_at":"...","tags":"core",
  "config":{"url":"https://api.example.com/health","method":"GET","basic_pass":"***"},
  "notification_ids":[1]}]
```

`uptime`/`uptime_hours` follow the configured window (Admin > Settings);
`uptime_24h`, `uptime_7d` and `uptime_30d` remain the fixed windows, so existing
consumers keep working.

Credentials are masked with `***` unless `include_secrets=true`. The payload is
the same decorated monitor the dashboard uses, so a monitor that watches its
certificate or its domain carries `certificate` (`issuer`, `subject`, `not_after`,
`days_left`, `captured_at`) and `domain` (`domain`, `registrar`, `expires_at`,
`source`, `status`, `days_left`, `checked_at`) when the watch is on and an
observation exists. `GET /api/v1/monitors/:id`, `GET /api/v1/monitors` and
`GET /api/v1/status?include_monitors=true` all return the same decoration.

### `GET /api/v1/monitors/:id`

`{"monitor": {...}, "stats": {...}}` (`?hours=24`).

### `GET /api/v1/monitors/:id/heartbeats`

`?hours=24&limit=100` -> the raw heartbeat list (newest first).

### `GET /api/v1/monitors/:id/uptime`

```json
{"monitor_id":6,"hours":24,"up":1420,"down":2,"pending":0,"total":1422,
 "uptime":99.86,"avg_ms":18.4,"min_ms":8,"max_ms":412,"p95_ms":41}
```

### `POST /api/v1/monitors/:id/pause` and `.../resume`

Require the `write` scope; both return the updated (redacted) monitor.

### `GET /api/v1/status-pages/:slug`

Same payload as the public status page (useful to render your own frontend).
The `certificate`/`domain` badges of the monitors are only included when the
page has `show_expiry` on (see [status-pages.md](status-pages.md)).

## 4. Recipes

```bash
# exit non-zero when anything is down (CI / cron watch)
out=$(curl -s -H "Authorization: Bearer $TOKEN" https://up.example.com/api/v1/status)
[ "$(echo "$out" | jq -r .overall)" = "up" ] || { echo "incident: $out"; exit 1; }

# uptime of one monitor over 7 days
curl -s -H "Authorization: Bearer $TOKEN" \
  "https://up.example.com/api/v1/monitors/6/uptime?hours=168" | jq .uptime

# pause a monitor during a maintenance window, then resume
curl -s -X POST -H "Authorization: Bearer $TOKEN" https://up.example.com/api/v1/monitors/6/pause
curl -s -X POST -H "Authorization: Bearer $TOKEN" https://up.example.com/api/v1/monitors/6/resume
```

## 5. Operational guidance

- Create **one token per consumer** so it can be revoked independently.
- Set an expiry; renewing a token is one API call.
- Use the `read` scope unless a workflow really needs to pause/resume.
- Combine tokens with IP rules (`scope=api`) to restrict the API to known egress
  addresses.
- Rotate after any leak: revoke the old token first, then create a new one.
