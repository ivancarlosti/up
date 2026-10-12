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
| `read` | status, monitors, heartbeats, statistics, status pages, on-demand analysis |
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
  "config":{"url":"https://api.example.com/health","method":"GET","ip_family":"auto","basic_pass":"***"},
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
page has the matching switch on, `show_cert_expiry` for the certificate and
`show_domain_expiry` for the domain (see [status-pages.md](status-pages.md)).

### `POST /api/v1/analyze`

Runs the probes of a monitor against a target **you type in**, and stores
nothing: no monitor, no heartbeat, no certificate/domain/performance row, no
notification. It is the script friendly counterpart of "check now" (which needs
an existing monitor) and the way a pipeline inspects a target before it becomes
one.

```bash
curl -s -X POST -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  https://up.example.com/api/v1/analyze \
  -d '{"type":"http","url":"https://example.com","checks":["uptime","certificate"]}'
```

The body is the flat monitor configuration (`url`, `method`, `headers`, `host`,
`port`, `keyword`, ...) plus the options below, so a payload that works here
works in `POST /api/monitors` - minus `name`, `interval_seconds` and the tags,
which an analysis does not need:

| Field | Default | Meaning |
|---|---|---|
| `type` | *required* | the probe to run: `http`, `keyword`, `tcp`, `dns`, `ssl`, `websocket` |
| `url`, `host`, `hostname`, `port`, `keyword`, ... | *required per type* | the target, validated exactly like a monitor |
| `checks` | `["uptime"]`, plus `"certificate"` when the target has a TLS endpoint | the probes to run: `uptime`, `certificate`, `domain`, `performance` |
| `timeout_seconds` | `10` | bound of ONE probe (1..300) |
| `retries` | `0` | extra attempts of a DOWN uptime probe (0..5) |
| `retries_interval_seconds` | `5` | pause between two attempts (1..60, only used when `retries` > 0) |
| `upside_down` | `false` | invert the uptime verdict (an unreachable target is up) |

`domain` (a registry lookup) and `performance` (a PageSpeed Insights unit) are
never implicit: ask for them. Every current type has a registrable domain, so
`domain` is always accepted; `performance` needs an absolute `http(s)` url
(`lighthouse_strategy: "mobile"` - the default - `"desktop"` or `"both"`).

The answer carries one section per probe that ran and omits the others:

```json
{
  "target": "https://example.com", "type": "http", "node_id": "up-node-1",
  "generated_at": "2026-10-12T18:00:00Z", "checks": ["uptime", "certificate", "domain"],
  "uptime": {"status": "up", "latency_ms": 128, "status_code": 200, "attempts": 1},
  "certificate": {"status": "ok", "days_left": 47, "info": {"subject": "example.com",
    "issuer": "R11", "not_before": "2026-09-16T00:00:00Z",
    "not_after": "2026-11-28T23:59:59Z", "days_left": 47,
    "captured_at": "2026-10-12T18:00:00Z", "captured_by_node": "up-node-1"}},
  "domain": {"status": "ok", "info": {"domain": "example.com", "registrar": "IANA",
    "expires_at": "2027-08-13T04:00:00Z", "source": "rdap", "status": "ok",
    "days_left": 305, "checked_at": "2026-10-12T18:00:00Z"}},
  "timings_ms": {"total": 412, "uptime": 128, "certificate": 96, "domain": 380}
}
```

How to read it:

- **A probe that fails is data, not an error.** Every section carries its own
  `status`: `ok` when the observation is there, `error` with the reason when it
  could not be read. The uptime section is the exception and reports the monitor
  verdict itself (`up`, `down`, `pending`, `maintenance`) with the `status_code`,
  the `message` and how many `attempts` were used.
- **A failed section never hides a good one.** A target whose certificate cannot
  be read is still worth an uptime answer, and a page measured on both
  strategies reports the mobile run next to a refused desktop one.
- The probes run **concurrently**, so `timings_ms.total` is the slowest probe and
  not the sum. The call is synchronous: `performance` waits for PageSpeed
  Insights (seconds), the others are fast.
- **`read` is enough.** The node does open an **outbound connection** to the
  target you send, so treat the token like any egress capable credential (see
  sections 5 and the IP rules of `scope=api`).

| Error | Meaning |
|---|---|
| `400 ERR_INVALID_PAYLOAD` | the body is not JSON or does not match the payload |
| `400 ERR_MONITOR_TYPE_INVALID` | `type` is not one of the six monitor types |
| `400 ERR_MONITOR_CONFIG_INVALID` | the configuration is incomplete or malformed (`config.url is required`, ...) |
| `400 ERR_VALIDATION` | an unknown check name, or `checks`/`timeout_seconds`/`retries`/`retries_interval_seconds` out of range |
| `400 ERR_MONITOR_CERT_INVALID` | a `certificate` check on a type without TLS support or on a target without a TLS endpoint (an `http://` url, a tcp host) |
| `400 ERR_MONITOR_LIGHTHOUSE_INVALID` | a `performance` check on a target PageSpeed Insights cannot audit |
| `503 ERR_INTERNAL` | the endpoint is not wired in this process |

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

# inspect a target BEFORE adding it as a monitor (nothing is stored)
curl -s -X POST -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  https://up.example.com/api/v1/analyze \
  -d '{"type":"http","url":"https://example.com","checks":["uptime","certificate"]}'

# pre-flight of a deployment: fail when the certificate is close to expiry
days=$(curl -s -X POST -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  https://up.example.com/api/v1/analyze \
  -d '{"type":"ssl","host":"example.com","port":443,"checks":["certificate"]}' \
  | jq -r '.certificate.days_left')
[ "$days" -gt 21 ] || { echo "certificate expires in $days days"; exit 1; }

# score a page without a monitor (mobile by default, "both" for two runs)
curl -s -X POST -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  https://up.example.com/api/v1/analyze \
  -d '{"type":"http","url":"https://example.com","lighthouse_strategy":"both","checks":["performance"]}' \
  | jq '.performance.runs[] | {strategy, performance, largest_contentful_paint_ms}'
```

## 5. Operational guidance

- Create **one token per consumer** so it can be revoked independently.
- Set an expiry; renewing a token is one API call.
- Use the `read` scope unless a workflow really needs to pause/resume.
- Combine tokens with IP rules (`scope=api`) to restrict the API to known egress
  addresses.
- `POST /api/v1/analyze` runs the probe against the target **in the body**, so a
  token can make the node dial an arbitrary host:port. The IP rules authenticate
  the caller, they do not restrict what the node reaches: hand that token only to
  consumers you would trust with an outbound request.
- Rotate after any leak: revoke the old token first, then create a new one.
