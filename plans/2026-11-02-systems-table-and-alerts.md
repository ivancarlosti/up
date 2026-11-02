# Plan - Systems table, summary boxes and per system alerts

> **Shipped plan** — task: *"rework the Systems view into a table with summary
> boxes and a search filter, and give every system notification alert thresholds
> modelled on Beszel, with per system channel links and per event toggles"*.
> Implemented on **2026-11-02**.
>
> Owner decision taken before coding: the channel configuration of a system is
> **per system links + per event toggles** (`on_down`, `on_up`, `on_threshold`,
> `on_recovered`), not the single "notify me" switch the monitors have.

## 1. Goal

1. The Systems page lists machines in a **sortable table** (name, status, CPU,
   memory, disk, containers, last sample, owner) instead of one card per machine.
2. **Seven summary boxes** above the table, overlapping, each one a filter:
   Reporting, Pending, Down, Alerting, High CPU, High memory, High disk.
3. One **search box** in the toolbar that filters all three tabs (it used to
   live inside the tab strip).
4. Every machine gets **alert rules** (`cpu`, `memory`, `disk`, `down`), each with
   a threshold and a sustained window in minutes, and can be linked to the
   notification channels with one toggle per event.

## 2. Semantics (Beszel)

| Rule | Fires when |
|---|---|
| `cpu` / `memory` / `disk` | `AVG(metric)` over `[T - min, T]` ≥ `threshold`, **and** the window is actually covered (the oldest sample in it is at most `T - 0.9·min`) |
| `down` | `now - last_sample_at - SystemDownAfter` ≥ `min` (the listing already waits `SystemDownAfter`, so `min` is the extra patience) |

The evaluation is **stateless**: nothing is remembered between ticks except the
latch. The latch (`system_alerts.triggered` / `triggered_at`) is claimed with
`UPDATE … WHERE triggered = 0`, so exactly one node — or one tick — dispatches
the "firing" edge, and it survives a failover because it lives in the shared
database. The latch is **local state and never travels** to a peer.

Recovery is the opposite edge: `triggered = 1 → 0`, and the notification is sent
only if that update changed a row (i.e. the alert had actually fired).

## 3. Events and channels

| Edge | Event sent | Toggle |
|---|---|---|
| down rule starts firing | `system_down` | `on_down` |
| down rule recovers (samples resume) | `system_up` | `on_up` |
| threshold rule starts firing | `system_threshold` | `on_threshold` |
| threshold rule comes back under | `system_recovered` | `on_recovered` |

A channel whose toggle for the event is off is skipped **entirely**: no
`notification_logs` row is written for it, exactly like the monitor path.

## 4. Data

| Table | Contents | Travels |
|---|---|---|
| `system_alerts` | one row per (system, metric): `enabled`, `threshold`, `min_minutes` + the local latch `triggered`, `triggered_at` | rules only, inside `SystemPayload.alerts` |
| `system_notifications` | one row per (system, channel) with the four toggles | its own entity `system_notification` |
| `notification_logs.system_id` | which machine a delivery was about | local |

## 5. Implementation

