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
| `run_on` | `all` | all, primary, node, some | which cluster nodes execute it (`some` = the subset in `run_on_nodes`) |
| `node_id` | - | - | required when `run_on=node` |
| `run_on_nodes` | - | - | comma separated node ids, required when `run_on=some` |
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
| `cache_buster` | bool | appends a fresh `uptime_kuma_cachebuster=<random>` to every request |
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
- `cache_buster=true` adds a randomly generated `uptime_kuma_cachebuster`
  parameter to every request, so a cache, a CDN or an in-between proxy always
  asks the origin. The parameter name is deliberately the same one Uptime Kuma
  uses (a cache rule written for it keeps working) and the value changes on
  every check. A query string already present in the URL is preserved next to
  it. HTTP and Keyword monitors share the option, the `ssl`, TCP and DNS types
  do not have it.

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
defaults (interval, timeout, retries, re-notification, `run_on` / `run_on_nodes`,
tags, channels and groups). It exists for two jobs:

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

## 9. Certificates

Three monitor types can read a TLS certificate: `ssl` (the certificate *is* the
probe) and `http`/`keyword` when the target is https (the certificate is a side
effect of the handshake). Two switches decide what happens with it:

| Switch | Effect |
|---|---|
| `cert_watch` | the probe captures the certificate and it is shown in the UI (validity badge, issuer, exact expiry) |
| `cert_notify` | the certificate events reach the notification channels (requires `cert_watch`) |

`cert_warn_days` is a **free form** list of days before expiry, e.g. `7,6,5,30`
(any numbers, any order; empty means `30,14,7,1`). The cadence is hybrid:

| Situation | What is sent |
|---|---|
| `days_left` equals a configured value | one `cert_expiring` when it is crossed |
| the process was offline and several values were crossed at once | **one** notification ("expires in 4 days") with every crossed value marked |
| `days_left` ≤ the smallest configured value | the reminder **repeats once a day** |
| the certificate is already expired | `cert_expired`, then the daily reminder until it is renewed |
| several nodes watching the same monitor | one notification: the day bucket of the lock table elects the sender (shared database) |

A monitor that is checked every minute does not send 1440 notifications: the
thresholds are remembered in `monitor_certificates.notified_days` and the daily
reminder is pinned to the day.

### How often the certificate is read

Certificates are **not** re-read on every probe any more. The stored row is
written at most once a day per target (and immediately when the certificate
actually changes, e.g. after a renewal), and the **daily expiry job** refreshes
every watched endpoint at the time configured in *Admin > TLD/SSL expiration*:

- the worklist is **deduplicated**: 30 monitors on `https://www.example.com` and
  12 on `app.example.com` produce a single handshake per unique
  `host:port:SNI`, and the observation is fanned out to every monitor;
- a monitor checked every 60 s therefore stops rewriting the same row 1440 times
  a day, while its badge and its reminders stay a day fresh;
- one node of a cluster runs the job (a day-bucket lock elects it), so the
  registries are not queried once per node.

The observation is stored within the column budgets: the subject alternative
names live in a `TEXT` column (a CDN certificate can list hundreds of them) and
a pathological list is trimmed from the tail with a warning in the log, so a
certificate is never silently missing — and with it its reminders — because of
its size.

## 10. Domain expiration

The registry counterpart of §9: the same thresholds, the same daily reminder,
the same notifications, but the target is the **registrable domain** (eTLD+1) of
the monitor instead of its TLS endpoint.

| Switch | Effect |
|---|---|
| `domain_watch` | the registrable domain of the target is resolved and watched |
| `domain_notify` | the domain events reach the notification channels (requires `domain_watch`) |

`domain_warn_days` behaves exactly like `cert_warn_days` (`30,14,7,1` by
default): every configured value alerts once when it is crossed, and below the
smallest one the reminder repeats **once a day**. The events are
`domain_expiring` and `domain_expired`.

### How the expiry date is found

1. **Manual date** — `domain_expires_at` in the monitor form (a calendar). When
   set it wins, and no network lookup runs. Use it for the TLDs that publish no
   date at all.
