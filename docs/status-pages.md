# Up - Status pages (public)

> A status page is a public, read-only view of a selection of monitors, with its
own slug, title, theme and footer. It is the shareable surface for end users,
> while the dashboard stays private.

## 1. Public URLs

| URL | Content |
|---|---|
| `https://up.example.com/status/<slug>` | the page (SPA route, anonymous) |
| `https://up.example.com/api/public/status/<slug>` | the JSON payload rendered by the page |
| `https://up.example.com/api/public/status/<slug>/badge.svg` | shields.io style badge |

Example API response:

```json
{
  "id": 1, "slug": "main", "title": "Up Status",
  "description": "Public status of the platform", "footer_text": "Powered by Up",
  "theme": "system", "is_public": true,
  "show_uptime": true, "show_charts": true, "show_tags": false,
  "overall_status": "up", "up_monitors": 4, "down_monitors": 0, "monitors_count": 4,
  "monitors": [
    {
      "id": 6, "name": "HTTP Health", "type": "http", "status": "up",
      "uptime_24h": 99.99, "last_latency_ms": 12, "last_check_at": "2026-09-22T18:00:00Z",
      "heartbeats": [{"status": "up", "latency_ms": 12, "created_at": "..."}],
      "votes": [{"node_name": "Primary Node", "status": "up", "online": true}]
    }
  ]
}
```

## 2. Page settings

| Field | Effect |
|---|---|
| `slug` | public identifier, lowercase letters/numbers/dashes (`my-status`); unique |
| `title`, `description`, `footer_text` | rendered as-is |
| `theme` | `system` (page follows the visitor), `light` or `dark` |
| `is_public` | `false` hides the page: anonymous visitors get `403 ERR_STATUS_PAGE_NOT_PUBLIC`, authenticated admins can still preview it |
| `show_uptime` | shows the 24 h uptime percentage per monitor |
| `show_charts` | shows the heartbeat bars |
| `show_tags` | shows the monitor tags |
| `custom_css` | injected into the public page (advanced) |
| `monitor_ids` order | display order (up to any number of monitors) |

## 3. Managing pages

Admin UI: **Admin > Status pages**.

| Action | Endpoint |
|---|---|
| Create | `POST /api/status-pages` (with optional `monitor_ids`) |
| List | `GET /api/status-pages` |
| Read | `GET /api/status-pages/:id` |
| Update | `PUT /api/status-pages/:id` |
| Delete | `DELETE /api/status-pages/:id` |
| Selection/order | `PUT /api/status-pages/:id/monitors` with `{"monitor_ids":[6,7,8]}` |

```bash
curl -b cookies.txt -X POST localhost:3000/api/status-pages \
  -H 'Content-Type: application/json' -d '{
    "slug": "main",
    "title": "Up Status",
    "description": "Live status of our services",
    "footer_text": "Subscribe to incident updates at status.example.com",
    "theme": "system",
    "is_public": true,
    "show_uptime": true,
    "show_charts": true,
    "monitor_ids": [6, 7, 8]
  }'
```

## 4. Badge

```markdown
![status](https://up.example.com/api/public/status/main/badge.svg)
```

Colours follow the overall status: green (`up`), red (`down`), amber
(`degraded`), grey (`pending`/`unknown`). The SVG is generated in
`internal/services/statuspage_badge.go` (no external service involved).

## 5. Freshness and aggregation

- The public page polls `/api/public/status/:slug` every 30 s (the anonymous
  surface has no WebSocket session).
- The `overall_status` of the page is `down` if any monitor is down, `degraded`
  if any is degraded, `pending` when no monitor has data yet and `up` otherwise.
- Monitor statuses are the **aggregated** ones (cluster strategies applied), so a
  status page reflects the same truth as the dashboard.
- Responses are sent with `Cache-Control: no-store`; put a CDN in front only if
  you accept a stale status.

## 6. Protection

| Control | How |
|---|---|
| Rate limit | `SECURITY_PUBLIC_RATE_LIMIT` (default 240/min per IP) |
| IP rules | scope `public` in Admin > Security (`docs/security.md`) |
| Hidden page | `is_public=false` (admins can preview) |
| No secrets | the public payload exposes no credentials, no configuration and no error details |

## 7. Limitations and extensions

- No incident timeline/announcements yet: everything shown is derived from the
  heartbeats.
- Monitor groups (`status_page_monitors.group_name`) exist in the schema; the UI
  currently renders a flat, ordered list.
- The 90-day uptime figure is 30 days today (`uptime_30d`); the payload is fixed
  at 24 h/7 d/30 d windows.
- To add a custom domain per page, place a reverse proxy rule in front
  (`docs/reverse-proxy.md`) and set `APP_URL` to the canonical public URL.
