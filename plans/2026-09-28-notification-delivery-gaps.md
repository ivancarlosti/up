# Plan - Notification delivery gaps (webhook query dropped, silent incidents, dead resend setting)

> **Plan** — task: *alerts never arrive on a single node (`CLUSTER_ENABLED=false`); the webhook
> channel's token sits in the URL query, the delivery log stays empty and the UI claims
> "Linked monitors: 0". Find and fix what stops the delivery*, worked on **2026-09-28**. Local-only
> working note (`plans/`, tracked in git), kept because §2 records the one line in
> `buildWebhookURL` that swallowed every operator query parameter (and why the existing test could
> not see it), and §3 records the semantics chosen for the re-notification clock — a channel
> interval that was stored, editable in three dialogs and read by nobody.

## 1. Goal (as asked)

1. **Alerts never fire** on a single-node install: no webhook call reaches the target, no row in the
   delivery log. Make the notification path work with `CLUSTER_ENABLED=false`.
2. **The webhook URL carries the credential** (`https://target/hook?token=…`): it must be delivered
   as-is, not normalised away.
3. Keep the cluster path (`notification_sender`, shared `monitor_states`) working, add no
   configuration knob and no migration.

## 2. What the review found

### The wiring is fine (two theories disproved first)

- **The scheduler is unconditional.** `internal/scheduler/execute.go` calls `EvaluateAndNotify`
  after every heartbeat, and `cmd/server/app.go` builds the notification, monitor and stats
  services and starts the scheduler **and** the maintenance loop regardless of `CLUSTER_ENABLED`.
  Cluster off does not unplug the pipeline.
- **The node row is not the blocker.** `maintenance.go` pings the local node row unconditionally
  (lines 73 and 122), so `Nodes()` is non-empty on a single node and that node's vote is online:
  `AggregateVotes` only answers `pending` when nobody voted, and `Evaluate` is not short-circuited.
- **The link flags are not the blocker either.** `MonitorNotification.OnDown` / `OnUp` default to
  `true` (`models/monitor.go:255-256`) and the monitor write path sets both explicitly
  (`monitor_write.go:462`, `sync_apply.go:618`), so `eventEnabled` does not filter every channel out.
- Config pinned from `.env`: `CLUSTER_ENABLED=false`, `CLUSTER_MODE=shared` (`Federated()` false),
  `LOG_LEVEL=debug`.

### Defect A (the reported symptom) - the operator's query never leaves the process

> Line numbers below refer to the reviewed revision (`0ed0ca7`); the tests named after the fix are
> the stable reference.

`buildWebhookURL` (`internal/notify/urls.go:60-103`) parses the operator's URL only to read `Host`
and `Path`, then builds a **fresh** `url.URL{Scheme, Host, Path, RawQuery}` from a query it composes
itself. `parsed.Query()` is never read, so `?token=…`, every other operator parameter and the
userinfo of `https://user:pass@target/…` are dropped. The target answers 401/404 (or 400, for a
target that only checks `token`) and the failure looks like "the webhook does not fire".

This is the only place the query dies, because shoutrrr's generic service is a pass-through:

- `generic.Service.Initialize` → `Config.setURL` (`generic_config.go:133-157`) copies the service
  URL and removes **only** the keys `PropKeyResolver.QueryFields()` knows
  (`format.SetConfigPropsFromQuery`, `format_query.go:56-85`) plus the `@`/`$` prefixed ones
  (`stripCustomQueryValues`). Everything else stays in `webhookURL.RawQuery`.
- `Config.WebhookURL()` (`generic_config.go:99-111`) copies that URL, forces the scheme (`https`,
  or `http` when `disabletls` is on) and keeps host, port, path, **userinfo and query**; it is what
  `doSend` hands to `http.NewRequestWithContext` (`generic.go:164`).

The prop keys are exactly: `contenttype`, `disabletls`, `template`, `title`, `titlekey`,
`messagekey`, `method` (the `key:` tags of `generic.Config`, `generic_config.go:22-28`).

Why the suite stayed green: `TestBuildWebhookURL` already builds
`http://hooks.internal:8080/up?existing=1` (`notify_test.go:79`) but asserts nothing about
`existing`, so dropping the query was invisible.

