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
| `POST https://up.example.com/api/public/status/<slug>/subscribe` | starts the double opt-in (`email`, `locale`, `recaptcha_token` when enabled) |
| `GET https://up.example.com/api/public/status/<slug>/confirm?token=…` | confirmation link from the e-mail (302 back to the page) |
| `GET https://up.example.com/api/public/status/<slug>/unsubscribe?token=…` | unsubscribe link from every e-mail (302 back to the page) |

Example API response:

```json
{
  "id": 1, "slug": "main", "title": "Up Status",
  "description": "Public status of the platform", "footer_text": "Powered by Up",
  "theme": "system", "is_public": true,
  "show_uptime": true, "show_charts": true, "show_tags": false,
  "show_cert_expiry": false, "show_domain_expiry": false,
  "uptime_window_hours": 0,
  "subscription_enabled": false, "subscription_smtp_uuid": "", "subscription_delay_minutes": 10,
  "subscription_available": false,
  "overall_status": "up", "up_monitors": 4, "down_monitors": 0, "monitors_count": 4,
  "monitors": [
    {
      "id": 6, "name": "HTTP Health", "type": "http", "status": "up",
      "uptime": 99.99, "uptime_hours": 24, "uptime_24h": 99.99,
      "last_latency_ms": 12, "last_check_at": "2026-09-22T18:00:00Z",
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
| `show_uptime` | shows the uptime percentage per monitor over the page period |
| `show_charts` | shows the heartbeat bars (same period) |
| `show_tags` | shows the monitor tags |
| `show_cert_expiry` | publishes the **certificate** days-left badge (`Certificate: 12 days`) of the monitors (default `false`) |
| `show_domain_expiry` | publishes the **domain registration** days-left badge (`Domain: 91 days`) of the monitors (default `false`); independent from `show_cert_expiry` |
| `uptime_window_hours` | period the uptime and the bars cover: `0` inherits the global one (Admin > Settings), otherwise `24`, `168`, `336` (14 d) or `720` (30 d) |
| `custom_css` | injected into the public page (advanced) |
| `subscription_enabled` | offers the opt-in incident e-mails to the visitors (default `false`) |
| `subscription_smtp_uuid` | SMTP channel that sends them, named by uuid so every cluster node finds the same channel |
| `subscription_delay_minutes` | how long a monitor must stay down before the first mail: `5` to `1440`, multiple of `5`, default `10` |
| `monitor_ids` order | display order (up to any number of monitors) |

## 3. Managing pages

Admin UI: **Admin > Status pages**. The table filters by title, slug and
description and sorts by any column (title, slug, visibility, displayed monitors,
displayed groups); the two counts come from the API (`monitors_count`,
`groups_count`). The sort widget and its remembered state are shared with the
other admin tables — see [monitors.md](monitors.md) §14.

| Action | Endpoint |
|---|---|
| Create | `POST /api/status-pages` (with optional `monitor_ids`) |
| List | `GET /api/status-pages` |
| Read | `GET /api/status-pages/:id` |
| Update | `PUT /api/status-pages/:id` |
| Delete | `DELETE /api/status-pages/:id` |
| Selection/order | `PUT /api/status-pages/:id/monitors` with `{"monitor_ids":[6,7,8]}` |
| Groups included | `PUT /api/status-pages/:id/groups` with `{"groups":[{"group_id":1}]}` |

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

## 3.1 Groups on a page

A page renders two things: the monitors picked one by one and the **groups**
linked to it (Admin > Status pages > Displayed groups). Because a group is a live
collection, adding a monitor to the group publishes it on every page that
includes the group — no page edit needed. The admin card shows both counters
(`Displayed monitors` counts the union, `Displayed groups` how many groups).

Rendering rules (`planStatusPage` in `internal/services/statuspage_groups.go`):

| Situation | Result |
|---|---|
| A monitor is both selected explicitly and inside a group | rendered **once**, in the explicit list |
| A monitor belongs to two linked groups | cannot happen: a monitor belongs to one group (the "already seen" guard keeps the page from rendering it twice, whatever the database holds) |
| A group linked to the page is empty | **no** section (an empty heading looks like a bug) |
| Monitors selected explicitly but in no group | last section, without a heading |
| `display_name` on the link | replaces the group name **on that page only** |

The public payload keeps `monitors` (the flat union, what the badge and the public
API have always used) and adds `groups` (the same list split into sections), so
the order of the sections and of the flat list always agree.

Both lists are picked in the page dialog (**Admin > Status pages > New/Edit page**)
with the same search-and-pick pair as the monitor group dialog (see
[monitors.md](monitors.md) §7): one search box filters the monitors (name, type,
tags) and the groups (name, description), a click appends the row to the list it
belongs to — `Displayed monitors` for the display order, `Displayed groups` for
the section order — and the remove button takes it out again. The two lists stay
separate because the page renders the explicit monitors first and the group
sections after them, so one interleaved list would promise an order the API does
not keep.

A monitor that a selected group already publishes is still listed by the search
but **not** clickable (`via <group>`): an explicit entry would only add a
redundant row that the renderer drops anyway. The reverse case — an explicit
monitor that later joins a linked group — is kept and marked `also in <group>`,
so nothing disappears silently; the operator can remove the explicit row to let
the group order win. A page with no groups still shows the group half of the
picker with a hint pointing at Admin > Monitor groups.

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

## 6. E-mail subscriptions

A page can offer visitors an opt-in e-mail subscription. It is off by default and
needs an **active SMTP channel**; the public form appears only when the payload
says `subscription_available: true` (switch on + channel present and active).

- **Double opt-in.** `POST /api/public/status/<slug>/subscribe` stores a pending
  row and mails a confirmation link valid for 7 days. Only a confirmed address
  receives incident mails. The endpoint answers the same way for a new, a pending
  and an existing address, so it cannot be used to enumerate subscribers.
- **One down and one up per incident.** A transition into `down`/`degraded` opens
  an incident per page+monitor. The down mail is scheduled after
  `subscription_delay_minutes`; if the monitor recovers first, the down mail is
  cancelled and only the recovery mail is sent. The unique
  `(incident, subscriber, kind)` key makes the rule hold under retries and
  concurrent scheduler passes.
- **Unsubscribe in every mail.** The link is a signed, deterministic token; the
  confirmation mail carries it too, so a confirmation message is never a dead
  end. A re-subscribe after unsubscribing starts a new double opt-in: an old
  confirmation link cannot reactivate the address.
- **Retries and election.** The scheduler runs every 30 s; a notice is claimed
  atomically, retried with exponential backoff (1, 2, 4, 8 minutes, at most five
  attempts), and only the node elected by the notification sender strategy sends
  it. Subscribers are a federated entity, so that node always has the row.
- **Admin.** The page row opens a paginated subscriber listing
  (`GET /api/status-pages/:id/subscribers`) with confirmed/pending/unsubscribed
  states; `DELETE /api/status-pages/:id/subscribers/:subscriber_id` removes one
  address and tombstones it on the peers.
- **Privacy.** The subscriber e-mail address replicates to every federated peer
  (docs/clustering-modes.md). Unsubscribed rows are retained so the operator sees
  who opted out and a later request cannot silently re-enable the address.

The subscribe endpoint has its own **5 requests/minute per IP** budget on top of
the public page limit. When the reCAPTCHA pair is configured
(Admin > Settings > **Login protection**, see `docs/authentication.md`), the form
must include a valid token (`ERR_RECAPTCHA_MISSING` / `ERR_RECAPTCHA_FAILED`).

## 7. Protection

| Control | How |
|---|---|
| Rate limit | `SECURITY_PUBLIC_RATE_LIMIT` (default 240/min per IP) |
| IP rules | scope `public` in Admin > Security (`docs/security.md`) |
| Hidden page | `is_public=false` (admins can preview) |
| No secrets | the public payload exposes no credentials and no error details; the subscription SMTP channel is reduced to the `subscription_available` boolean, never its uuid |
| Expiry is opt-in, one switch per badge | `show_cert_expiry`/`show_domain_expiry` strip the `certificate`/`domain` object they own from the payload (`PublicPayload`), so a page that only asks for the certificate does not publish the domain with it. Both default to `false` because an anonymous page has no reason to advertise how old the domain is |

## 8. Limitations and extensions

- No incident timeline/announcements yet: everything shown is derived from the
  heartbeats.
- Monitor groups (`status_page_monitors.group_name`) exist in the schema; the UI
  currently renders a flat, ordered list.
- The uptime figure follows the page period (`uptime_window_hours`, or the
  global one when it is `0`); the monitor payload also keeps the fixed
  `uptime_24h`/`uptime_7d`/`uptime_30d` columns of the public API.
- To add a custom domain per page, place a reverse proxy rule in front
  (`docs/reverse-proxy.md`) and set `APP_URL` to the canonical public URL.
