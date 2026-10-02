# Up - Monitors

> A monitor is a probe definition: what to check, how often, how patient to be
> and which channels to alert. The scheduler runs one goroutine per active
> monitor; the cluster service aggregates the results of every node.

## 1. Common fields

| Field | Default | Range | Notes |
|---|---|---|---|
| `name` | - | 1..200 chars | required, shown everywhere |
| `type` | `http` | http, keyword, tcp, dns, ssl | selects the checker |
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
| `tags` | - | 255 chars | comma separated, used by filters, status pages and tag scopes |

### Retries and the `pending` status

A failing probe is retried up to `retries` times, waiting
`retries_interval_seconds` between attempts. The heartbeat of a retried check is
stored with `important=false`, so uptime statistics and notifications only react
to the definitive verdict. `retries=0` means "report the first failure".

### Address family (`config.ip_family`)

A target usually has an address in both families, and the two paths are not
equivalent: a host whose service on IPv6 is dead looks healthy forever when every
check dials whichever family the dialer happened to pick. The field picks between
that pre-existing behaviour and an explicit family, so the choice is visible in the
monitor instead of being a side effect of the resolver.

| `config.ip_family` | Behavior |
|---|---|
| `auto` (default) | no preference: the name is resolved and the dialer tries the addresses with happy eyeballs - IPv6 first when the name has an AAAA record and the host has a usable IPv6 route, the other family 300 ms later. The probe is up as soon as one family works, so **a dead family is not reported**; this is the pre-feature behaviour of every monitor, and what a monitor stored before the field existed keeps |
| `alternate` | every execution flips between IPv4 and IPv6, so both paths are tested. The family is only insisted on when the target really has an address in it, so a single stack target keeps working on every execution. This is the mode that reports a dual stack host whose second path is dead |
| `ipv4` / `ipv6` | a hard pin: the probe only ever uses that family, and it fails when the target has no address in it |

- **One execution = one family.** With `alternate`, the retries of a check re-test
  the path that just failed (a retry cannot hide a dead family behind the working
  one) and the next execution rotates.
- The rotation lives in the worker and is seeded from the monitor id (odd ids
  start on IPv6), so a restart re-seeds it — it is deliberately in memory, like
  the ticker, and never reaches another node.
- `auto` is the default for monitors **and** templates, and every type reads the
  field (HTTP, Keyword, TCP, DNS and SSL), so it survives a type switch.
- A dual stack target whose IPv6 path is broken is reported **down** on the
  executions that test it only with `alternate`: that is the point of the
  rotation. With the default (`auto`) the working family answers and the monitor
  keeps looking up, so choose `alternate` when both paths must be proven.
- The probe may add one resolution step: with a family to insist on, the name is
  resolved first so the chosen address can be dialed as is. Nothing changes for
  `auto`.
- A failed probe names the family it used (`connection refused (tcp6)`,
  `dial tcp6 ...: no suitable address found`), so the half of the heartbeats that
  fail says which path is broken. An unknown `config.ip_family` answers
  `400 ERR_MONITOR_CONFIG_INVALID`; an omitted one keeps `auto`.

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
| `cache_buster` | bool | appends a fresh `up_cachebuster=<random>` to every request |
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
- `cache_buster=true` adds a randomly generated `up_cachebuster` parameter to
  every request, so a cache, a CDN or an in-between proxy always asks the
  origin. The parameter name is `up_cachebuster` and its value is regenerated on
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
    "notification_ids": [1, 2],
    "group_id": 1
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

```json
{
  "name": "SMTPS",
  "type": "ssl",
  "config": { "host": "mail.example.com", "port": 465, "server_name": "mail.example.com" }
}
```

`notification_ids` replaces the links on every update; omit the field to keep the
current links, send `[]` to clear them.

`group_id` is the group of the monitor and follows the same contract: omit the
field to keep the current group, send `0` to move the monitor to no group. The
payload answers with `group_id` (the id) and `group_name` (the name), and the
listing filters with `?group_id=`.

