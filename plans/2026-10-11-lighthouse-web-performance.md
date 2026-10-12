# Plan - Lighthouse web performance (per monitor and per template)

> Task: score the page of an HTTP/Keyword monitor with Google Lighthouse through
> the PageSpeed Insights API, keep a 30 day history, expose a dedicated "Web
> performance" admin page — and move the TLD/SSL timezone and job settings out of
> the TLD/SSL page into Settings.

## 1. Goal

A monitor can opt into web performance measurement with a new switch
(`lighthouse_enabled`, next to `cert_watch` / `domain_watch`), and a template can
carry it as a default. Every day, at a configurable time and in a configurable
timezone, the job scores every distinct `url + strategy` target through the
PageSpeed Insights API v5 and stores the four category scores (performance,
accessibility, best practices, SEO) plus the Core Web Vitals of the run. The
result is stored per monitor (so a `https://example.com` HTTP monitor and a
keyword monitor on the same URL both show the score) and every run is appended to
a history that the admin page charts.

The admin surface is a new **Web performance** page (sidebar) that lists the
monitors that measure, their last scores and vitals, and per monitor the history
of the retention window. The **Settings** page gains the job configuration
(enabled, time, timezone, strategy, rate limit, timeout, API key, retention) —
including the certificate/domain job settings that used to live on the TLD/SSL
page, which keeps only its target list, its parsers and its run-now action.

## 2. Decisions

- **D1 — PageSpeed Insights API v5 is the engine.** The runner is a small
  `net/http` + `encoding/json` client against
  `https://www.googleapis.com/pagespeedonline/v5/runPagespeed`
  (`internal/lighthouse/psi.go`). No Chromium, no Node, no new Go dependency and
  no Dockerfile change: the image stays as it is and the only requirement is
  egress to `www.googleapis.com:443`. Scores come from
  `lighthouseResult.categories.*.score` (0–1 → 0–100, rounded), the vitals from
  `audits.*.numericValue` (FCP, LCP, TBT, CLS, speed index). A failed run is
  stored as a `status: "error"` observation with the message, exactly like a
  certificate that could not be read.
