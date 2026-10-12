# Plan - Configurable check intervals for the web performance job

> Task: the Lighthouse measurement of a page stops being a daily-only job: a
> monitor (and a template) picks its own cadence — hourly, every 6 or 12 hours,
> daily (default) or weekly on a weekday chosen in Admin > Settings — while the
> job keeps its single run per deduplicated target per slot.

## 1. Goal

Today `lighthouse_enabled` means "measured once a day at `check_time`", and the
day bucket in `settings` is what makes the job restart proof. A page whose
performance regresses in an afternoon cannot be followed without lowering the
whole fleet's cadence, and a slow marketing page does not deserve 24 API calls a
day.

This change makes the cadence a **per monitor** property
(`config.lighthouse_interval`, travelling with the monitor and with a template
exactly like `config.lighthouse_strategy`), keeps the job configuration (time of
day, timezone, fallback cadence, weekly weekday) in Admin > Settings, and derives
the schedule of every monitor from the instant of its **last stored run** instead
of from a counter. The *Web performance* page shows the cadence and the next run
of every row; the history endpoint gains an optional `?limit=` so an hourly
monitor does not download a month of points into the chart.

## 2. Decisions

- **D1 — a discrete cadence enum, not a free interval.** `LighthouseIntervals` =
  `hourly`, `6h`, `12h`, `daily`, `weekly` (+ `DefaultLighthouseInterval =
  daily`), mirroring `UptimeWindowHoursAllowed`: a closed set validates
  identically in the API, the form and the stored configuration, and it is
  translated instead of printed as a code.
- **D2 — the schedule is the last run.** `models.LighthouseSchedule` is a pure
  value object (`NextRun`, `RunDue`) built from the job clock
  (`check_time`/`check_timezone`), the cadence and the weekday. Due-ness is
  `NextRun(ran_at) <= now`, so there is **no new column, no new table and no
  counter**: `monitor_lighthouses.ran_at` (written by whoever measured) is the
  only state, a restart repeats nothing, a process that was down catches up on
  its next tick and every node of a cluster derives the same answer.
- **D3 — the grid is anchored at `check_time`.** The runs of a cadence repeat from
  the configured time of day, counted in real time: `04:15` + `hourly` fires at
  `:15` every hour, `+ 6h` at `04:15, 10:15, 16:15, 22:15` local and `+ 12h` at
  `04:15, 16:15`; `daily`/`weekly` use the calendar (`RunInstant` /
  `NextDailyRun`, so a DST change keeps the local hour), and the weekly one lands
  on the **configured weekday** (`settings.lighthouse_weekday`, default `monday`,
  `LighthouseWeekdays`). The anchor is recomputed from the day of the instant it
  is asked about, so a sub-daily grid re-anchors after a DST change instead of
  drifting.
- **D4 — the setting is a fallback, never an override.** `settings.interval` +
  `settings.weekday` are the cadence of a monitor that carries none (a row written
  before the field existed, which stays valid), and the default the form offers.
  Turning the switch off clears both the strategy and the cadence, so a stale
  value can never apply to the next enable.
- **D5 — deduplication is unchanged.** The identity of a run stays
  `url|strategy`; the cadence decides *when* a target is measured, not how it is
  keyed. When two monitors share a target, the one with the shorter cadence wins
  in practice: the shared `ran_at` makes the next run due as soon as the smallest
  interval has elapsed, and the longer one simply receives the fresher score.
- **D6 — the cluster lock claims an HOUR slot** (`models.LighthouseSlot(now) =
  now.UTC().Unix()/3600`, `ClaimDailyJob("lighthouse_run", slot)`) instead of a day
  bucket, and it is only taken **after** the due monitors are known: a node with
  nothing to do never claims, and the PageSpeed Insights quota is spent once.
- **D7 — the ticker never blocks.** `RunDue` takes `runMu` with `TryLock` and
  returns (logging at debug level) when a run is in flight; the manual actions
  (`RunNow`, `Refresh`) take it exclusively, because the operator asked for that
  run. The ticker case in `scheduler/maintenance.go` hands the tick to a goroutine
  so a long run cannot stall the other maintenance loops, and
  `settings.lighthouse_last_run_day` becomes **informational** (the admin page
  reports the last run day); it no longer gates the job.
- **D8 — the history read is capped by the client.** `history?limit=N` keeps the N
  newest runs (clamped to 1000); the chart asks for 200, because an hourly monitor
  collects 24 points a day and the retention (30 days) would otherwise stream
  thousands.
