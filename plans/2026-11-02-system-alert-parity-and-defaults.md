# Plan - Beszel parity alert rules, default thresholds, copy and reset

> Task: *"extend the hardware alerts with the rest of the Beszel alert types, add
> settings for the default thresholds, and let one machine copy the rules of
> another one or go back to the defaults"*.
>
> Decisions taken before coding: the defaults live in **Admin > Settings** (they
> are global and travel in the `CLUSTER_SYNC_SETTINGS` whitelist), every new rule
> ships **enabled** by default, and the catalogue includes `swap`, `load5` and
> `load15` on top of the strict Beszel set.

## 1. Goal

1. Grow `system_alerts` from four rules (`cpu`, `memory`, `disk`, `down`) to the
   Beszel catalogue plus the load history: `swap`, `load`, `load5`, `load15`,
   `temperature`, `bandwidth`, `gpu`, `container`, `smart`.
2. Turn the built-in rule set into a **global setting**
   (`system_alert_defaults`) editable in Admin > Settings, with a "restore the
   built-in values" action.
3. Per machine: **copy the rules of another monitor** and **reset to the
   defaults**, both from the alerts dialog.
4. Keep the cluster story intact: the defaults travel with the settings
   whitelist, the rules keep travelling inside the system payload, and the latch
   stays local.

## 2. Catalogue

| key | unit | built-in threshold | window | evaluated from |
|---|---|---|---|---|
| `down` | - | 5 minutes | 5 minutes | `systems.last_sample_at` + `SystemDownAfter` |
| `cpu` | `%` | 80 | 5 minutes | `AVG(system_metrics.cpu)` |
| `memory` | `%` | 80 | 5 minutes | `AVG(mem_pct)` |
| `swap` | `%` | 80 | 5 minutes | `AVG(swap_pct)` |
| `disk` | `%` | 85 | 5 minutes | `AVG(disk_pct)` |
| `load` | load | 10 | 5 minutes | `AVG(load_avg)` |
| `load5` | load | 10 | 5 minutes | `AVG(load_avg5)` (new column) |
| `load15` | load | 10 | 5 minutes | `AVG(load_avg15)` (new column) |
| `temperature` | `°C` | 80 | 5 minutes | `AVG(temperature)` |
| `bandwidth` | `MB/s` | 80 | 5 minutes | `AVG((net_sent + net_recv) / 1048576)` |
| `gpu` | `%` | 80 | 5 minutes | `AVG(gpu_pct)` (new column) |
| `container` | containers | 1 | - | `COUNT(system_containers WHERE health='unhealthy')` |
| `smart` | disks | 1 | - | `system_smart.failed` (new column) |

The numeric rules keep the Beszel semantics: the alert fires when the average of
the window is at or above the threshold **and** the oldest sample of the window is
at most `0.9 x minutes` old. The two count rules are state checks: they fire as
soon as the count reaches the threshold and recover as soon as it drops below,
with no window and no warm-up.

Units/thresholds mirror Beszel's `alerts-sheet` ranges: CPU/memory/disk/GPU
1..100, temperature 40..100, bandwidth 1..250, load 0..64, count rules minimum 1.

Deferred (documented, not implemented): battery, failed systemd units, ZFS pool
health, CPU iowait/steal, probe-side loss/latency/certificate rules. Each needs a
new ingest column or a different evaluator shape; the registry is the extension
point.

## 3. Models

`internal/models/system_alert_spec.go` (new) is the declarative registry:

```go
type SystemAlertSpec struct {
	Metric           SystemAlertMetric `json:"metric"`
	Unit             SystemAlertUnit   `json:"unit"`
	Min, Max, Step   float64
	HasThreshold     bool
	HasWindow        bool
	DefaultEnabled   bool
	DefaultThreshold float64
	DefaultMinutes   int
}
```

plus `AllSystemAlertSpecs`, `SystemAlertSpecOf`, `SystemAlertUnitOf`,
`FormatSystemAlertValue`, `FormatSystemAlertThreshold`,
`SystemAlertCountDetail`. `AllSystemAlertMetrics` is derived from it, so the
order can never drift.

`internal/models/system_alert.go` keeps `DefaultSystemAlert`,
`NormalizeSystemAlert` (now range-checked against the catalogue instead of the
hardcoded 1..100) and `EvaluateSystemAlert`, and adds:

- `SystemAlertDefaults{Rules []SystemAlertRule}` with JSON `{"rules": [...]}`;
- `DefaultSystemAlertDefaults` and `NormalizeSystemAlertDefaults` (fill the
  missing metrics, drop the unknown ones, clamp, force the latch off);