### Defect B - a claimed incident with zero channels is marked as notified

`internal/services/cluster_notify.go:84-92`: as soon as `claimNotification` grants the claim,
`state.Notified = status`, `state.NotifiedAt = &now` and `dispatched = true` are set — whatever
`Dispatch` returned. With `resend_interval_seconds = 0` (the default in all three dialogs) the
"already down" branch can never fire again, so an incident that was evaluated while the monitor had
no active/linked channel is silent **for its whole duration**; the only trace is the info line
`notification event processed … channels=0`. It explains the "no alerts at all" report even for an
operator whose channel is correct but was linked after the first failure.

### Defect C - the channel-level re-notification interval is dead

The resend branch (`cluster_notify.go:68-71`) reads only `monitor.ResendIntervalSeconds`. The
channel's own `notifications.resend_interval_seconds` is editable in three dialogs
(`AdminNotificationsView.vue:401`, `MonitorForm.vue:312`, `AdminMonitorTemplatesView.vue:525`), is
stored, is exported in the API and in the cluster payload (`sync_payload.go:168`) — and is read by
nothing. `models.EffectiveResendInterval` (`models/notification.go:330-338`, "the larger value
wins") has **no caller at all**.

### Defect D - the channel dialog pretends to own the links, and gives no hint

`AdminNotificationsView.vue` sends `monitor_ids` in the channel payload (lines 89 and 240) on every
save, but a channel write never persists it: `models.Notification.MonitorIDs` is `gorm:"-"` and no
notification handler or service reads the field back (`models/notification.go:62-63`); only the
monitor-group and status-page handlers parse a `monitor_ids` field. The value **is** computed on
read for the list and the single-channel response (`notification.go:83-99` and `Get`, nil
normalised to `[]`), so the card badge is accurate — the defect is that the dialog offers no way to
create a link and does not say where links live: an operator whose alerts never fire sees
"Linked monitors: 0", saves the channel again (which changes nothing), and has no cue that the link
must be made from the monitor side (`MonitorForm.vue` `notification_ids` checkboxes, the only writer
besides the cluster sync).

## 3. Decisions taken

### A — carry the operator's URL through, escape only what shoutrrr would eat

`buildWebhookURL` copies the parsed URL (`serviceURL := *parsed`) instead of rebuilding it, so
userinfo, port, path (`RawPath` included) survive, and seeds the query with `parsed.Query()`.
Colliding keys are escaped with shoutrrr's own convention (`format.EscapeKey` → `__key`), which
`SetConfigPropsFromQuery` moves back into the target URL (`format_query.go:74-81`).

- **Invariant (documented and tested): every query key of `webhook.url` is present, with the same
  value, in the request sent to the target.** Keys that collide with the generic service's seven
  props are escaped on the shoutrrr side and restored before the request is built, so `?method=GET`
  in the operator's URL still arrives at the target as `method=GET` while the dialog's method field
  keeps controlling the verb. The one documented exception is the `@`/`$` prefixes: shoutrrr turns
  those into request headers / template extra data by design, so they are not forwarded as query
  parameters.
- **The reserved set is derived, not hardcoded**: a package-level `webhookServiceProps()` reads
  `generic.DefaultConfig()`'s `PropKeyResolver.QueryFields()`. A shoutrrr bump that adds a prop then
  escapes it automatically instead of silently eating an operator parameter. The test pins the
  resulting set to the current seven keys, so the behaviour change is visible in review rather than
  only in production.
- **Up's settings win, deterministically.** `method`, `contenttype` and `disabletls` (derived from
  the scheme, which stays ground truth for `http://` targets) come from the channel form, and
  `@Content-Type` is still forced (shoutrrr would otherwise send `text/plain` for a template-less
  body). Any operator variant of that header key (`@content-type`, `@Content-Type`) is removed
  before ours is set, so the header cannot flip with Go's map iteration order.
