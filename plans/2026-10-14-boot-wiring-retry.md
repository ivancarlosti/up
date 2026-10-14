# Plan - A boot that retries itself (and a "Try now" that really tries)

> Task: make the **whole** server boot sequence self-retrying with a capped
> exponential backoff — the connection, the schema/data steps and the wiring
> (OIDC discovery included) — and make the **Try now** button of the boot screen
> trigger an immediate attempt instead of re-reading a frozen state. Worked on
> **2026-10-14**.

## 1. Goal

`database.Connect` already retried forever (1s → 8s, see
`plans/2026-09-29-fast-startup-and-db-error-ux.md`), but **everything after it
parked in a permanent error**: `serveFailure` logged once, kept the process up
and let `/api/boot` and `/api/health` report a failure that never changed again.
The only way out was a restart. And the boot screen's **Try now** could not help:
the store's `retryBoot()` did `stopPolling()` + `pollStatus()`, which re-read
`/api/boot` — the same frozen answer, because nothing in the process was going to
try again.

Two failures made that concrete:

1. An **OIDC discovery behind a proxy that answers 502** while the provider is
   restarting (or not deployed yet). The container came up, the database was
   fine, and the process sat on `ERR_BOOT_FAILED` until someone restarted it.
2. A migration or a backfill that hits a **lock timeout**, or a replica that is
   briefly read-only. Nothing is wrong with the configuration; the step simply
   needed a second chance.

## 2. Decisions

- **D1 — the retry belongs to the whole boot, with two cadences.** The connection
  keeps its fast schedule (1s → 8s): connecting is cheap, and waiting for a
  database that is still starting usually costs seconds. Every later step uses
  15s, 30s, 1m, 2m, 4m, then **5m for good**. A cause that needs a human (a wrong
  issuer, a missing grant) must not be hammered every second, and a cause that
  needs seconds must not cost minutes.
- **D2 — retrying is safe by construction, so no lock or flag was invented.**
  Every step is idempotent (`Migrate`, `Backfill`, `BackfillTemplateAuth`,
  `ConsolidateSingleGroupMembership`, `Plan/RebuildRollups`, `Seed`,
  `cluster.EnsureSelf`), the single `*gorm.DB` pool is reused (no attempt
  reconnects), and a failed attempt logs and leaves the state readable.
- **D3 — one signal, all the wait loops.** `boot.State.Retry(attempts, code,
  detail, next)` already existed and its doc says "a step that is going to be
  retried"; the loop simply extends its use, and progress resets the counters. No
  change to the `boot` API.
- **D4 — a `prepped` guard.** Once the schema and the data are in place, a
  failure in the wiring retries **only** the wiring: without it the migrations
  and the backfills would run again every five minutes.
- **D5 — the step classifies, the loop records.** `fail()` no longer calls
  `state.Fail` (that call was the frozen failure) — it returns a typed
  `*bootFailure{code, detail, err}` so a step keeps its own fallback code
  (`ERR_DB_MIGRATION_FAILED` for the migration, `ERR_BOOT_FAILED` for the wiring)
  and the loop reads it back with `errors.As` in `recordRetry`. `serveFailure` is
  deleted; `waitForShutdown` stays for the success path.
- **D6 — `boot.Retry`: a buffered(1) kick channel, nil-safe.** Every wait loop
  selects on it next to its timer (`database.Connect` and the boot loop), so one
  mechanism covers every stage. `POST /api/boot/retry` on the bootstrap engine
  (unauthenticated, 6/min, no body) calls `Now()` and answers the same payload as
  `GET /api/boot`, so the SPA repaints from it without a second request.
- **D7 — the WebSocket hub starts last.** `go hub.Run` sat before the fallible
  steps; a retried attempt would have leaked one hub goroutine per attempt, and
  the services of the *second* engine would have kept publishing into the first
  hub. It now starts after the last fallible call (`sched.Start`). Safe because
  `Hub.Publish` never blocks (it drops with a warning when the buffer is full),
  so a service may hold the hub before its loop runs.
- **D8 — no new i18n keys.** The page already renders `boot.retry` and
  `boot.attempts`, and the codes (`ERR_DB_*`, `ERR_BOOT_FAILED`) are ones the
  seven locales already carry: `npm run check:i18n` and the locale files are
  untouched.