- **D2 — the daily job mirrors the expiry job.** Same shape as
  `services.ExpiryService`: the worklist is built from the monitors (dedup by
  `url + strategy`, like `expiry.CertificateTarget.Key()`), the run happens at
  `check_time` in `check_timezone` (defaults `04:00` / `UTC`), the day bucket and
  the cluster lock come from `models.NextDailyRun` / `RunInstant` /
  `cluster.ClaimDailyJob`, a one-minute ticker in `scheduler/maintenance.go`
  calls `RunDue` and the admin page can run the job now. The outbound calls are
  paced by a limiter keyed by host (`DefaultLighthouseRateLimitMS = 60000`:
  PSI's anonymous quota is harsh, and the operator lowers it with an API key).
- **D3 — per monitor in, deduplicated out.** `Monitor.LighthouseEnabled` decides
  who is measured and `MonitorConfig.LighthouseStrategy` (`mobile` / `desktop`,
  default `mobile`) what is measured; the *target* is the normalized monitor URL
  plus the strategy, so two monitors on the same page cost ONE API call and both
  get the score. `MonitorType.SupportsLighthouse()` gates the switch to `http`
  and `keyword` (the types with a real HTTP(S) page).
- **D4 — two tables.** `monitor_lighthouses` holds the last observation of a
  monitor (the `lighthouse` decoration of the API payload, mirroring
  `monitor_certificates`), `lighthouse_runs` holds the history the chart reads.
  The history is bounded by `lighthouse_retention_days` (default **30**,
  `0` = keep forever, `LIGHTHOUSE_RETENTION_DAYS` as the env fallback) and is
  purged by the existing six-hour retention tick.
- **D5 — the flag is federated, the history is not.** `lighthouse_enabled` and
  the strategy travel with the monitor (they are columns) and are published by
  the sync outbox like every other monitor field; the scores and the runs are
  observations, exactly like heartbeats and certificates, so they stay local to
  the node that measured and are never pushed to peers.
- **D6 — the settings move is a UI move.** `AdminSettingsView.vue` gains a
  "Certificate & domain expiration" card (the fields copied from
  `AdminExpiryView.vue`) and a "Web performance (Lighthouse)" card;
  `AdminExpiryView.vue` keeps the target list, the WHOIS parsers and the
  run-now button, and its settings card becomes a short note linking to
  Settings. The API (`GET/PUT /api/admin/expiry`) is untouched, so nothing else
  breaks.
- **D7 — the new page does not touch the dashboard.** `DashboardView.vue` stays
  as it is; the page is reached from the sidebar, and the monitor form/detail is
  the only other place a score appears.
- **D8 — no alerting in v1.** A score is a trend, not an outage: no
  notification channel is wired to a threshold (the page shows the values and
  the deltas). Notifications stay the certificate/domain job's business.
- **D9 — enabling the switch gives immediate feedback.** Turning
  `lighthouse_enabled` on (and a change of target or strategy) enqueues one
  asynchronous, rate-limited run for that monitor, so the operator does not wait
  a whole day to see whether the API key works.

## 3. Changes

| file | change |
|---|---|
| `internal/models/lighthouse.go` (new) | `LighthouseSettings` (+ defaults/normalize/validate), strategies, `LighthouseResult`/`LighthouseMeasurements`, `MonitorLighthouse` (table `monitor_lighthouses`), `LighthouseRun` (table `lighthouse_runs`) |
| `internal/models/security.go` | `SettingLighthouse*` keys (enabled, check time/timezone, strategy, rate limit, timeout, api key, retention days, last run day) |
| `internal/models/maintenance.go` | `DefaultLighthouseRetentionDays = 30`, `MaxLighthouseRetentionDays`, `NormalizeLighthouseRetentionDays` |
| `internal/models/enums.go` | `MonitorType.SupportsLighthouse()` (http, keyword) |
| `internal/models/monitor.go` | `LighthouseEnabled` column + `Lighthouse *LighthouseResult` runtime field |
| `internal/models/monitor_config.go` | `LighthouseStrategy` + `Normalize` + `PruneToType` (http/keyword) |
| `internal/models/monitor_template.go` | `Defaults.LighthouseEnabled`/`LighthouseStrategy`, `TemplateDefaultFields`, validation, the template default copy list |
| `internal/lighthouse/psi.go` (new) | `Runner` (base URL, client, api key), `Run(ctx, Target)`, response parsing (scores + vitals), error mapping |
| `internal/lighthouse/targets.go` (new) | `Target{URL,Strategy}` + `Key()`, `NormalizeURL`, `TargetFor(monitor)`, `BuildPlan(monitors)` |
| `internal/lighthouse/limiter.go` (new) | the host-keyed pacing helper (same semantics as `expiry.Limiter`) |
| `internal/lighthouse/*_test.go` (new) | targets (normalization, dedup, strategy), PSI parsing against `httptest` (ok, error object, HTTP 500, missing categories), limiter pacing |
| `internal/services/lighthouse.go` (new) | `LighthouseService` storage: `Record`, `Get`, `ByMonitorIDs`, `History`, `Purge`, `Stats`, `Clear` |
| `internal/services/lighthouse_run.go` (new) | `Settings`, `RunDue`, `RunNow`, `Refresh`, `Enqueue`, the worklist and the fan-out to the monitors that share a target |
| `internal/services/settings.go` | `LighthouseSettings`/`SetLighthouseSettings`, `LighthouseLastRunDay`/`SetLighthouseLastRunDay`, `LighthouseRetentionDays`/`SetLighthouseRetentionDays` |
| `internal/services/monitor.go` | `lighthouseFor` + the `lighthouse` decoration in `decorate`/`applySources`; `SetLighthouseService` |
| `internal/services/monitor_validate.go` | `lighthouse_enabled` type guard, strategy validation, clearing the configuration when the switch is off |
| `internal/services/monitor_write.go`, `monitor_bulk_create.go`, `monitor_clone.go`, `monitor_template_apply.go` | carry the new field and strategy, enqueue the first run |
| `internal/scheduler/scheduler.go`, `maintenance.go` | `SetLighthouseService`, the one-minute ticker (boot log + `RunDue`) and the retention purge |
| `internal/handlers/lighthouse.go` (new) | `lighthouseSettings`, `updateLighthouseSettings`, `runLighthouseNow`, `lighthouseMonitors`, `lighthouseMonitorHistory`, `runLighthouseMonitor`, `purgeLighthouse` |
| `internal/handlers/router.go`, `router_admin.go` | `Container.Lighthouse`, the `/api/admin/lighthouse` group |
| `internal/database/database.go` | the two new tables in `MigrationModels()` |
| `internal/config/config.go` | `LIGHTHOUSE_API_KEY`, `LIGHTHOUSE_RETENTION_DAYS` |
| `cmd/server/app.go` | build the service, wire it into the monitor service, the scheduler and the container |

## 4. Verification

- `gofmt -l .`, `go build ./...`, `go vet ./internal/...`, `go test ./internal/...`
  (the lighthouse package tests point the runner at an `httptest` server, so they
  never reach Google).
- `npm run typecheck`, `npm run check:i18n`, `npm run build` (in `node:22-alpine`,
  the host node is 18).
- Manual smoke: enable the switch on an HTTP monitor, press "Run now", check the
  page shows the scores and that a second "Run now" within the rate limit window
  is paced. A real PSI call is the only step that needs egress to
  `www.googleapis.com`.

## 5. Out of scope

- No alerting on a score (see D8) and no per monitor score threshold.
- No dashboard widget (see D7) and no status page score.
- No field data (CrUX) and no trends beyond the retention window.
- No Chromium/Node based self hosted Lighthouse, and no change to the image.
- No i18n of the Lighthouse report itself (the scores and the vitals are the
  only data stored).