| Where | Change |
|---|---|
| `internal/models/system_alert.go` | **new**: `SystemAlertMetric`, `SystemAlertRule`, `SystemAlert` (row), `SystemNotification` (row), `DefaultSystemAlerts`, `NormalizeSystemAlert`, `EvaluateSystemAlert`, `EvaluateDown` |
| `internal/models/heartbeat.go` | `NotificationLog.SystemID` |
| `internal/models/strategies.go` | the four `system_*` events |
| `internal/models/sync.go` | `EntitySystemNotification` + `SystemNotificationUUID` |
| `internal/models/sync_payload.go` | `SystemPayload.Alerts`, `SystemAlertRulePayload`, `SystemNotificationPayload` |
| `internal/services/system_alerts.go` | **new**: `Rules`, `States`, `EnsureDefaults`, `Save` (rules + links, one transaction, emits) , `Claim`, `Release` |
| `internal/services/system_alert_eval.go` | **new**: `EvaluateSample`, `Sweep`, `evaluateThreshold`, `evaluateDown`, `dispatch` |
| `internal/services/system_lease.go` | `decorate` gains the alert rules |
| `internal/services/system_ingest.go` | evaluates the rules of the sample just stored |
| `internal/services/system_write.go` | links are removed with the system |
| `internal/services/notification*.go` | `DispatchSystem`, `deliverSystem`, `NotificationLog.SystemID` |
| `internal/notify/system.go` | **new**: `NewSystemMessage`, `SystemLink`, the system rows of the text/HTML bodies |
| `internal/services/sync_emit.go` | `EmitSystem` carries the rules, `EmitSystemNotification` |
| `internal/services/sync_apply*.go` | `writeSystem` upserts the rules (latch untouched), the new relation |
| `internal/handlers/systems.go` + `router_admin.go` | `GET`/`PUT /api/systems/:id/alerts` |
| `internal/scheduler/maintenance.go` | 30 s `Sweep` tick |
| `cmd/server/app.go` | wires the alert service |
| `web/src/components/systems/SystemTable.vue` | **new**: the table |
| `web/src/lib/system-sort.ts` | **new**: the comparators |
| `web/src/views/SystemsView.vue` | 7 boxes, toolbar + search, the table |
| `web/src/stores/systems.ts` | `overview`, the box counts, `saveAlerts` |
| `web/src/components/systems/SystemAlertsDialog.vue` | **new**: rules + channels |
| `web/src/locales/*.json` (7) | 31 new `systems.*` keys (English, like the rest of the block) |
| `docs/hardware-monitoring.md`, `docs/notifications.md`, `docs/clustering-modes.md` | the new behaviour |

## 6. Tests

| File | What it pins |
|---|---|
| `internal/models/system_alert_test.go` | the defaults, the clamp of an incoming rule (and that the latch is never trusted from outside), the sustained evaluation (window coverage, disabled rule, the down rule has no number), the down arithmetic (`SystemDownAfter` + minutes) |
| `internal/notify/system_test.go` | the event label of the title, the `/systems/<id>` link (with and without `APP_URL`), the "System/Metric" body lines, the extra webhook fields, and that a monitor payload keeps exactly its old shape |
| `internal/services/system_alerts_test.go` | the event→toggle mapping, the link diff, the display order/defaults of a machine with no row |
| `internal/services/system_alerts_integration_test.go` | **opt-in** (`UP_SCRATCH_DSN`): defaults, a save that keeps a rule **off**, the patch semantics of an omitted metric, one winner per claim, one recovery per release, the four toggles against a real webhook, `notification_logs.system_id`, and a sustained 95 % firing then releasing |
| `internal/handlers/systems_routes_test.go` | the two new routes, registered next to `/:id/history` |
| `internal/services/sync_inventory_test.go` | the two new synced tables on the inventory (`Claim`/`Release` write the latch only, `saveChannels` publishes through `emitLinks`) |

## 7. Validation

- `gofmt -l internal cmd`, `go build ./...`, `go vet ./internal/...` — clean.
- `go test ./internal/models/... ./internal/services/... ./internal/handlers/... ./internal/notify/...`
  — pass (19 write paths inspected by the inventory guard).
- **Live**, against a throwaway MariaDB 11 (`UP_SCRATCH_DSN=…:3310`):
  `go test ./internal/services -run TestSystemAlertsLive -v` — pass. It caught two
  real things while it was written: a webhook header is **static** (only the body
  template is rendered), and a rule saved as off must be turned back on before it
  can fire.
- `cd web && npm run typecheck && npm run check:i18n && npm run build` — clean
  (1 173 messages per locale, 1 004 used keys).

## 8. Results

The Systems page is a table (sortable by name, status, CPU, memory, disk,
containers and last sample, with the sort remembered per browser), the seven
summary boxes are filters, and the search box moved out of the tab strip into the
toolbar. Every machine now has four alert rules with a threshold, a window and a
latch, and its own notification links with four toggles.

The latch is the one design decision worth repeating: it is claimed with a
conditional `UPDATE`, so a cluster sends each edge exactly once and the state
survives a failover — and it is never synchronised, so a peer cannot inherit an
incident it did not see.

## 9. Documentation

`docs/hardware-monitoring.md` gained the two tables in §3, the whole **§5 Alerts**
(the rules, the two evaluation paths, the latch, the four events, the boxes) and
the two new routes; `docs/notifications.md` gained **§1 Hardware alerts** and the
`system_*` rows in the link and log tables; `docs/clustering-modes.md` §10 lists
the two new tables (and §22 explains the upsert of the link payload); `README.md`
mentions the alert rules on the Systems row.