## 6. Other operations

| Action | Endpoint |
|---|---|
| Pause / resume | `POST /api/monitors/:id/pause` / `.../resume` |
| Immediate check | `POST /api/monitors/:id/check` (202, queued) |
| Heartbeats | `GET /api/monitors/:id/heartbeats?hours=24&limit=500` |
| Statistics + bars | `GET /api/monitors/:id/stats?hours=24&limit=60` |
| Clone | `POST /api/monitors/:id/clone` (see below) |
| Tags in use | `GET /api/monitors/tags?type=http` |
| Tags in bulk | `POST /api/monitors/bulk/tags` (see below) |
| Delete | `DELETE /api/monitors/:id` (removes heartbeats, state, links) |

### Tags

The tags of a monitor are one comma separated column (`255` characters), not a
join table: they travel with the row, they are typed in one box and the listing
filter is a plain substring match (`?tag=`, `LIKE %term%`). Three rules make that
list usable as a *selection* (a bulk tag operation, the tag scope of a template)
instead of a decoration:

- Every write **canonicalizes** the list (`models.CleanTags`, called by the
  monitor validation): trimmed, de-duplicated case insensitively, order
  preserved, first spelling wins. `"prod, prod"` is stored as `"prod"` and a
  monitor never compares differently from one save to the next. A list longer
  than the column is a `400` with a message instead of a driver error.
- A **selection** matches whole tags, case insensitively
  (`models.HasAnyTag`): a scope on `prod` never selects `production`, which the
  substring filter of the listing would.
- `GET /api/monitors/tags` answers the tags in use with the number of monitors
  carrying each of them (most used first, then alphabetical, `Ops` and `ops`
  reported once). It is the vocabulary the tag pickers offer, because the row
  column cannot be turned into a `SELECT DISTINCT`.

`POST /api/monitors/bulk/tags` adds and removes tags on a selection of monitors:
`{monitor_ids?, group_ids?, tag?, add?, remove?, dry_run?}`. The three selectors
are a **union** of what is ticked, the monitors of those groups and the monitors
carrying that tag (one of them is required: an empty selection is never
"everything", so a request that forgot a field cannot retag the installation).
Naming a tag is what makes a **rename** possible — `tag: "prod"`, `remove:
["prod"]`, `add: ["production"]` retags every monitor that carried `prod`, and
nothing else. The answer is
`{selected, updated, unchanged, failed, dry_run, changes: [{monitor_id, name,
tags_before, tags_after, status, error?}]}`: a row that already carries exactly
those tags is **not rewritten** (its revision does not move), a row that would
overflow the column fails alone with a message, and `dry_run: true` returns the
same before/after per row without writing. Every written row goes through the
ordinary monitor write path, so the revision advances, the cluster is told and
nothing else in the row (target, group, channels, template link) moves.

### Clone

`POST /api/monitors/:id/clone` with an optional
`{"name": "...", "copy_notifications": true, "copy_group": true}` body. Without
a name the copy is `<name> (copy)` (and `(2)`, `(3)`… when that one is taken).

What is copied: the type, every configuration field, the scheduling options and
`run_on`, plus the notification links and the group when asked. What is **not**
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

- A monitor belongs to **one** group. On the monitor payload `group_id` follows the
  same contract as `notification_ids`: omitted keeps the current group, `0` clears
  it, and an id moves the monitor into that group.
- Adding a monitor to a group from the group side (`PUT
  /api/monitor-groups/:id/monitors`) MOVES it out of the group it was in: a monitor
  claimed by two groups appeared twice on a status page that included both, which
  is what the single row per monitor in `monitor_group_members` prevents.
- Group names are unique (`409 ERR_MONITOR_GROUP_INVALID`); the ids in
  `monitor_ids` are validated, so a typo answers
  `400 ERR_MONITOR_GROUP_INVALID` instead of creating a dangling link.
- Deleting a monitor removes its memberships; deleting a group keeps the
  monitors (it is a hard delete, so the name is free again immediately).
