# Plan - The strategy of a Lighthouse result, a results page and calmer defaults

> Task: fix the web performance results that were stored (and displayed) without
> their run, so a monitor measured on **both** really keeps a mobile *and* a
> desktop score; simplify the row of the listing to the scores themselves; add a
> page with the details of a measured page (vitals, audited URL, run history);
> and change two defaults — the PageSpeed Insights pacing and the cadence of a
> monitor that picks none.

## 1. The bug: a desktop score filed as mobile

`LighthouseService.Record` identifies the observation it upserts by
`(monitor_id, strategy)` (`monitor_lighthouses` is keyed that way since the
"both" strategy shipped). The strategy it reads comes from the
`models.LighthouseResult` it is handed — and the successful path never set it:

    internal/lighthouse/psi.go
      payload.result()          // no strategy on the result
      -> LighthouseResult{Status: ok, FinalURL: ..., scores...}

`Record` normalizes an empty strategy to `DefaultLighthouseStrategy` (`mobile`)
for the current row and stored `strategy: ''` in `lighthouse_runs`. The
consequences, all of them silent:

1. the desktop observation of a "both" monitor was **upserted onto the mobile
   row**: `monitor_lighthouses` kept one row, with the desktop scores in it;
2. the listing, the badges and the monitor detail rendered one block per run of
   the *configuration* — so the mobile block showed the desktop number and the
   desktop block showed nothing;
3. every history row of a successful run carried `strategy: ''`, so the run could
   not be told apart from the other one.

The failure path was already correct (`failedResult` stamps the strategy), which
is why only successful runs were affected.

### The fix

- `psiResponse.result(strategy string)` takes the run from the **target** and
  stamps it (`models.NormalizeLighthouseStrategy`), and `Run` passes
  `target.Strategy`. The strategy is a property of the request — the API echoes
  it back inside `lighthouseResult.configSettings`, which Up does not decode — and
  the caller keys the stored row by it.
- `LighthouseService.Record` normalizes the strategy of the **history** row with
  the same helper the current row uses, so a result that arrives without one (an
  older node during a rolling upgrade) lands with the run it was upserted into
  instead of in a column no filter can name.

### What it does not do

- **No migration.** `monitor_lighthouses` already has the composite primary key;
  the next run of a "both" monitor upserts the mobile row and *inserts* the
  desktop one, which is the self-healing the table was designed for.
- **No backfill of `lighthouse_runs.strategy = ''`.** Those rows cannot be
  attributed: an empty strategy is both "stored before the column existed" (see
  `5db4afb`) and "stored by the bug", and rewriting the second kind would need a
  guess. The chart filters on the runs it can name and the empty ones are read as
  the mobile run they were upserted with.

## 2. Decisions

- **D1 — the run travels with the target.** `Target` is what the deduplicated
  worklist is built from, so the strategy is stamped where the target is (the
  runner), never re-derived from the answer.
- **D2 — one normalization, two rows.** `Record` runs
  `NormalizeLighthouseStrategy` on both the current row and the history row: the
  two can no longer disagree, whatever a node sends.
- **D3 — the row of the listing keeps the scores only.** `LighthouseScores`
  already had a `compact` mode that drops the vitals and the "Measured ..." line;
  the listing now passes it (and the scores column shrinks from `sm:min-w-72` to
  `sm:min-w-56`). A "both" monitor shows its two blocks of four chips and their
  Mobile/Desktop badge — nothing else.
- **D4 — the details get a page, the dialog stays.** The *History* dialog answers
  "how did this page move lately?" without leaving the table, so it is kept as it
  is. The row also gets a **Results** link to
  `/admin/web-performance/:id` (`admin-web-performance-monitor`) that carries what
  the table has no room for: the vitals of each run, the audited URL, the node
  that ran it, the whole run history with every number, and the retention note.
- **D5 — the chart is strategy aware.** `LighthouseHistoryChart` draws one run at
  a time when the window holds more than one: a selector (*Every run* / *Mobile* /
  *Desktop*) that only appears then, a tooltip that names the run when the series
  are mixed, and a legend (*Latest / Best / Worst / Runs*) that follows the
  selection. Two interleaved series in a CSS bar chart read as noise.
- **D6 — two calmer defaults.** `DefaultLighthouseRateLimitMS` 60000 → **300000**
  (one call every five minutes: the anonymous quota is harsh and a run costs the
  API tens of seconds), and `DefaultLighthouseInterval` `daily` → **`weekly`** (the
  pace a page really changes at). Both are only *defaults*: a stored value is
  never rewritten, and the settings page still edits both.
- **D7 — the monitor detail badges link here too.** The score badges of
  `MonitorDetailView` became links to the same page (the `RouterLink` carries the
  aria-label, the badge keeps its score tooltip), which is the natural place to
  answer "what are the vitals behind this number?".

