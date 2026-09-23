# Up - Monitors

> A monitor is a probe definition: what to check, how often, how patient to be
> and which channels to alert. The scheduler runs one goroutine per active
> monitor; the cluster service aggregates the results of every node.

## 1. Common fields

| Field | Default | Range | Notes |
|---|---|---|---|
| `name` | - | 1..200 chars | required, shown everywhere |
| `type` | `http` | http, keyword, tcp, dns | selects the checker |
| `active` | `true` | - | paused monitors keep their history but are not executed |
| `description` | - | 500 chars | free text |
| `interval_seconds` | `60` | 5..86400 | ticker of the worker |
| `retries` | `0` | 0..50 | extra attempts after a failure |
| `retries_interval_seconds` | `60` | 1..3600 | delay between attempts |
| `timeout_seconds` | `10` | 1..300 | budget of a single attempt |
| `resend_interval_seconds` | `0` | >= 0 | 0 = notify only on transitions |
| `upside_down` | `false` | - | inverts up/down (a rule that must keep blocking) |
| `run_on` | `all` | all, primary, node | which cluster nodes execute it |
| `node_id` | - | - | required when `run_on=node` |
| `tags` | - | 255 chars | comma separated, used by filters and status pages |

### Retries and the `pending` status

A failing probe is retried up to `retries` times, waiting
`retries_interval_seconds` between attempts. The heartbeat of a retried check is
stored with `important=false`, so uptime statistics and notifications only react
to the definitive verdict. `retries=0` means "report the first failure".

### Re-notification

- `resend_interval_seconds=0`: an alert is sent on each transition
  (up -> down, down -> up).
- `> 0`: while the aggregated status stays `down`/`degraded`, the alert is
  repeated every N seconds (`monitor_states.notified_at` is the reference, so a
  cluster repeats it only once per window).

## 2. HTTP(s) and HTTP(s) Keyword

| Field | Values | Notes |
|---|---|---|
| `url` | absolute http(s) URL | required |
| `method` | GET POST PUT PATCH DELETE HEAD OPTIONS | default GET |
| `encoding` | json, form, xml, raw | `Content-Type`: `application/json`, `application/x-www-form-urlencoded`, `application/xml`, `text/plain` |
| `body` | string | sent verbatim (for `form` it is URL-encoded) |
| `headers[]` | list of `{key,value}` | dynamic key/value editor in the UI |
| `auth_type` | none, basic, bearer | basic uses `basic_user`/`basic_pass`, bearer uses `bearer_token` |
| `ignore_tls` | bool | skips certificate verification |
| `max_redirects` | 0..20 | 0 = no redirect accepted, default 10 |
| `accepted_status_codes` | ranges | `200-299` (default), `200-299,301,404` |
| `keyword` | string | **keyword type only**, required |
| `invert_keyword` | bool | the keyword must be **absent** |
| `case_sensitive` | bool | default insensitive |

Behaviour:

- A response whose status is outside `accepted_status_codes` is **down** and the
  message shows the code, the reason phrase and the accepted list.
- Keyword matching runs over the first 512 KiB of the body.
- Network errors are normalised into a short message (`connection refused`,
  `timeout exceeded`, `DNS resolution failed`, `TLS certificate error`).
- `upside_down=true` inverts the final status (a keyword that must NOT appear).

## 3. TCP

| Field | Notes |
|---|---|
| `host` | hostname or IP (required) |
| `port` | 1..65535 (required) |
| `send` | optional payload written after the connect |
| `expect` | optional substring that must appear in the answer |

The monitor is **up** when the TCP handshake succeeds (plus the optional
send/expect exchange). Latency is the time to establish the connection.

## 4. DNS

| Field | Notes |
|---|---|
| `hostname` | name to resolve (required) |
| `resolver_server` | `1.1.1.1`, `8.8.8.8:53`, ... (default `1.1.1.1`) |
| `record_type` | A, AAAA, CNAME, MX, TXT, NS, SOA |
| `expected_value` | substring compared (case-insensitive) against the answers |
| `invert_check` | inverts the match |

The query is sent with the `miekg/dns` client. Inverted semantics:

| `expected_value` | `invert_check` | Up when |
|---|---|---|
| set | false | the value is present |
| set | true | the value is **absent** |
| empty | false | the name resolves with the requested record type |
| empty | true | the name does **not** resolve (detect unwanted records) |

## 5. Creating a monitor through the API

```bash
curl -b cookies.txt -X POST http://localhost:3000/api/monitors \
  -H 'Content-Type: application/json' -d '{
    "name": "API health",
    "type": "http",
    "active": true,
    "interval_seconds": 30,
    "retries": 2,
    "retries_interval_seconds": 15,
    "timeout_seconds": 5,
    "tags": "production,api",
    "run_on": "all",
    "config": {
      "url": "https://api.example.com/health",
      "method": "GET",
      "headers": [{"key": "X-Tenant", "value": "acme"}],
      "auth_type": "bearer",
      "bearer_token": "secret",
      "accepted_status_codes": "200-299",
      "max_redirects": 3,
      "ignore_tls": false
    },
    "notification_ids": [1, 2]
  }'
```

Keyword example:

```json
{
  "name": "Login page",
  "type": "keyword",
  "config": {
    "url": "https://app.example.com/login",
    "method": "GET",
    "keyword": "Sign in",
    "invert_keyword": false,
    "case_sensitive": false
  }
}
```

TCP and DNS example:

```json
{ "name": "MariaDB", "type": "tcp", "config": { "host": "db.internal", "port": 3306, "expect": "" } }
```