- The clone is **shallow by default** (an empty group with the same settings).
  With `deep: true` every monitor inside is cloned too, each one with its
  `(copy)` name and, with `copy_links`, its channels. The monitor joins the new
  group instead of the group it was cloned from.

## 8. Templates

A template is a monitor without a target: the probe type and its options plus the
defaults (interval, timeout, retries, re-notification, `run_on` / `run_on_nodes`,
channels and the certificate/domain watches). The **groups and the tags are never
part of a template**: two monitors can follow the same template and live in
different groups with different tags. A template exists for three jobs:

1. **Add monitors in bulk** (Dashboard > *Add in bulk*): paste one monitor
   per line and pick the template. The server parses the paste, validates every
   line and reports what it did, line by line. The created monitors **follow** the
   template.
2. **Bulk edit** (Dashboard > *Apply template*): overwrite the fields of
   many monitors at once, with a preview of the diff.
3. **Be followed** by the monitors linked to it (see below).

Every probe type is available as a template, `ssl` included: that is what turns
"the same certificate check for 40 mail servers" into a single blueprint. The
certificate switches only exist for the types that can read a certificate
(`http`, `keyword`, `ssl`), so the template dialog hides them for `tcp`/`dns` and
the API rejects a `cert_watch` the type cannot honour
(`400 ERR_MONITOR_TEMPLATE_INVALID`) — the same rule a monitor follows.
`run_on=node` requires `node_id` and `run_on=some` requires `run_on_nodes`, again
like a monitor: a template must not describe a monitor the API would refuse to
create.

`internal/services/monitor_template*.go` implements all three; the bulk edit
**never touches the target** of a monitor even when the probe options are applied,
so applying a template to 40 monitors cannot repoint them at the same address.

**Authentication is never part of a template.** `auth_type`, `basic_user`,
`basic_pass` and `bearer_token` belong to the monitor: the template dialog hides
them, the API strips them from whatever a client sends
(`models.MonitorConfig.WithoutAuth`, on the create, the update and the incoming
synchronisation) and the apply path keeps the credentials of the monitor it
writes. Two monitors can therefore follow the same template against the same host
with different users — or with no authentication at all — and editing the
template never resets them. A template stored by an older release had its
credentials removed at boot (`database.BackfillTemplateAuth`, idempotent, run by
every node on its own copy).

### Following a template

A monitor can be **linked** to a template (`template_uuid`): the monitor form has a
*Template* selector, the monitors created by the bulk importer are linked
automatically, and both the monitors table and the monitor page show the name of
the template a monitor follows.

- Editing a template **pushes its defaults to every linked monitor**; only the
  monitors whose values actually differ are written, and the push is controlled by
  the template's *Update the linked monitors when this template changes* switch
  (on by default).
- The link stores the template **uuid**, never its numeric id, because a monitor
  row is synchronised between the cluster nodes
  ([clustering-modes.md](clustering-modes.md)).
- The type must match: a template never reshapes a monitor of another type, and a
  link whose template disappeared is cleared on the next save of that monitor.
- **Groups and tags are never pushed**, and a template that lists no notification
  channel never clears the channels of its monitors.
- Deleting a template clears the links: the monitors keep working, they simply stop
  following it.
- **Link monitors to this template** (the link icon on the templates page) attaches
  the monitors in scope and applies the defaults, after a dry-run preview. The
  scope is one of three: **every monitor of the template type**, the monitors of
  one or more **selected groups**, or the monitors carrying one or more **selected
  tags** (`scope: {kind: type|groups|tags, group_uuids?, tags?}` in the request).
  That is how an existing installation is gathered under a newly created template,
  or only the part of it that belongs to a team or to a tag. A group with no member
  is an empty scope, never "everything", and a tag matches a **whole tag**, case
  insensitively (`prod` never selects `production`).