- Duplicate operator keys are preserved (append the value slice; never `Set`).
- Rejected alternative: passing `method`/`contenttype`/… as shoutrrr **send params** instead of
  service-URL props (`UpdateConfigFromParams`, `generic.go:127`) and leaving the operator's query
  untouched. It works, but it rewrites the contract `TestBuildWebhookURL` and `docs/notifications.md`
  describe, and a colliding key would be *consumed* by the service instead of reaching the target —
  the class of silent loss this fix removes.

### B — the notification clock advances only when something was attempted

`dispatched = len(logs) > 0`. A granted claim that reached zero channels no longer marks the
incident as notified, and the branch logs a warning naming the monitor and the reason, so "the alert
never fires" becomes a line in the app log instead of a silent state change.

- "Zero channels" is a cheaper failure than it looks: with no link there is no network call at all,
  and the incident stays re-evaluable, so the operator who links a channel while the monitor is down
  gets the alert on the next check instead of waiting for a recovery.
- A channel that was **attempted and failed** still advances the clock: the error is recorded in
  `notification_logs` (visible in the UI) and re-dispatching a broken endpoint on every check would
  be a storm. Retrying is what `resend_interval_seconds > 0` is for.
- A DB error inside `Dispatch` also returns no log, and now also no longer consumes the incident.

### C — the channel interval finally counts

The "already down, already notified" branch asks the notification service for the largest
`resend_interval_seconds` among the monitor's linked channels and takes the larger of the two
through `models.EffectiveResendInterval` — which finally has a caller. The helper follows the
`groupNames` pattern in `notification_dispatch.go` (`Table(...).Joins(...).Pluck(...)`) and is only
called in that branch (one query per re-notification check, not per heartbeat).

### D — the dialog stops sending a field it cannot own, and says where the links are