## 3. Files touched

| Layer | File | Change |
|---|---|---|
| Lighthouse | `internal/lighthouse/psi.go` | `Run` passes `target.Strategy`; `result(strategy)` stamps it with a comment on why |
| Lighthouse | `internal/lighthouse/psi_test.go` | the existing parse test asserts the mobile strategy; new `TestRunnerRunKeepsTheTargetStrategy` (mobile, desktop, `DESKTOP`, empty → default) |
| Service | `internal/services/lighthouse.go` | `Record` normalizes the strategy of the history row too |
| Service | `internal/services/lighthouse_both_strategy_integration_test.go` | new opt-in (`UP_SCRATCH_DSN`) `TestLighthouseBothRunsLive`: two current rows, two history rows with their run, an unlabelled run filed under the default, `ClearExcept` keeping one row |
| Models | `internal/models/lighthouse.go`, `lighthouse_schedule.go` | `DefaultLighthouseRateLimitMS = 300000`, `DefaultLighthouseInterval = weekly` |
| Models | `internal/models/lighthouse_schedule_test.go` | the fallback duration is expressed as `LighthouseIntervalDuration(DefaultLighthouseInterval)` instead of a literal 24h |
| Web | `web/src/views/admin/WebPerformanceView.vue` | `compact` scores in the row, narrower scores column, **Results** link per row |
| Web | `web/src/views/admin/WebPerformanceDetailView.vue` | new page: two result blocks (vitals + audited URL + rendered at + node), the chart, the run history table, the retention note, "Measure now" and "Open monitor" |
| Web | `web/src/router/index.ts` | `admin-web-performance-monitor` (`/admin/web-performance/:id`) |
| Web | `web/src/components/layout/AppSidebar.vue` | `also: ['admin-web-performance-monitor']` keeps *Web performance* highlighted on the detail page |
| Web | `web/src/components/lighthouse/LighthouseHistoryChart.vue` | strategy selector, run in the tooltip, legend follows the selection |
| Web | `web/src/views/MonitorDetailView.vue` | the score badges link to the results page |
| Web | `web/src/lib/monitor-config.ts`, `types.ts`, `LighthouseJobCard.vue`, `MonitorForm.vue`, `AdminMonitorTemplatesView.vue`, `WebPerformanceView.vue` | the weekly default and the 300000 ms fallback |
| Web | `web/src/locales/*.json` (7 locales) | `rateLimitHelp` updated; 12 new keys: `backToList`, `details`, `results`, `runHistory`, `runAt`, `strategy`, `strategyAll`, `finalUrl`, `fetchTime`, `node`, `openMonitor`, `retentionNote` |
| Docs | `docs/monitors.md`, `docs/api.md` | the `weekly` default cadence, the 300000 ms pacing, the results page of a measured monitor |

## 4. Verification

1. `go build ./... && go vet ./... && go test ./internal/...`.
2. `UP_SCRATCH_DSN=… go test ./internal/services/ -run TestLighthouseBothRunsLive -v`
   against a real MariaDB (the upsert's composite key is what the test pins):

       docker run --rm -d --name up-lighthouse-verify -p 127.0.0.1:3309:3306 \
         -e MARIADB_ROOT_PASSWORD=root -e MARIADB_DATABASE=up mariadb:11
       # let the app migrate that database once (any node pointed at it does it at boot)
       UP_SCRATCH_DSN='root:root@tcp(127.0.0.1:3309)/up?charset=utf8mb4&parseTime=true&loc=UTC' \
         go test ./internal/services/ -run TestLighthouseBothRunsLive -v
       docker rm -f up-lighthouse-verify

3. `npm run typecheck && npm run check:i18n && npm run build` in `web/`.
4. Live pass (headless Chrome over CDP, clean instance, a "both" monitor with two
   stored runs and eight history rows): the listing shows two score blocks and no
   vitals, the **Results** link opens `/admin/web-performance/:id`, the page shows
   the four vitals, the audited URL and the eight runs (one flagged as failed),
   the chart selector filters the window from eight bars to the four of a run, the
   sidebar keeps *Web performance* highlighted, and neither page logs a console
   error. Screenshots reviewed at 1440 px.

## 5. Not done, on purpose

- **The `strategy: ''` history rows are left alone** (see §1): they cannot be
  attributed.
- **A per-run backfill of the current rows** is unnecessary: the next run of the
  monitor writes the missing row, and a monitor that never runs again shows the
  score it has (which is the mobile upsert it always was).
- **The history dialog stays** in the listing, unchanged.
- **The dashboard and the monitor table are untouched**: they already render one
  block per run through `lighthouseResults`.