- The scope of a run is **remembered on the template** (`link_scope`): re-opening
  the dialog shows the selection of the last run instead of falling back to
  "everything of this type", the templates table shows it in its *Scope* column,
  and the row action re-applies it. Groups are stored as **uuids** and never as
  local ids, because the template is synchronised between the cluster nodes, where
  the ids differ and the uuids do not. A scope whose groups were deleted elsewhere
  is pruned of the missing ones; a scope that names no group that exists is
  refused (`400`) instead of running over the whole type. The scope is also
  **preserved** by an edit that does not carry one (the template dialog does not
  send it), so only a link run replaces it.
- A run with an outside (groups, tags) is **authoritative**: the selection becomes
  the scope of the template, so the monitors that follow it from outside the
  selection **stop following it**. The preview and the answer report them as
  `unlinked`, and the dialog words both the warning and the counts for it, with a
  call-out when the selection resolves to **no monitor at all** while followers
  exist. This is what makes the choice mean something over time: a template trimmed
  to one team's group stops pushing its defaults into the monitors of another,
  while an additive run would simply link them all back on the next click.
- A detach is a **normal monitor edit**, not a hidden `UPDATE`: the monitor keeps
  its row, its target, its groups and its tags (only `template_uuid` is cleared),
  its revision advances so the cluster propagates the change, and a follower whose
  row no longer validates keeps following the template (the failure is logged and
  never counted as a release) instead of failing the whole run. The scope is the
  type of the template, so no monitor of another type is ever affected, and a run
  over **every monitor of the type** has no outside and detaches nobody.

### The bulk text format

```
name,target[,type,keyword,tags,interval]
```

| Column | Notes |
|---|---|
| `name` | monitor name (required) |
| `target` | URL (`http://`/`https://` required), `host` or `host:port` |
| `type` | overrides the template type: any of `http`, `keyword`, `tcp`, `dns`, `ssl` |
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
- 2000 rows maximum per request. The ceiling bounds one paste (memory, one
  transaction per row, one scheduler hand-off): it is not a data import pipeline.
  A larger import must be split, or the limit raised together with a batched
  insert and a streaming parser.

The target mapping per type is in `monitorFromBulkRow`: `url` for HTTP and
Keyword, `host`/`port` for TCP and SSL (the port of the template is the fallback,
`443` for SSL when the template gives none) and `hostname` for DNS.

A row whose `type` column **differs from the type of the template** is created
with that probe type and keeps the scheduling defaults and the channels of the
template, but it does **not** follow it: a template never describes another probe,
so the link is dropped instead of failing the row (the same rule the monitor form
applies when the type changes, and `MonitorService.Validate` enforces). The probe
options of the template type are pruned from the created monitor, and the
certificate/domain switches are kept only if the new type can honour them.

## 9. Certificates

Three monitor types can read a TLS certificate: `ssl` (the certificate *is* the
probe) and `http`/`keyword` when the target is https (the certificate is a side
effect of the handshake). Two switches decide what happens with it:

| Switch | Effect |
|---|---|
| `cert_watch` | the probe captures the certificate and it is shown in the UI (validity badge, issuer, exact expiry) |
| `cert_notify` | the certificate events reach the notification channels (requires `cert_watch`) |

The validity badge appears on the dashboard card, the monitor detail page and the
admin monitors table (sortable by days left), and on a public status page only
when that page has `show_cert_expiry` on — the domain badge of the same monitor
follows its own `show_domain_expiry` switch (`docs/status-pages.md`).

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
  registries are not queried once per node;
- *Admin > TLD/SSL expiration* lists that worklist and can refresh a single
  target without waiting for the daily run (see §10).

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
   date at all. The date belongs to the **registrable domain**, not to the monitor
   that typed it: saving it mirrors the value to every monitor of the same domain
   (`app.example.com` and `www.example.com` share it), and clearing it clears the
   siblings. A domain is therefore always ONE row in the expiry worklist, marked
   `manual date`, whichever of its monitors carries the date. Two properties make
   the "one value per domain" rule hold in practice:

   - the calendar is **independent of the watch switch**. `domain_watch` decides
     what is looked up and what is notified, never what is remembered: turning
     the watch off — or editing a monitor that never watched — used to clear the
     date for the whole domain, and no longer does. A monitor whose watch is off
     still shows the date of its domain;
   - the date is applied to the stored observation **as soon as it is saved**
     (and at boot, before the daily pass). The badge shows the real date
     immediately, instead of keeping an older `unsupported` status — the
     *no parser* badge — until the next daily run.
