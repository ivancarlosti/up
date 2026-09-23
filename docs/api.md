# Up - API reference

> Base URL: `APP_URL` (e.g. `https://up.example.com`). Every endpoint lives under
> `/api`. Bodies are JSON; errors use the shape
> `{"code":"ERR_...","message":"..."}` where `code` is stable and translated by
> the frontend.

## 1. Authentication summary

| Surface | Credential |
|---|---|
| `/api/auth/*`, `/api/health`, `/api/version`, `/api/settings` | none |
| admin API (`/api/monitors`, ...) | session cookie (`up_session`) or fully open with `AUTH_METHOD=none` |
| `/api/public/*` | none (IP rules scope `public` + rate limit) |
| `/api/v1/*` | `Authorization: Bearer up_<prefix>_<secret>` (scope `read`/`write`) |
| `/api/cluster/join` (node flavour) | `X-Cluster-Key` / body `private_key` |
| `/api/ws` | session cookie |

Status codes: `200`/`201` success, `204` no content, `400` invalid payload,
`401` not authenticated, `403` forbidden (IP rule, token scope, allow list),
`404` unknown resource or route, `409` conflict, `429` rate limited,
`500` internal, `503` dependency unavailable.

## 2. Bootstrap and authentication

### `GET /api/health`

```json
{"status":"ok","version":"1.0.0","node_id":"up-node-1","database":"ok","time":"2026-09-22T18:00:00Z"}
```

`503` with `"status":"degraded"` when the database is unreachable. Used by the
container health check.

### `GET /api/version`

```json
{"version":"1.0.0","commit":"a7059a2","readable":"1.0.0 (a7059a2)"}
```

### `GET /api/settings`

Public bootstrap payload used by the SPA:

```json
{
  "app_name":"Up", "version":"dev",
  "auth_method":"account", "auth_enabled":true,
  "recaptcha_enabled":false, "recaptcha_client_id":"",
  "default_locale":"en-US", "default_theme":"system",
  "supported_locales":["en-US","pt-BR","es-MX"],
  "supported_themes":["system","light","dark"],
  "cluster_enabled":true, "node_id":"up-node-1", "node_name":"Primary Node"
}
```

### `GET /api/auth/session`

```json
{"authenticated":true,"auth_method":"account","login_enabled":true,"oidc_enabled":false,
 "recaptcha_enabled":false,"recaptcha_client_id":"","instance_url":"https://up.example.com",
 "identity":{"email":"admin@example.com","method":"account"}}
```

### `POST /api/auth/login`

```json
{"email":"admin@example.com","password":"admin123","recaptcha_token":""}
```

Sets the `up_session` cookie. Errors: `401 ERR_AUTH_INVALID_CREDENTIALS`,
`403 ERR_AUTH_METHOD_DISABLED`, `403 ERR_RECAPTCHA_MISSING`,
`403 ERR_RECAPTCHA_FAILED`, `429 ERR_RATE_LIMITED`.

### `POST /api/auth/logout` -> `204`

### `GET /api/auth/oidc/login?redirect=/admin/cluster`

302 to the identity provider (Authorization Code + PKCE, see
`docs/authentication.md`).

### `GET /api/auth/callback`

Handled by the provider redirect; sets the session cookie and redirects to
`APP_URL + redirect`. Failures render a small HTML page with the error code
(`ERR_CLUSTER_*` style codes, `ERR_AUTH_DOMAIN_NOT_ALLOWED`, ...).

## 3. Dashboard and monitors

### `GET /api/dashboard`

Query: `search`, `type`, `tag`, `active`.

```json
{
  "monitors": [{ "id": 6, "name": "HTTP Health", "type": "http", "status": "up",
                 "uptime_24h": 99.98, "last_latency_ms": 12, "last_check_at": "...",
                 "heartbeats": [{"status":"up","latency_ms":12,"created_at":"..."}],
                 "votes": [{"node_id":"up-node-1","node_name":"Primary Node","status":"up","online":true}],
                 "notification_ids": [1] }],
  "summary": { "total": 5, "up": 4, "down": 1, "degraded": 0, "paused": 0,
               "heartbeats_1h": 312, "ws_clients": 2, "cluster": { } }
}
```

### `GET /api/monitors`

Same payload without the summary; `?decorate=false` skips the runtime fields
(faster, used by pickers).

### `GET /api/monitors/:id`