- `EvaluateSystemCountAlert(rule, count)`.

## 4. Setting and service

- `SettingSystemAlertDefaults = "system_alert_defaults"` (JSON blob), added to the
  `CLUSTER_SYNC_SETTINGS` whitelist.
- `SettingService.SystemAlertDefaults()` / `SetSystemAlertDefaults` (normalise on
  both sides; a broken value falls back to the built-ins).
- `SystemAlertService`: `defaults()` (setting or built-ins), `orderRules`,
  `ResetToDefaults`, `CopyFrom`, `touch` (revision + emit), and `View` now also
  returns `specs` and `defaults`.

## 5. Sweep and evaluator

- The sweep repair only served machines with **zero** rules (`ensureLegacyRules`);
  it becomes `ensureAllRules`: one `SELECT system_id, metric`, then the missing
  `(system, metric)` pairs inserted with `ON CONFLICT DO NOTHING` in a batched
  statement, so a machine that already had the four original rules receives the
  nine new ones exactly once and nothing that exists is overwritten.
- A machine whose **down** rule is switched off has its down latch cleared
  silently: nothing else would ever release it, and "back up" would be a lie.
- `metricColumn` learns the new columns (and the bandwidth expression).
- The detail message formats with the unit of the rule (`average 82.4% ...`,
  `average 12.3 MB/s ...`, `2 unhealthy containers (threshold 1)`).
- `evaluateRule` dispatches: state rules count rows (`evaluateCount`), the rest
  average a window (`evaluateThreshold`).
- `internal/beszel.SmartDiskHealthy` (trim/upper; empty/PASSED/OK/GOOD healthy)
  feeds the `Failed` counter stored on `system_smart`; `RefreshSmart` evaluates
  the `smart` rule immediately (`EvaluateMetric`), because the report only
  refreshes every ten minutes.

## 6. API

| method + path | body | result |
|---|---|---|
| `GET /api/systems/:id/alerts` | - | view + `specs` + `defaults` |
| `PUT /api/systems/:id/alerts` | rules + links | unchanged |
| `POST /api/systems/:id/alerts/reset` | - | the view after the reset |
| `POST /api/systems/:id/alerts/copy` | `{"source_id": 3}` | the view after the copy |

`GET /api/admin/settings` returns `system_alert_defaults` and
`system_alert_specs`; `PUT /api/admin/settings` accepts the former.

Reset and copy **keep** the local latch and the target's channels: the next
evaluation releases what the new rules no longer satisfy, which is what sends the
recovery notification.

## 7. Frontend

- `lib/system-alert-form.ts` (new): the shared form shape (`AlertRuleForm`) and
  its conversions, the unit suffix and the default lookups.
- `components/systems/SystemAlertRules.vue` (new): the spec-driven editor shared
  by the alerts dialog and the admin card (unit in the threshold label,
  `min`/`max`/`step` from the API, the minutes field replaced by a help line for
  the state rules, the "default: ..." hints).
- `SystemAlertsDialog.vue`: the shared editor plus a toolbar with *Copy from...*
  (a `Select` of the other machines, applied immediately) and *Reset to defaults*
  (confirmed), both writing through the store and redrawing from the response.
- `AdminSettingsView.vue`: a "Hardware alert defaults" card with *Restore the
  built-in values*, saved by the page button.
- Types (`SystemAlertMetric`, `SystemAlertUnit`, `SystemAlertSpec`,
  `SystemAlertDefaults`, `SystemAlertRuleConfig`), `api-systems`/`api-admin`,
  store (`resetAlerts`, `copyAlerts`) updated; the new labels added to every
  locale (the `systems.*` block is kept in English everywhere, like the rest of
  that namespace; the `settings.*` keys are translated).

## 8. Docs

`docs/hardware-monitoring.md`: the new columns, the full catalogue, count-rule
semantics, the SMART refresh path, the defaults card, copy/reset and the deferred
list. `docs/clustering-modes.md`: the defaults are part of the synchronised
settings. `README.md`: the Systems row lists the new rules.

## 9. Validation

`gofmt -l`, `go build ./...`, `go vet ./internal/...`, the unit tests of
`models`, `services`, `handlers`, `notify` and `beszel` (including the sweep
inventory guard, which needed `clearLatch` on its latch allow-list), the opt-in
`TestSystemAlertsLive` scratch-database run (extended with copy and reset), and
in `web/`: `npm run typecheck`, `npm run check:i18n`, `npm run build`.