2. **RDAP** — the IANA bootstrap (`https://data.iana.org/rdap/dns.json`) says
   whether the TLD has RDAP; when it does, the registry answers the `expiration`
   event (and the registrar) on `<bootstrap-base>/domain/<name>` (the bootstrap
   publishes base URLs, so Up appends the `domain/` resource path itself).
3. **WHOIS** — a port 43 query, parsed by the **per-TLD rule** configured in
   *Admin > TLD/SSL expiration* (`whois_parsers`): a regular expression whose
   first capture group holds the date, plus the date layouts the registry uses.
   Up ships a built-in table of the TLDs without RDAP whose registry does publish
   the expiration (`.io`, `.co`, `.me`, `.it`, `.se`, `.ru`, `.mx`, `.tr`, …:
   every row was verified against the live registry), so those monitors work
   without any configuration. The query goes to the registry of the TLD — the
   rule's `server` field when it is set, otherwise the built-in table — and only
   asks `whois.iana.org` for the TLDs nothing else covers. A stale `server` in a
   rule therefore overrides a correct built-in entry, and a registry that only
   answers over IPv6 also needs an IPv6 route from inside the container — the
   host-network compose files described in *Network egress* of the README, since
   no address family setting can create one.

`date_layouts` is a `;` separated list of Go reference layouts and its
recommended value is **ISO 8601** (`2006-01-02T15:04:05Z07:00;2006-01-02`),
which is what a new rule starts with; the common registry shapes
(`2006-01-02 15:04:05`, `02/01/2006`, `02.01.2006`, `2006-Jan-02`, …) are tried
automatically after the configured ones.

Every rule can be created, edited and deleted in the admin page, which also
offers *reset parsers*: a confirmed action that erases the customizations and
restores the built-in table.

The status stored with the monitor tells the operator what happened:
`ok` (a date was found), `not_found` (the registry says the domain is free),
`unsupported` (no RDAP and no rule for the TLD — add one, or set the manual
date) and `error` (network, rate limit, or the rule did not match).

**Deduplication**: `app.example.com` and `www.example.com` both normalize to
`example.com`, so any number of monitors share one registry lookup per day. The
rate limit between two lookups (default `100 ms`) and the time of day are
configured in *Admin > TLD/SSL expiration*; a per-TLD `min_interval_ms` can slow
down one strict registry without slowing down the others.

### What the next run will look up

*Admin > TLD/SSL expiration* shows the **deduplicated worklist** of the job: one
row per unique target, with the monitors that share it, the last observation and
the days left, plus a per-row *check now*. The table is filterable (by target
type and by target/monitor name) and sortable on every column (type, target,
monitors, status, last check), like the monitors table. `GET
/api/admin/expiry/targets` returns exactly what `POST /api/admin/expiry/run`
would iterate (both are built from the same planner), and
`POST /api/admin/expiry/targets/refresh` with `{kind, target}` refreshes one row
through the same resolver, rate limit and reminder evaluation as the daily job —
so a manual check cannot produce a different observation than the scheduled one.

A row whose domain has a manual date (typed in any of its monitors) is marked
`manual date`: it is still listed (the operator must see it) but no network
lookup happens for it, and every monitor that watches the domain reports the date
as `ok`/`manual` right away. `kind` is `certificate` (key `host:port|sni`) or
`domain` (key is the registrable domain) — one row per domain, whatever its
monitors. A certificate target is **not** deduplicated by registrable domain: the
key carries the dialled host, the port and the SNI, so two subdomains served by
different certificates stay two rows (and two handshakes) on purpose.

