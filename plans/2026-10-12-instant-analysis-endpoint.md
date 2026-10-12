# Plan — On-demand target analysis (`POST /api/v1/analyze`)

> Task: a stateless, token-authenticated endpoint that runs the probes Up already
> has (uptime/HTTP, TLS certificate, domain expiry, web performance) against a
> target **the caller types in**, without creating a monitor, storing a heartbeat
> or firing a notification.

## 1. Goal

One call answers "what does this target look like right now?":

```bash
curl -s -X POST https://up.example.com/api/v1/analyze \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"type":"http","url":"https://example.com","checks":["uptime","certificate"]}'
```

The response carries one section per requested check. Nothing is written to the
database: no monitor, no heartbeat, no certificate/domain/lighthouse row, no
notification. The endpoint is the script-friendly counterpart of the dashboard's
"check now" (which needs an existing monitor) and of the daily jobs.

## 2. Decisions

- **Stateless by construction.** No insert, no update, no delete, no publish.
  Every observation is returned and thrown away, so the endpoint can never
  pollute the statistics, the incident history or the notification inbox.
- **Auth: an existing `read` token** (`middleware.RequireToken(h.Tokens,
  models.ScopeRead)`), the same door as the other `/api/v1` reads. No new scope,
  no new auth mechanism, no schema change (`token_scopes` is untouched).
  - **SSRF note (documented in `docs/public-api.md`).** A read token can now make
    the node open an outbound connection to an arbitrary `host:port`. Moving the
    route to `ScopeWrite` is a one-line change if that is too generous; the plan
    keeps `read` for consistency with the rest of the read API and because the
    dashboard already probes operator-typed targets from a browser session.
- **Validation parity with a monitor.** The body embeds `models.MonitorConfig`
  (flattened: `url`, `host`, `headers`, ...) and the service runs the *same*
  `Config.Normalize(type)` + `Config.Validate(type)` the monitor write path uses
  (`MonitorService.Validate`), so "valid for a monitor" and "valid for an
  analysis" cannot drift. Only the monitor-only concepts are relaxed: no `name`,
  no `interval_seconds`, no tags, no template.
- **The switches are implied by the checks.** The probes read
  `monitor.CertWatch` / `DomainWatch` / `LighthouseEnabled` (via
  `expiry.CertificateTargetFor`, `lighthouse.TargetsFor`), so the service sets
  them from the requested `checks` instead of asking the caller for monitor
  plumbing: `checks:["certificate"]` means "read the certificate", full stop.
- **A failed probe is data, not an error.** Each section carries its own
  `status` (`ok`/`error`) and a message. A bad request (unknown check, invalid
  config) is a `400`; a target that refuses to answer is a `200` whose section
  says why. This is the same philosophy as the daily jobs, which store a
  `status: error` observation instead of failing the run.
- **Probes run concurrently** (`sync.WaitGroup`, one goroutine per section).
  The certificate/domain/performance work is I/O bound and independent, so the
  wall clock is the slowest probe, not their sum. No error channel: a goroutine
  can only write its own section, so there is nothing to collect and no shared
  state to guard (the timings struct has one field per probe for the same
  reason).
- **No global deadline; each probe keeps its own bound.** `checkers.Check`
  honours `timeout_seconds`, `checkers.ProbeCertificate` gets the same value, the
  domain resolver uses the expiry settings timeout and PageSpeed Insights uses
  the job timeout. The HTTP server has `WriteTimeout: 0`, so a slow
  `performance` check is not cut off; a disconnected client cancels the request
  context and every probe aborts.
- **Retries are bounded and synchronous.** `retries` (0..5) +
  `retries_interval_seconds` (1..60, default 5) reuse the scheduler's semantics
  (retry only while the probe is `down`) with tighter bounds than a monitor's
  (0..50 / 1..3600): the caller is holding the connection open.
