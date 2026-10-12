# Plan - One General settings card and a "both runs" web performance strategy

> Task: stop the timezone from being edited twice (once on the certificate card,
> once on the web performance card), fold it into a single **General settings**
> box that owns the instance defaults, give every daily job the same, cluster
> wide zone, add a **Both** (mobile + desktop) strategy to the web performance
> measurement, and remove the two redundant "Job settings" boxes that only
> pointed at Admin > Settings.

## 1. Goal

Three things that used to be reasonable stopped being reasonable once both daily
jobs existed:

1. **The timezone was stored twice.** `expiry_check_timezone` and
   `lighthouse_check_timezone` were two keys with two readers and two writers, so
   an installation could check certificates in `Europe/Lisbon` and measure pages
   in `America/Sao_Paulo` without ever being told. The card that owns "locale,
   theme, app name, clock, uptime window" is the natural home of the zone, and one
   value for the whole installation is what an operator expects.
2. **"Both" was missing.** A page is scored either as a phone sees it or as a
   desktop sees it, never both, even though the two numbers tell different
   stories and the API charges one call each. A marketing page and its
   documentation usually need the pair.
3. **Two boxes were dead weight.** *TLD/SSL expiration* carried a "Job settings"
   card whose only content was a link to Admin > Settings, and the *Web
   performance* page embedded the very job card the Settings page already shows.
   Two places to look at for one configuration, one of them a stub.


## 2. Decisions

- **D1 — one key, no migration.** `settings.timezone` (`models.SettingTimezone`)
  is the single value. `SettingService.Timezone()` reads it and falls back to
  `expiry_check_timezone` then `lighthouse_check_timezone` (the certificate job's
  key first: it is the older of the two), so an upgrade keeps the zone the
  operator had already chosen without touching a single row.
  `SetTimezone` **validates** the IANA name (`time.LoadLocation`), stores the
  unified key and mirrors it into the two legacy keys, so a node running an older
  binary — or an operator reading the raw settings rows — still sees the same
  value. Both job cards keep writing their own key, which can never win over the
  unified one.
- **D2 — the timezone is a cluster wide policy.** It is in `settingsWhitelist`, so
  `CLUSTER_SYNC_SETTINGS` converges every node on one zone: two nodes of a cluster
  must not check certificates at different instants.
- **D3 — the first card becomes "General settings".** It owns the locale/theme
  defaults, the instance name, the clock, the shared timezone and the uptime
  window; its description spells out what it holds, and the "applied only to
  visitors who have not chosen yet" sentence stays as the alert of the instance
  card, where it already was.
- **D4 — `both` is a strategy, not a run.** `LighthouseStrategies` gains `both`;
  `LighthouseRuns` (mobile, desktop) is what the API accepts and
  `LighthouseStrategyTargets(strategy)` expands one into the other, so the daily
  job, the admin run-now and the per-monitor refresh share exactly one definition
  of "the runs this monitor measures". `NormalizeLighthouseStrategy` keeps `both`
  when it is stored, and `TargetsFor` (lighthouse package) turns a monitor into one
  target per run.
- **D5 — one stored row per (monitor, run).** `monitor_lighthouses` is keyed by
  `(monitor_id, strategy)`: the upsert targets both columns, `Get` returns every
  run, `ByMonitorIDs` returns a map of maps and `LastRuns` keeps the newest of them
  (the pair is due together). The single JSON field of the public API
  (`lighthouse`) is kept: it is the run of the monitor's strategy, or the newest
  one for `both`, while `Monitor.Lighthouses` carries the whole map additively.
- **D6 — the primary key is widened by an explicit pass.** `AutoMigrate` never
  rewrites the primary key of a table that already exists (it only adds columns
  and indexes), so `database.Migrate` runs an idempotent
  `ALTER TABLE monitor_lighthouses ... DROP PRIMARY KEY, ADD PRIMARY KEY
  (monitor_id, strategy)` guarded by a read of `information_schema`
  (`internal/database/lighthouse_strategy.go`). Rows written before the change
  survive as they are, and an empty strategy is normalized to the default run
  first (a key column cannot hold a NULL).
- **D7 — narrowing a strategy forgets the dropped run.** Switching a monitor from
  `both` to `mobile` deletes the stored desktop row (`ClearExcept`) instead of
  leaving a score that will never be refreshed.