2. **RDAP** — the IANA bootstrap (`https://data.iana.org/rdap/dns.json`) says
   whether the TLD has RDAP; when it does, the registry answers the `expiration`
   event (and the registrar) on `<bootstrap-base>/domain/<name>` (the bootstrap
   publishes base URLs, so Up appends the `domain/` resource path itself).
3. **WHOIS** — a port 43 query, parsed by the **per-TLD rule** configured in
   *Admin > TLD/SSL expiration* (`whois_parsers`): a regular expression whose
   first capture group holds the date, plus the date layouts the registry uses.

TLDs such as `.io`, `.pt` or `.mx` have no RDAP at all: they stay
`unsupported` until a WHOIS parser for their registry is added or the manual date
is set.

The status stored with the monitor tells the operator what happened:
`ok` (a date was found), `not_found` (the registry says the domain is free),
`unsupported` (no RDAP and no rule for the TLD — add one, or set the manual
date) and `error` (network, rate limit, or the rule did not match).

**Deduplication**: `app.example.com` and `www.example.com` both normalize to
`example.com`, so any number of monitors share one registry lookup per day. The
rate limit between two lookups (default `100 ms`) and the time of day are
configured in *Admin > TLD/SSL expiration*; a per-TLD `min_interval_ms` can slow
down one strict registry without slowing down the others.

## 11. Upside down monitors

**When it expires**: a monitor that verifies TLS goes `down` on its own (the
handshake fails, so the normal down notification arrives), *unless*
`ignore_tls=true` — in that case the checks keep succeeding and `cert_expired` is
the only signal, which is why it exists. The certificate is captured even when the
verification fails, so the operator sees the real issuer and the negative days
left instead of a bare handshake error.

A `ssl` monitor dials `config.host`:`config.port` (default 443) and accepts an
optional `config.server_name` (SNI) for virtual hosts where the IP alone does not
identify the certificate.

### `ssl` versus `cert_watch` on an HTTP monitor

They are not alternatives: `cert_watch` is the shortcut for a target already
monitored over HTTP(s), while `ssl` is an independent probe.

|  | `cert_watch` on `http`/`keyword` | dedicated `ssl` type |
|---|---|---|
| What is probed | the URL (status codes, redirects, keyword) | a raw TLS handshake on `host:port` (default 443) |
| The certificate is | a side effect of the handshake the request already performs | the probe itself |
| An HTTP endpoint is required | yes | no |
| SNI | derived from the URL host | explicit `server_name` |
| Requests in the access log | one per interval | none |
| Up/down means | "the URL answered as configured" | "the TLS handshake verified" (an untrusted chain is `down`) |
| With `ignore_tls=true` | the checks keep succeeding, so `cert_expired` is the only signal | not applicable |

Use the **dedicated type** when:

1. the target does not speak HTTP — IMAPS 993, POP3S 995, SMTPS 465, LDAPS 636,
   MySQL/MariaDB with TLS 3306, MQTT 8883, or any custom port;
2. the certificate names a virtual host that differs from the address you dial
   (`server_name`);
3. the certificate must be watched even while the HTTP monitor is paused, or that
   monitor runs with `ignore_tls=true` and you still want "the chain verifies"
   as its own signal;
4. a broken certificate and a broken backend must be two separately routable
   alerts instead of one `down`.

For a normal `https://` site the HTTP flag is enough, and it avoids a second
monitor and a second request: that is exactly what the flag is for.

## 10. Upside down monitors

`upside_down=true` swaps `up` and `down` **after** the probe, with a
`upside down:` prefix in the message. Typical use: an IP that must stay blocked
(a TCP monitor to a port that must remain closed) or a DNS name that must not
exist.

## 12. How the UI is wired

| Element | File |
|---|---|
| Form (all four types) | `web/src/components/monitors/MonitorForm.vue` |
| Card with status/uptime/bars | `web/src/components/monitors/MonitorCard.vue` |
| Status badge | `web/src/components/monitors/StatusBadge.vue` |
| Heartbeat bars | `web/src/components/monitors/HeartbeatBars.vue` (`HeartbeatBar.vue`) |
| Detail page (stats + events + votes) | `web/src/views/MonitorDetailView.vue` |
| Table listing | `web/src/views/admin/AdminMonitorsView.vue` |

All texts come from `web/src/locales/*.json` (`monitor.*`, `monitorDetail.*`).