`monitor_ids` is dropped from the create/update payload in `channelPayload()` (it is a computed,
read-only field: `gorm:"-"`, filled by `List`/`Get` only). The type keeps it, because the card badge
reads the count from the API — and that count is now explained: when a channel has no link, the card
renders the **existing** `notifications.linkedMonitorsHelp` line ("Without any link the channel
receives no alerts") right under the destination, using a key that is already present in all seven
locales — no new translation key, so `npm run check:i18n` stays green and no locale needs a
translation.

### Not changed

No new setting, no migration, no cluster wire-format change: the same `notifications` table and the
same `sync_payload.go` shape. `docs/notifications.md` gains the URL-query semantics and the resend
precedence; the http-only `titlekey`/`messagekey` assignment stays as it is (it equals shoutrrr's
defaults — harmless, and out of scope).

## 4. Files touched

| Layer | File |
|---|---|
| Notify URL | `internal/notify/urls.go` (`buildWebhookURL` passthrough + `webhookServiceProps` + content-type normalisation) |
| Notify test | `internal/notify/notify_test.go` (`TestBuildWebhookURL` asserts `existing=1` survives; new cases for userinfo, prop-key escaping, duplicate keys; `httptest` round trip proving the target sees `?token=…` and the Basic userinfo) |
| Service | `internal/services/cluster_notify.go` (B: `dispatched` gate + warning; C: effective interval), `internal/services/notification_dispatch.go` (`MaxResendInterval`, next to `groupNames`/`links`) |
| Service test | `internal/services/cluster_notify_test.go` (pure cases: `shouldResend`, next to the existing `stateUpdateColumns` pin) and two opt-in live tests against a throwaway MariaDB: `notification_resend_integration_test.go` (the new query) and `cluster_notify_integration_test.go` (the whole pipeline, from the aggregated state to the request the target receives) |
| Model test | `internal/models/notification_test.go` (`EffectiveResendInterval`, currently uncovered) |
| Web | `web/src/views/admin/AdminNotificationsView.vue` (payload without `monitor_ids`, hint when the count is 0) |
| Docs | `docs/notifications.md` (webhook `url` query/userinfo, resend precedence, where links are made) |

Order of work: (1) this plan file; (2) A + its tests; (3) B and C + their tests; (4) D; (5) docs;
(6) the verification block below.

## 5. Verification commands

```bash
cd /home/ivan/Documents/Git/up
export PATH="$HOME/.local/go/bin:$PATH"
gofmt -l internal/notify internal/services internal/models
go build ./... && go vet ./internal/notify/ ./internal/services/ ./internal/models/
go test ./internal/notify/ -run 'Webhook' -v
go test ./internal/services/ ./internal/models/ -run 'Notify|Resend|StateUpdate' -v
go test ./...
cd web && npm run typecheck && npm run check:i18n
```

Result (2026-09-28, single node `CLUSTER_ENABLED=false`, Go toolchain at `$HOME/.local/go`,
Node 18.19 in the sandbox):

- `gofmt -l internal/notify internal/services internal/models` → no output; `go build ./...` and
  `go vet ./...` → clean.
- `go test ./...` → `ok` in every package that has tests (checkers, database, expiry, handlers,
  middleware, models, notify, scheduler, services, utils). The two live tests skip themselves
  without `UP_SCRATCH_DSN`.
- `go test ./internal/notify/ -run Webhook -v` → `TestBuildWebhookURL`,
  `TestBuildWebhookURLCarriesOperatorURL` (4 cases), `TestWebhookServicePropsAreStable` and
  `TestWebhookRequestCarriesOperatorURL` all pass; the last one sends a **real** request to an
  `httptest` target and asserts it received `?token=abc123&mode=fast&method=GET` plus the Basic
  userinfo, while the request verb stayed `PUT` and the body arrived verbatim.
- Live against a throwaway MariaDB 11.8.9 (`UP_SCRATCH_DSN='up:verify@tcp(127.0.0.1:3308)/up?…'`):
  `TestNotificationMaxResendIntervalLive` (the join, both filters and the MAX → 900, not 7200) and
  `TestNotificationPipelineLive` (no channel → nothing sent and `notified_at` stays NULL; channel
  linked while down → the alert is delivered with the token; no interval → no repeat; channel
  interval elapsed → the repeat is sent) both pass.
- **Regression check**: with the old behaviour temporarily restored (`dispatched = true`,
  monitor-only interval), the new tests fail with the reported symptoms —
  `the target must receive the token, got ""` and
  `the alert must be delivered as soon as a channel is linked, got 0 requests` — so they pin the
  defect and not the implementation.
- `cd web && npm run typecheck` → clean; `npm run check:i18n` → `ok 5201 messages compiled`, and
  `linkedMonitorsHelp` is present in all 7 locales (the hint needed no new key).
- `npm run build` cannot run here: `vite` imports `node:util`'s `styleText`, which the sandbox's
  Node 18.19 does not provide (the same failure happens on an unmodified checkout; CI uses Node 20).
  `vue-tsc --noEmit` is the type gate that covers the template change, and it passes.
- The `npm run e2e:notifications` browser check needs the built bundle plus Chrome, so it could not
  run in this sandbox; the live pipeline test above covers the same ground at the API/HTTP level.

## 6. Risks / notes

- **The target now really receives the operator's query.** That is the fix, but it also means a
  parameter that was previously dropped is now sent (and will show up in the target's logs). No
  behavioural change for URLs without a query, which is every URL the UI currently suggests.
- **A prop written in the URL is not a prop for Up.** `?template=json` (a shoutrrr generic feature)
  is forwarded to the target instead of switching the payload format, because Up renders the body
  from `body_template` and passes it as the message. Documented; the escaping rule is uniform on
  purpose, so no parameter is ever silently swallowed.
- **Zero-channel incidents are re-evaluated while the monitor is down** and no channel is eligible:
  a claim insert and a couple of selects, no network call, and it self-heals the moment a channel is
  linked (the alert then goes out on the next check, gated by the notification lock window - 60s by
  default - which is what keeps the retry from being once per heartbeat).
- **A failed delivery still consumes the incident** (resend > 0 to retry, or the *Test* button).
  Pre-existing semantics, now documented in `docs/notifications.md`.
- **The reserved-key derivation is version-sensitive by design**: `generic.DefaultConfig()` is public
  API of the pinned module and the test pins today's seven keys, so a shoutrrr upgrade that changes
  them fails the suite instead of dropping an operator parameter quietly.
- No cluster impact: nothing in the sync payload, the models or the protocol version changed, so
  nodes can be upgraded one by one.