A single decorated monitor.

### `POST /api/monitors`

Body: `models.Monitor` fields plus `notification_ids`. Response `201` with the
decorated monitor. Validation errors use
`ERR_MONITOR_CONFIG_INVALID` / `ERR_MONITOR_TYPE_INVALID` / `ERR_VALIDATION`
(see `docs/monitors.md` for every field).

### `PUT /api/monitors/:id`

Same body as create. Omitting `notification_ids` keeps the current links;
`[]` clears them.

### `DELETE /api/monitors/:id` -> `204`

Removes the monitor, its heartbeats, state row, notification links and lock rows.

### `POST /api/monitors/:id/pause` and `.../resume`

Stop/start the workers of that monitor (the history is preserved).

### `POST /api/monitors/:id/check` -> `202 {"queued":true,"monitor_id":6}`

### `GET /api/monitors/:id/heartbeats?hours=24&limit=500`

```json
[{"id":4821,"monitor_id":6,"node_id":"up-node-1","status":"up","latency_ms":12,
  "status_code":200,"message":"200 OK","important":true,"created_at":"..."}]
```

### `GET /api/monitors/:id/stats?hours=24&limit=60`

```json
{
  "stats": {"monitor_id":6,"hours":24,"up":1420,"down":2,"pending":0,"total":1422,
            "uptime":99.86,"avg_ms":18.4,"min_ms":8,"max_ms":412,"p95_ms":41},
  "series": [{"status":"up","latency_ms":12,"created_at":"...","node_id":"up-node-1"}]
}
```

### `POST /api/monitors/:id/clone` -> `201`

Body (all optional): `{"name": "...", "copy_notifications": true, "copy_groups": true}`.
An empty `name` produces `<name> (copy)`; the heartbeat history and the aggregated
state are never copied, so the copy starts clean. The worker of the new monitor
starts on the node that served the request.

## 4. Monitor groups

| Method | Path | Notes |
|---|---|---|
| GET | `/api/monitor-groups` | list with `monitor_ids` and `monitor_count` |
| POST | `/api/monitor-groups` | create (`{"name": "...", "monitor_ids": [6,7]}`) |
| GET | `/api/monitor-groups/:id` | single group |
| PUT | `/api/monitor-groups/:id` | update; omitting `monitor_ids` keeps the members |
| DELETE | `/api/monitor-groups/:id` | delete the group (the monitors stay) |
| PUT | `/api/monitor-groups/:id/monitors` | `{"monitor_ids": [6,7]}` |
| POST | `/api/monitor-groups/:id/clone` | `{"name": "...", "deep": true, "copy_links": true}` |

A monitor belongs to any number of groups: the create/update payload of a monitor
takes `group_ids` (omitted = keep the current groups, `[]` = clear them) and the
listing filters with `?group_id=`. Names are unique
(`409 ERR_MONITOR_GROUP_INVALID`) and an unknown id answers
`400 ERR_MONITOR_GROUP_INVALID` / `404 ERR_MONITOR_GROUP_NOT_FOUND`.

`deep: true` also clones the monitors of the group (each one with its `(copy)`
name and, with `copy_links`, its channels and groups); without `deep` the copy
starts empty on purpose, so two groups cannot silently share the same monitors.

## 5. Notifications

| Method | Path | Notes |
|---|---|---|
| GET | `/api/notifications` | list with `monitor_ids` |
| GET | `/api/notifications/:id` | single channel |
| POST | `/api/notifications` | create (see `docs/notifications.md`) |
| PUT | `/api/notifications/:id` | update |
| DELETE | `/api/notifications/:id` | delete links + logs |
| POST | `/api/notifications/:id/test` | immediate test delivery, returns a `NotificationLog` |
| GET | `/api/notifications/logs?limit=100` | delivery history |

### `POST /api/notifications` and `PUT /api/notifications/:id`

Body: the writable fields of `models.Notification` (`name`, `type`, `active`,
`is_default`, `resend_interval_seconds`) plus the `config` block of that type
(see `docs/notifications.md` for every field).

The response-only fields stay out of the request: `created_at`/`updated_at` are
`time.Time`, so echoing them back as an empty string is rejected with
`ERR_INVALID_PAYLOAD`, and the monitor links belong to `notification_ids` on the
monitor (`PUT /api/monitors/:id`), not to this endpoint. Numbers are numbers on
the wire too (`{"port": 587}`, never `{"port": "587"}`: the numeric inputs of the
UI emit strings, the API does not accept them).