- **Default checks: `uptime`, plus `certificate` when the TARGET has one.** A
  caller who names no check gets the cheap answer; `domain` and `performance` are
  always opt-in (a registry lookup and a quota unit are not free). The implicit
  certificate is decided by the target and not by the type alone: an http monitor
  pointed at an `http://` url supports the check as a type but has no TLS
  endpoint, and defaulting to it would answer every such request with a section
  that can only say "there is no certificate here" (`analysisSupport`).
- **The web performance service gains a non-persisting measurement.**
  `LighthouseService.MeasureTarget` shares the job's limiter slot and settings
  (key, timeout, pace) but stores nothing and returns the API error to the
  caller. A `psiBaseURL` field (the same seam `lighthouse.Runner` already has)
  lets a test point both paths at a local stub.
- **No frontend work.** The endpoint is for scripts, CI and dashboards, and the
  response reuses the models the UI already renders (`CertificateInfo`,
  `DomainInfo`, `LighthouseResult`).

## 3. Changes

| file | change |
|---|---|
| `internal/services/analysis.go` (new) | `AnalysisService`, `AnalysisRequest`, `AnalysisResult` + the four section types, `Analyze`, `prepare` (validation + in-memory monitor), `runUptime` / `runCertificate` / `runDomain` / `runPerformance`, `resolveChecks` |
| `internal/services/lighthouse_run.go` | `MeasureTarget` (non-persisting, paced), `runner()` helper extracted from `refreshTarget`, `measurementSettings()` |
| `internal/services/lighthouse.go` | `psiBaseURL` field (the endpoint override used by tests) |
| `internal/handlers/analyze.go` (new) | `analyzePayload` (embeds `models.MonitorConfig`, flattened) and `apiAnalyze` |
| `internal/handlers/analyze_test.go` (new) | route registration, the wire payload, a malformed body, the validation codes, the unwired service |
| `internal/handlers/router.go` | `Container.Analysis` field + `v1.POST("/analyze", RequireToken(..., ScopeRead), h.apiAnalyze)` |
| `cmd/server/app.go` | build `services.NewAnalysisService(cfg, log, lighthouses, domains)` and put it in the container |
| `internal/services/analysis_test.go` (new) | validation (type, checks, config, bounds), check resolution and defaults, uptime section, certificate section, the per-section failures, performance against a stub PSI |
| `internal/models/security.go` | the `ScopeRead` comment names the endpoint |
| `docs/public-api.md` | §"POST /api/v1/analyze": the payload, every option (required/optional), the response, the error table and copy-paste recipes |
| `docs/api.md`, `docs/security.md` | the route table and the scope summary name the endpoint |

## 4. Verification (what was actually run)

- `go build ./...`, `go vet ./...`, `gofmt -l internal cmd` (the two files listed
  by the last one, `internal/database/rollup.go` and `cmd/server/main.go`, were
  already unformatted before this change and are untouched).
- `go test ./... -count=1` → every package ok.
- `go test ./internal/services/ -run 'TestPrepare|TestAnalyze' -v` (12 cases, 22
  subtests) and `go test ./internal/handlers/ -run TestAnalyze -v` (5 cases, 4
  subtests): offline, no database, no Google call (the PSI cases run against a
  local stub through `psiBaseURL`).
- The `curl` walkthrough documented in `docs/public-api.md` §3 - §4 (uptime only,
  certificate, domain, performance, a `400` on an unknown check, a `200` whose
  section carries `status: "error"`).

## 5. Out of scope

- Persisting an analysis (that is what a monitor is for) and the
  `?store=true` variant.
- A batch form (`POST /analyze` over several targets) and a streaming/async
  form; the endpoint stays one target, one call.
- Rendering the analysis in the dashboard (a "test this target" button reusing
  the same service is the natural follow-up).
- An IP allow/deny list for outbound analysis targets: the behaviour is
  documented (`docs/public-api.md` §5) and the route is one line away from
  `ScopeWrite` if an operator wants the stricter door.
- The `ERR_MONITOR_DOMAIN_INVALID` branch of `checkProblem` mirrors the monitor
  write path but is unreachable today: all six types support a domain watch, so a
  `domain` check is always accepted (`TestAnalyzeAcceptsTheDomainCheckEverywhere`
  pins that).