## 11. The `ssl` monitor type

| Field | Notes |
|---|---|
| `host` | hostname or IP to dial (required) |
| `port` | 1..65535, default 443 |
| `server_name` | optional SNI for virtual hosts the IP alone does not identify |
| `ignore_tls` | accepts an unverified chain (the handshake still has to answer) |

The probe **is** the TLS handshake, so the monitor is `up` when the chain
verifies (plus the optional SNI) and `down` otherwise; the certificate captured
on the way is what feeds the badges and the reminders of §9.

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

## 12. Upside down monitors

`upside_down=true` swaps `up` and `down` **after** the probe, with a
`upside down:` prefix in the message. Typical use: an IP that must stay blocked
(a TCP monitor to a port that must remain closed) or a DNS name that must not
exist.

## 13. How the UI is wired

| Element | File |
|---|---|
| Form (all five types) | `web/src/components/monitors/MonitorForm.vue` |
| Templates (type aware defaults) | `web/src/views/admin/AdminMonitorTemplatesView.vue` |
| Bulk add / bulk edit dialogs | `web/src/components/monitors/BulkAddDialog.vue` (`ApplyTemplateDialog.vue`) |
| Bulk tag dialog | `web/src/components/monitors/BulkTagsDialog.vue` |
| Target fields per type | `web/src/components/monitors/MonitorConfigFields.vue` |
| Type aware payload pruning | `web/src/lib/monitor-config.ts` |
| Status badge | `web/src/components/monitors/StatusBadge.vue` |
| Shared monitors table (dashboard + admin) | `web/src/components/monitors/MonitorTable.vue` |
| Heartbeat bars | `web/src/components/monitors/HeartbeatBar.vue` (series) and `HeartbeatSparkline.vue` (bucketed column) |
| Detail page (stats + events + votes + expiry badges) | `web/src/views/MonitorDetailView.vue` |
| Table listing (sortable, expiry columns, filters, bulk/apply) | `web/src/views/DashboardView.vue` |
| Other sortable admin tables | `web/src/views/admin/AdminMonitorGroupsView.vue`, `AdminMonitorTemplatesView.vue`, `AdminStatusPagesView.vue` |
| Sortable header widget | `web/src/components/ui/SortHeader.vue` |
| Table sorting rules | `web/src/lib/sort.ts` (`lib/monitor-sort.ts` and `lib/table-sort.ts` for the remembered state) |
| Expiry badge colour/tooltip | `web/src/lib/expiry.ts` |
| Daily job + TLD rules + target list | `web/src/views/admin/AdminExpiryView.vue` |
| Public status page | `web/src/views/StatusPagePublicView.vue` |

Changing the type of a monitor clears the fields that do not belong to the
selected one: the probe options of the previous type (`config`), the certificate
switches when the new type cannot read a certificate, the domain switches and the
manual date when the watch is off, and a template link that points at a template
of another type. `web/src/lib/monitor-config.ts` owns that map (it mirrors the
`Validate` of every type on the Go side): the form may keep a hidden value while
the operator experiments with the type, it is dropped when the dialog is saved
and never reaches the API (which keeps rejecting an incoherent payload built by
another client).

The template dialog follows the same rule with its defaults
(`sanitizeTemplateDefaults` in the same module): the certificate section is
rendered only for the types that can read a certificate, the domain section for
the types that have a registrable target, and the hidden switches are dropped
before the save — a template never carries a switch its own monitors would be
rejected for.

The **link dialog** of the templates page (`AdminMonitorTemplatesView.vue`) is
opened on the scope stored on the template, offered as three radio cards (every
monitor of the type, some groups by uuid, some tags), and every change re-runs the
server side dry run: the counts, the released followers and the warning come from
the same code path that will write them, never from a rule re-implemented in the
browser. The tag picker reads its vocabulary from `GET /api/monitors/tags?type=`
(so it suggests tags that exist), still lets the operator type one that does not,
and the bulk tag dialog (`BulkTagsDialog.vue`, opened from the dashboard toolbar)
reuses the same read to offer the tags a rename can start from.