## 3. Changes

| File | Change |
|---|---|
| `internal/models/security.go` | `SettingTimezone` |
| `internal/services/settings.go` | `Timezone` (legacy fallback), `SetTimezone` (IANA validation + mirroring); `ExpirySettings`/`LighthouseSettings` read the shared zone |
| `internal/services/sync_settings.go` | `SettingTimezone` in the federated whitelist |
| `internal/handlers/public.go` | `timezone` in the admin settings payload and in `PUT /api/admin/settings` |
| `internal/models/lighthouse.go` | `LighthouseStrategyBoth`, `LighthouseRuns`, `LighthouseStrategyTargets`, `MonitorLighthouse` keyed by `(monitor_id, strategy)` |
| `internal/lighthouse/targets.go` | `TargetsFor` (one target per run) and `BuildPlan` over it |
| `internal/services/lighthouse.go` | upsert on `(monitor_id, strategy)`, `Get`/`ByMonitorIDs`/`rowsFor` keyed by strategy, `LastRuns` keeps the newest, `ClearExcept` |
| `internal/services/lighthouse_run.go` | `Refresh` measures every run of the monitor, `Monitors` projects a cadence per run, `Stats` counts monitors (`COUNT(DISTINCT monitor_id)`) |
| `internal/services/monitor.go` | the `Lighthouses` decoration + `primaryLighthouse` |
| `internal/services/monitor_write.go` | a strategy change forgets the dropped run |
| `internal/handlers/lighthouse.go` | run-now answers `results` (one per run) next to `result` |
| `internal/database/lighthouse_strategy.go` (new) | the guarded primary key widening |
| `internal/lighthouse/targets_test.go` | `both` in `TargetsFor` and `BuildPlan` |
| `internal/database/lighthouse_strategy_integration_test.go` (new) | opt-in MariaDB check of the widening, the surviving rows, its idempotence and the per-run upsert |
| `web/src/views/admin/AdminSettingsView.vue` | the "General settings" card (+ timezone, + description); the certificate card loses its timezone |
| `web/src/views/admin/AdminExpiryView.vue` | the "Job settings" stub card is gone |
| `web/src/views/admin/WebPerformanceView.vue` | the embedded job card is gone; one score block per run |
| `web/src/components/lighthouse/LighthouseJobCard.vue` | no timezone field |
| `web/src/lib/monitor-config.ts` | `LIGHTHOUSE_STRATEGY_KEYS`, `LIGHTHOUSE_STRATEGY_TARGETS`, `lighthouseResults`, `both` in the lists |
| `web/src/lib/types.ts`, `web/src/lib/types-platform.ts` | `LighthouseRunStrategy` vs `LighthouseStrategy` (+ `both`), `Monitor.lighthouses`, `LighthouseRunResult.results`, `AdminSettings.timezone` |
| `web/src/views/MonitorDetailView.vue` | one header badge per run |
| `web/src/components/lighthouse/LighthouseScores.vue`, `web/src/components/monitors/MonitorForm.vue`, `web/src/views/admin/AdminMonitorTemplatesView.vue` | the shared strategy labels |
| `web/src/locales/*.json` (7) | `settings.generalTitle`/`generalHelp`/`timezone`/`timezoneHelp`, `monitor.lighthouseStrategyBoth`, the reworded help texts; the dead `expiry.*` and `lighthouse.timezone*` keys are gone |
| `docs/api.md`, `docs/monitors.md`, `docs/database.md`, `docs/clustering-modes.md`, `README.md` | the new strategy, the `lighthouses` field, the shared timezone, the widened key |

## 4. Verification

- `gofmt -l internal/`, `go build ./...`, `go test ./internal/...`.
- The schema change and the per-run upsert were checked against a throwaway
  MariaDB 11 (`UP_SCRATCH_DSN=... go test ./internal/database/ -run
  TestLighthouseStrategyLive`): the legacy table is rebuilt with the composite
  key, the rows it already held survive, a second boot changes nothing and the
  upsert writes one row per run.
- `npm run typecheck`, `npm run check:i18n` (7 locales), `npm run build`.

## 5. Out of scope

- No per-job timezone anymore: an installation has one zone for its daily jobs.
- No third run and no per-monitor throttling.
- No alerting on a score, and no change to what a run measures.