- **D9 — the page is told, it does not guess.** The listing decorates each stored
  observation with the computed `interval` and `next_run` (never stored), the
  settings endpoint returns `intervals` + `weekdays` and the monitors endpoint
  returns `interval_default` (the cadence a monitor without one follows), so the UI
  renders the effective cadence of a legacy row without a second source of truth.
- **D10 — no migration, no dependency, no environment variable.** Both new
  settings are key/value rows (`lighthouse_interval`, `lighthouse_weekday`), the
  cadence lives inside the existing `monitors.config` JSON column and the runner is
  untouched: the change is pure Go and TypeScript.

## 3. Changes

| File | Change |
|---|---|
| `internal/models/lighthouse_schedule.go` (new) | the cadence + weekday enums, `LighthouseSchedule` (`Normalize`/`NextRun`/`RunDue`), `LighthouseSlot`, `LighthouseIntervalDuration`, `nextSteppedRun`, `nextWeeklyRun` |
| `internal/models/lighthouse_schedule_test.go` (new) | hourly / 6h / 12h grids, daily + weekly (default and another weekday), timezone anchoring, DST re-anchor, `RunDue`, slot, settings projection |
| `internal/models/lighthouse.go` | `LighthouseSettings.Interval`/`Weekday` (+ defaults, `Normalize`, `Validate`, `Schedule()`); `LighthouseResult.Interval`/`NextRun` (computed) |
| `internal/models/monitor_config.go` | `LighthouseInterval` field, normalisation, `PruneToType` for http/keyword |
| `internal/models/monitor_validate.go` | `config.lighthouse_interval` validation (an empty value stays valid) |
| `internal/models/security.go` | `SettingLighthouseInterval`, `SettingLighthouseWeekday` |
| `internal/lighthouse/targets.go` | `IntervalFor(monitor, fallback)` (per monitor, job setting as the fallback) |
| `internal/services/settings.go` | read/write the two new settings |
| `internal/services/lighthouse.go` | `LastRuns` (one query), the `runMu` TryLock contract |
| `internal/services/lighthouse_run.go` | `RunDue` (due monitors + hour slot + informational day), `dueMonitors`, `runTargets`, `RunNow`, `NextRun` from the schedule, the listing decoration (`interval` + `next_run`) |
| `internal/services/monitor_validate.go`, `internal/services/monitor_template_apply.go` | clear the cadence with the switch; report the strategy/cadence in the template dry run |
| `internal/scheduler/maintenance.go` | the tick is handed to a goroutine; the boot log reports the cadence and the weekday |
| `internal/handlers/lighthouse.go` | `intervals`/`weekdays` in the settings payload, `interval_default` in the listing, `?limit=` on the history |
| `web/src/lib/types.ts`, `web/src/lib/types-platform.ts` | `LighthouseInterval`/`LighthouseWeekday`, the settings/response fields, the computed `interval`/`next_run` |
| `web/src/lib/monitor-config.ts` | the option lists, the label keys, `lighthouseInterval`/`lighthouseWeekday`/`effectiveLighthouseInterval`, the sanitizer |
| `web/src/lib/api-admin.ts` | `lighthouseHistory(id, limit)` |
| `web/src/components/lighthouse/LighthouseJobCard.vue` | cadence select, weekday select (weekly only) |
| `web/src/components/monitors/MonitorForm.vue`, `web/src/views/admin/AdminMonitorTemplatesView.vue` | the cadence select of a monitor and of a template |
| `web/src/views/admin/WebPerformanceView.vue` | cadence column + next run ("due now"), default-cadence next run, the cadence in the filter, the capped history read |
| `web/src/locales/*.json` (7) | the new keys (cadences, weekdays, due-at/now, help texts) and the updated daily-only wording |
| `docs/monitors.md` (§15), `docs/api.md`, `docs/database.md`, `README.md` | the cadences, the fallback rule, the derived schedule, the new response fields, the `?limit=` |

## 4. Verification

- `gofmt -l internal/`, `go build ./...`, `go vet ./internal/...`,
  `go test ./internal/...` (the schedule is unit tested, nothing reaches Google).
- `npm run typecheck`, `npm run check:i18n` (7 locales, same key set),
  `npm run build`.
- Manual smoke: enable `hourly` on a monitor, save the form, confirm the *Web
  performance* page shows the cadence and the next run; press *measure now*; set a
  stale `ran_at` with SQL and watch one tick pick the monitor up (the only step a
  unit test cannot prove).

## 5. Out of scope

- No alerting on a score, no threshold, no trend beyond the retention window.
- No cron expression and no per monitor time-of-day or timezone: a monitor picks a
  cadence, the job owns the clock.
- No per target rate limit and no API key per monitor.
- No change to what is measured or stored (the same four scores and five vitals).