All texts come from `web/src/locales/*.json` (`monitor.*`, `monitorDetail.*`,
`certificate.*`, `domain.*`, `expiry.*`, `templates.*`, `bulkTags.*`).

### Sorting the monitors table

Every column of the dashboard monitors table except
*Actions* and *Heartbeat* is a sort button: name, type, groups, certificate,
domain, status, interval and uptime. Clicking a header sorts
ascending, clicking it again reverses the direction; the choice is remembered per
browser (`localStorage`, key `up.admin.monitors.sort`) and announced to screen
readers through `aria-sort`. The persistence lives in `web/src/lib/monitor-sort.ts`,
the comparators in `web/src/lib/sort.ts`.

### Sorting and filtering the other admin tables

The same header widget (`web/src/components/ui/SortHeader.vue`), the same rules
and the same remembered state (`web/src/lib/table-sort.ts`) are used by the other
three admin tables, with **one storage key per table**, so sorting the groups
never changes how the templates are listed:

| Table | Filter looks at | Sortable columns | Default | `localStorage` key |
|---|---|---|---|---|
| Monitor groups (`AdminMonitorGroupsView.vue`) | name, description | name, monitors, order | `order` ascending (the position given to each group, which is the order the API returns) | `up.admin.monitor-groups.sort` |
| Monitor templates (`AdminMonitorTemplatesView.vue`) | name, description, probe type | name, type, monitors, interval | `name` ascending | `up.admin.monitor-templates.sort` |
| Status pages (`AdminStatusPagesView.vue`) | title, slug, description | title, slug, visibility, monitors, groups | `title` ascending | `up.admin.status-pages.sort` |

The counts a column sorts by come from the API, never from a loop in the view:
the templates list counts the monitors that follow each template (the
`template_uuid` link) in **one** grouped query (`MonitorTemplateService`), a group
is counted from the members the service already had to read, and a status page
publishes `monitors_count` and `groups_count` (the explicit list plus the groups).
Every table also reports *shown of total* (`common.shownOfTotal`) next to the
filter and says so when nothing matches (`common.noMatch`) instead of rendering an
empty body. The behaviour itself is covered by `npm run e2e:table-sort`
(`web/scripts/e2e-table-sort.mjs`), which drives the three tables in a real
browser and asserts the columns, the storage keys and the two messages listed
here.

### Uptime period and the heartbeat column

The uptime percentage follows one window: `24`, `168` (7 d), `336` (14 d) or
`720` (30 d) hours. Admin > Settings holds the global value (it applies to the
dashboard, the table, the detail page and the compact heartbeat column); a public
status page can override it per page, and the monitor detail page offers a period
selector. The backend fills both `uptime`/`uptime_hours` (the selected window) and
the fixed `uptime_24h`/`uptime_7d`/`uptime_30d` columns of the public API, with a
single grouped query (`StatsService.History`).

The *Heartbeat* column draws a bucketed snapshot of the same window
(`StatsService.RecentBars`: one grouped query over the hourly rollups, bounded by
monitors x slots, never a query per monitor and never updated live), so the column
stays cheap even on a large installation. Every slot is a whole hour - one per
hour of the window plus the hour in progress - which is both what keeps the column
readable on a wide window and what lets the rollups answer it. An empty slot
(paused monitor, or a gap in the history) is drawn in the muted "no data" colour.

The rules (`web/src/lib/sort.ts`):

- rows **without** the value sink to the bottom in both directions: a monitor with
  no group, no certificate date or a domain whose lookup did not return a date is
  never mixed into the middle of the sorted values;
- `status` sorts by the attention a status deserves (down, degraded, pending,
  unknown, maintenance, up) and the **paused** monitors stay last either way;
- every comparison falls back to the name, so a re-render never reshuffles rows
  with equal values.