```json
{
  "name": "example.com A",
  "type": "dns",
  "config": { "hostname": "example.com", "resolver_server": "1.8.8.8", "record_type": "A", "expected_value": "93.184" }
}
```

`notification_ids` replaces the links on every update; omit the field to keep the
current links, send `[]` to clear them.

## 6. Other operations

| Action | Endpoint |
|---|---|
| Pause / resume | `POST /api/monitors/:id/pause` / `.../resume` |
| Immediate check | `POST /api/monitors/:id/check` (202, queued) |
| Heartbeats | `GET /api/monitors/:id/heartbeats?hours=24&limit=500` |
| Statistics + bars | `GET /api/monitors/:id/stats?hours=24&limit=60` |
| Clone | `POST /api/monitors/:id/clone` (see below) |
| Delete | `DELETE /api/monitors/:id` (removes heartbeats, state, links) |

### Clone

`POST /api/monitors/:id/clone` with an optional
`{"name": "...", "copy_notifications": true, "copy_groups": true}` body. Without
a name the copy is `<name> (copy)` (and `(2)`, `(3)`… when that one is taken).

What is copied: the type, every configuration field, the scheduling options and
`run_on`, plus the notification links and the groups when asked. What is **not**
copied: the heartbeat history and the aggregated state, so a copy starts with a
clean history and never inherits an old incident.

## 7. Groups

Groups are named collections of monitors (Admin > Monitor groups). They exist to
organise a large monitor list, to filter it (`GET /api/monitors?group_id=12`) and
— from the status page side — to publish a whole set of monitors at once: adding a
monitor to a group makes it appear on every page that includes the group.

| Action | Endpoint |
|---|---|
| List (with `monitor_ids` and `monitor_count`) | `GET /api/monitor-groups` |
| Create / update | `POST /api/monitor-groups` · `PUT /api/monitor-groups/:id` |
| Replace the members | `PUT /api/monitor-groups/:id/monitors` |
| Delete (the monitors stay) | `DELETE /api/monitor-groups/:id` |
| Clone | `POST /api/monitor-groups/:id/clone` |

Rules that matter in practice:

- A monitor belongs to **any number** of groups. On the monitor payload
  `group_ids` follows the same contract as `notification_ids`: omitted keeps the
  current groups, `[]` clears them.
- Group names are unique (`409 ERR_MONITOR_GROUP_INVALID`); the ids in
  `monitor_ids`/`group_ids` are validated, so a typo answers
  `400 ERR_MONITOR_GROUP_INVALID` instead of creating a dangling link.
- Deleting a monitor removes its memberships; deleting a group keeps the
  monitors (it is a hard delete, so the name is free again immediately).
- The clone is **shallow by default** (an empty group with the same settings).
  With `deep: true` every monitor inside is cloned too, each one with its
  `(copy)` name and, with `copy_links`, its channels and groups.

## 8. Templates

A template is a monitor without a target: the probe type and its options plus the
defaults (interval, timeout, retries, re-notification, `run_on`, tags, channels
and groups). It exists for two jobs:

1. **Add monitors in bulk** (Admin > Monitors > *Add in bulk*): paste one monitor
   per line and pick the template. The server parses the paste, validates every
   line and reports what it did, line by line.
2. **Bulk edit** (Admin > Monitors > *Apply template*): overwrite the fields of
   many monitors at once, with a preview of the diff.

`internal/services/monitor_template*.go` implements both; the bulk edit **never
touches the target** of a monitor even when the probe options are applied, so
applying a template to 40 monitors cannot repoint them at the same address.

### The bulk text format

```
name,target[,type,keyword,tags,interval]
```

| Column | Notes |
|---|---|
| `name` | monitor name (required) |
| `target` | URL (`http://`/`https://` required), `host` or `host:port` |
| `type` | overrides the template type (`http`, `keyword`, `tcp`, `dns`) |
| `keyword` | required when the row is a keyword monitor |
| `tags` | overrides the template tags |
| `interval` | overrides the template interval (seconds, minimum 5) |

What the importer does for you:

- the delimiter is detected (comma, semicolon or tab, so a spreadsheet paste
  works) and quoted cells are supported;
- a header row (`name,url`) is ignored, `#` starts a comment and blank lines are
  skipped;
- a row with a missing name, a missing target, an unknown type or a `tcp` target
  without a port is reported as **invalid** with the reason and its line number;
- a row whose name or target already exists — in the database **or earlier in the
  same paste** — is reported as a **duplicate** and skipped;
- a dry run (`dry_run: true`) returns the same report without writing anything,
  which is what the dialog shows while you type;
- 500 rows maximum per request.

The target mapping per type is in `monitorFromBulkRow`: `url` for HTTP and
Keyword, `host`/`port` for TCP (the template port is the fallback) and `hostname`
for DNS.

## 9. Upside down monitors

`upside_down=true` swaps `up` and `down` **after** the probe, with a
`upside down:` prefix in the message. Typical use: an IP that must stay blocked
(a TCP monitor to a port that must remain closed) or a DNS name that must not
exist.

## 10. How the UI is wired

| Element | File |
|---|---|
| Form (all four types) | `web/src/components/monitors/MonitorForm.vue` |
| Card with status/uptime/bars | `web/src/components/monitors/MonitorCard.vue` |
| Status badge | `web/src/components/monitors/StatusBadge.vue` |
| Heartbeat bars | `web/src/components/monitors/HeartbeatBars.vue` (`HeartbeatBar.vue`) |
| Detail page (stats + events + votes) | `web/src/views/MonitorDetailView.vue` |
| Table listing | `web/src/views/admin/AdminMonitorsView.vue` |

All texts come from `web/src/locales/*.json` (`monitor.*`, `monitorDetail.*`).