Validation problems answer `400` with `ERR_NOTIFICATION_CONFIG_INVALID` and the
offending field in `message` (`config.webhook.url must start with http:// or
https://`); the UI shows that detail next to the translated sentence.

## 6. Status pages

| Method | Path | Notes |
|---|---|---|
| GET | `/api/status-pages` | list with monitor counts |
| POST | `/api/status-pages` | create (`monitor_ids` optional) |
| GET | `/api/status-pages/:id` | `{"page": {...}, "monitors": [links]}` |
| PUT | `/api/status-pages/:id` | update |
| DELETE | `/api/status-pages/:id` | delete |
| GET | `/api/status-pages/:id/monitors` | ordered selection |
| PUT | `/api/status-pages/:id/monitors` | `{"monitor_ids":[6,7,8]}` (order = display order) |
| GET | `/api/public/status/:slug` | **public** payload rendered by the page |
| GET | `/api/public/status/:slug/badge.svg` | **public** shields.io style badge |

## 7. Cluster

| Method | Path | Notes |
|---|---|---|
| GET | `/api/cluster/status` | node identity, role, key, settings, nodes |
| GET | `/api/cluster/nodes` | node registry |
| POST | `/api/cluster/join` | join/register (see `docs/clustering.md`) |
| POST | `/api/cluster/leave` | `{"node_id":"up-node-2"}` (empty = self) |
| GET | `/api/cluster/settings` | strategies |
| PUT | `/api/cluster/settings` | `{"failure_strategy":"QUORUM","node_unavailable_strategy":"MARK_DEGRADED","notification_sender":"ANY_WITH_LOCK"}` |
| GET | `/api/cluster/private-key` | `{"private_key":"..."}` |
| POST | `/api/cluster/private-key/regenerate` | rotates the key |
| POST | `/api/cluster/heartbeat` | refresh liveness, returns `{"online":[],"offline":[]}` |

## 8. Settings, tokens and IP rules

| Method | Path | Notes |
|---|---|---|
| GET | `/api/admin/settings` | locale/theme defaults + instance info |
| PUT | `/api/admin/settings` | `{"default_locale":"pt-BR","default_theme":"dark","app_name":"Up"}` -> `204` |
| GET | `/api/tokens` | API tokens (never the secret) |
| POST | `/api/tokens` | `{"name":"ci","scopes":["read"],"expires_in_days":30}` -> the plain token **once** |
| PUT | `/api/tokens/:id` | rename / rescope / change expiry |
| POST | `/api/tokens/:id/revoke` | `204` |
| DELETE | `/api/tokens/:id` | `204` |
| GET | `/api/ip-rules` | `{"rules":[],"client_ip":"...","bypassed":false}` |
| POST | `/api/ip-rules` | `{"cidr":"203.0.113.0/24","action":"allow","scope":"dashboard","note":"office"}` |
| PUT | `/api/ip-rules/:id` | update |
| DELETE | `/api/ip-rules/:id` | `204` |

## 9. Real time channel

`GET /api/ws` (WebSocket, session cookie). Client -> server messages:

```json
{"action":"subscribe","topics":["heartbeat","monitor.","notification.log","cluster."]}
{"action":"ping"}
```

Event types published by the server (envelope
`{"type":"...","payload":{...},"at":"..."}`):

| Type | Payload |
|---|---|
| `heartbeat` | `monitor_id`, `node_id`, `status`, `latency_ms`, `status_code`, `message`, `created_at` |
| `monitor.status` | `monitor_id`, `status`, `previous`, `changed`, `votes`, `message`, `latency_ms`, `checked_at` |
| `monitor.created` / `monitor.updated` / `monitor.deleted` | the monitor or `{id}` |
| `notification.created` / `notification.updated` / `notification.deleted` | the channel |
| `notification.log` | the delivery log entry |
| `cluster.nodes` / `cluster.settings` / `cluster.key` | cluster events |

The hub sends a ping every 25 s and drops clients that do not answer (60 s).

## 10. Public REST API (`/api/v1`)

Requires a bearer token and is documented in full in `docs/public-api.md`:

| Method | Path | Scope |
|---|---|---|
| GET | `/api/v1/status` | read |
| GET | `/api/v1/monitors` | read |
| GET | `/api/v1/monitors/:id` | read |
| GET | `/api/v1/monitors/:id/heartbeats` | read |
| GET | `/api/v1/monitors/:id/uptime` | read |
| POST | `/api/v1/monitors/:id/pause` / `resume` | write |
| GET | `/api/v1/status-pages/:slug` | read |

## 11. Error codes

The complete, stable catalogue (also present in `web/src/locales/*.json` under
`errors`):

| Code | HTTP | Meaning |
|---|---|---|
| `ERR_INTERNAL` | 500 | unexpected failure |
| `ERR_VALIDATION` | 400 | invalid field |
| `ERR_INVALID_PAYLOAD` | 400 | malformed JSON body |
| `ERR_NOT_FOUND` | 404 | unknown record |
| `ERR_ALREADY_EXISTS` | 409 | duplicate |
| `ERR_DATABASE` | 500 | database failure |
| `ERR_FORBIDDEN` | 403 | not allowed |
| `ERR_RATE_LIMITED` | 429 | too many requests |
| `ERR_ROUTE_NOT_FOUND` | 404 | unknown API route |
| `ERR_AUTH_REQUIRED` | 401 | no/invalid session |
| `ERR_AUTH_INVALID_CREDENTIALS` | 401 | wrong e-mail or password |
| `ERR_AUTH_RATE_LIMITED` | 429 | too many login attempts |
| `ERR_AUTH_DOMAIN_NOT_ALLOWED` | 403 | e-mail outside `KEYCLOAK_ACCOUNTS` |
| `ERR_RECAPTCHA_FAILED` / `ERR_RECAPTCHA_MISSING` | 403 | captcha rejected/absent |
| `ERR_AUTH_STATE_INVALID` | 400 | expired OIDC state |
| `ERR_AUTH_PROVIDER_ERROR` | 403 | identity provider error |
| `ERR_AUTH_METHOD_DISABLED` | 403 | endpoint not enabled for the configured method |
| `ERR_MONITOR_NOT_FOUND` | 404 | unknown monitor |
| `ERR_MONITOR_TYPE_INVALID` | 400 | unknown monitor type |
| `ERR_MONITOR_CONFIG_INVALID` | 400 | invalid type specific option |
| `ERR_NOTIFICATION_NOT_FOUND` | 404 | unknown channel |
| `ERR_NOTIFICATION_CONFIG_INVALID` | 400 | invalid channel configuration |
| `ERR_NOTIFICATION_SEND_FAILED` | 500 | delivery failed |
| `ERR_CLUSTER_DISABLED` | 403 | clustering disabled on this node |
| `ERR_CLUSTER_KEY_INVALID` | 403 | wrong cluster private key |
| `ERR_CLUSTER_JOIN_FAILED` | 400 | primary unreachable/rejected |
| `ERR_CLUSTER_PRIMARY_IS_SELF` | 409 | the reported primary is this node |
| `ERR_CLUSTER_STRATEGY_INVALID` | 400 | unknown strategy |
| `ERR_IP_BLOCKED` | 403 | blocked by an IP rule |
| `ERR_IP_RULE_INVALID` | 400 | invalid CIDR/action/scope |
| `ERR_TOKEN_REQUIRED` | 403 | missing bearer token |
| `ERR_TOKEN_INVALID` | 403 | unknown/mismatched token |
| `ERR_TOKEN_EXPIRED` | 403 | expired or revoked |
| `ERR_TOKEN_SCOPE_INSUFFICIENT` | 403 | scope missing |
| `ERR_STATUS_PAGE_NOT_FOUND` | 404 | unknown slug |
| `ERR_STATUS_PAGE_NOT_PUBLIC` | 403 | page hidden from anonymous visitors |
| `ERR_STATUS_PAGE_SLUG_TAKEN` | 409 | duplicate slug |

## 12. Conventions

- Timestamps are RFC3339 in UTC (`2026-09-22T18:00:00.123456789Z`).
- Heartbeat `status` is a string (`up`/`down`/`pending`/`maintenance`); the
  aggregated monitor status adds `degraded`/`unknown`.
- `notification_ids` is always present on a monitor (possibly empty).
- The public API masks credentials (`basic_pass`, `bearer_token` -> `***`) unless
  `?include_secrets=true` is passed.
- `Cache-Control: no-store` is set on public status/badge responses.