## 3. What was built (files)

| Area | Files |
|---|---|
| Retry signal | `internal/boot/retry.go` (new: `Retry`, `NewRetry`, `Now`, `Chan`), `internal/boot/retry_test.go` (new) |
| Boot loop | `cmd/server/main.go` (`bootRetryInitial` / `bootRetryMax` / `nextBackoff`, the two stage retry loop, `bootFailure`, `recordRetry`; `fail` no longer records, `serveFailure` deleted), `cmd/server/main_test.go` (new) |
| Hub | `cmd/server/app.go` (`go hub.Run` moved after `sched.Start`) |
| Connection | `internal/database/database.go` (`Connect(..., retry *boot.Retry)` selects on `retry.Chan()`) |
| HTTP | `internal/handlers/boot.go` (`NewBootstrapEngine(..., retry)` + `POST /api/boot/retry`, `bootRetryRateLimit`) |
| Frontend | `web/src/lib/api-core.ts` (`retryBoot`), `web/src/stores/app.ts` (`retryBoot` is async: POST first, then resume the poll), `web/dist/index.html` (rebuilt) |
| Docs | `docs/api.md` (§1 + new `POST /api/boot/retry`, the `attempts` field), `docs/architecture.md` (§3), `docs/authentication.md` (§5), `docs/development.md` (§11) |

## 4. Verified (not only built)

- **Unit.** `go test ./cmd/... ./internal/...`: `Retry.Now` never blocks and
  collapses duplicate requests, `Chan` fires once, a nil receiver is tolerated;
  `nextBackoff` walks 30s → 1m → 2m → 4m → 5m and stays at 5m; `fail` keeps the
  classified code, names the step and stays `errors.Is`-reachable; `fail` does
  **not** touch the state while `recordRetry` publishes the attempt counters.
  `go build ./...`, `go vet ./...` clean. `npm run typecheck`,
  `npm run check:i18n` (52 codes, 801 keys, 6657 messages) and `npm run build`
  pass.
- **Live end to end**, throwaway `mariadb:11.4` on `127.0.0.1:3399` plus a stub
  issuer answering **502** on `127.0.0.1:3901`, `AUTH_METHOD=keycloak`:
  - `/api/boot` reported `phase:"wiring"`, `code:"ERR_BOOT_FAILED"`,
    `attempts` climbing, `next_retry_ms` 15000 → 30000 → 60000 → 120000 → 240000
    → **300000 (capped)**, `status:"degraded"`, `/api/health` 503 — the process
    stayed up and the log printed one `a boot step failed, retrying` per attempt
    with the attempt, the wait and the provider message.
  - `POST /api/boot/retry` → `200`, the log line `boot retry requested by the web
    interface` and the next failure landed in the **same millisecond**: the wait
    was skipped, not shortened (five POSTs produced five immediate attempts).
  - The rate limit: six POSTs `200`, the seventh `429`.
  - **Recovery:** the stub switched to a real discovery document, one
    `POST /api/boot/retry` → `boot completed` (`wiring_ms: 7`, and the migration
    timings appeared **once**, so the `prepped` guard held), `/api/boot`
    `ready:true`, `/api/health` `200` — no restart.
  - After the swap, `POST /api/boot/retry` answers `404 ERR_ROUTE_NOT_FOUND`, as
    documented.
  - `SIGTERM` while the wiring was retrying, and while `database.Connect` was
    retrying, both exited promptly with **code 0**.
  - With a closed DB port: `ERR_DB_UNREACHABLE`, 8s backoff, and a single kick
    skipped the remaining ~7s (attempts 4 → 5 within a second) — the connection
    loop really does select on the kick.

## 5. Trade-offs and follow ups

- A **permanently** broken step now retries forever (one WARN line per attempt,
  every 5 minutes at worst) instead of parking in a permanent error and printing
  nothing. Judged the better default: `/api/health` stays `503`, the boot screen
  says which step and how long, and a fix needs no restart. An operator who wants
  it to stop signals the process, which still exits cleanly.
- `attempts` / `next_retry_ms` are no longer connection-only fields; `docs/api.md`
  now says so.
- The deferred rollup rebuild keeps its own failure path (`state.Fail`): it runs
  *after* the API serves, where there is no boot loop to retry it.

