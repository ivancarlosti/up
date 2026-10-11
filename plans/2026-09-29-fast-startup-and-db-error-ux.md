# Plan - Fast, self explaining startup (and the three database failures told apart)

> **Plan** — task: *make the startup of the app fast and self explaining: the SPA must be
> served immediately (the port must never be closed), a loading/boot screen must show the
> phase the server is in, and the three database failures (unreachable, wrong credentials,
> missing schema) must produce a friendly, classified message instead of a blank page*,
> worked on **2026-09-29**. Working note (`plans/`, tracked in git), kept because
> §2 records the measurements the design was chosen from and §3 records the two decisions
> that are hard to reverse (retry forever instead of exiting, and the deferred rollup).

## 1. Goal (as asked)

1. **The SPA answers from the first millisecond.** Serving the page must not depend on the
   database: every start has to show something, even when the database is not there yet.
2. **The start explains itself.** While the process prepares the database, the page says which
   step is running and how long it has been running, and it leaves that screen on its own.
3. **The three database failures are distinguishable in the browser**: the server is
   unreachable (`DB_HOST`/`DB_PORT`), the credentials were refused
   (`DB_USERNAME`/`DB_PASSWORD`) and the schema does not exist (`DB_DATABASE`). Each one names
   the variables to check, in the visitor's language.
4. **The same story once it is serving**: a database that goes away after the boot is reported
   by the interface (banner) instead of by half loaded pages.

## 2. What the measurements said (Phase 0)

The steps that used to run before the listener opened, measured against a throwaway MariaDB
11.8 (`UP_SCRATCH_DSN`, `127.0.0.1:3307`) with 500 000 heartbeats and 35 985 rollup buckets:

| Boot | Scenario | Ready at | Notes |
|---|---|---|---|
| A | brand new schema | 850 ms | `migrate` 782 ms (29 tables), `seed` 51 ms |
| B | 500k heartbeats, empty rollup table (first boot after the upgrade) | 378 ms | the full 30 day rebuild took **2 396 ms** and used to sit in the critical path |
| C | warm (rollups present) | 623 ms | trailing heal 267 ms, SPA served in parallel |

Before this change every one of those numbers was time with a **closed port**: the browser
showed `ERR_CONNECTION_REFUSED`, and a database that never came back produced 90 seconds of
connection errors followed by an exit and a restart loop - visually identical for all three
failures. Two conclusions drove the design:

- The migrations (≈0.3-0.8 s) are cheap enough to stay on the critical path; the **full rollup
  rebuild** (~2.4 s here, hours on a big instance) is not, so it moves after the swap.
- The classification must be **typed** (driver error numbers, `net.Error`), because the user
  facing difference between "refused" and "wrong password" is the whole feature.


## 3. Decisions taken

- **The port never closes: `handlerSwitcher` + bootstrap engine.** `http.Server.Handler` is a
  `handlerSwitcher` holding an `atomic.Pointer[gin.Engine]`. The bootstrap engine
  (`internal/handlers/bootstrap.go`) serves the embedded SPA, `/api/boot`, `/api/health`,
  `/api/version` and answers every other `/api/*` with `503` + a boot code; once the wired
  engine exists the pointer is swapped, without closing a connection (`/api/ws` handshakes and
  the SPA poll survive).
- **`GET /api/boot` always answers 200, `GET /api/health` keeps the status code contract.**
  The SPA polls `/api/boot` (a 503 would fill the console of every waiting browser), the
  container health check keeps reading the status code of `/api/health` (503 while not ready,
  or when the live `Ping` fails). Both answer the same body, so there is one shape to keep in
  sync (`docs/api.md` §2).
- **`database.Connect` retries forever (1s → 8s) instead of exiting after 90s.** A process
  without a database cannot serve, but it is the only thing that can *explain* why, and the
  page is already open. Exiting only traded a readable page for a restart counter. The same
  rule applies to a failed boot step: the process stays up, `/api/health` stays 503, the SPA
  reports the failure (`main.go`, `serveFailure`).
- **Typed classification, in `internal/boot/classify.go`.** `mysql.MySQLError` numbers
  (1044/1045/1130/1698 → credentials, 1049 → missing schema, 1042/1142/1143/1227 →
  migration rights, anything else → `ERR_DATABASE`) and `net.Error` (dial refused, timeout,
  DNS) for the transport. `github.com/go-sql-driver/mysql` became a direct dependency - no new
  module, it was already in the tree.
- **The interface only shows the boot screen for those codes.** `isBootUnavailable()` requires
  a typed `APIError` with `status === 503` and a `ERR_BOOT_*`/`ERR_DB_*` code. A network level
  failure (a proxy in front of a stopped process, `npm run smoke` without a backend) keeps the
  normal routing, so the login page stays reachable in the only case where it is still useful.
- **A parked navigation instead of a redirect.** The router guard awaits
  `app.waitForBoot()`, so a deep link (`/admin/cluster`) survives the boot screen and lands
  where it was asked once the API answers - instead of bouncing through `/login` with an API
  that cannot verify the session.
- **The full horizon rollup rebuild is deferred, and does not need a lock.** The plan's range
  is `[start, floor)` where `floor = now truncated to the hour`; the live probes only ever
  upsert the bucket of the current hour (`heartbeatRollupUpsert`), so the two ranges are
  disjoint by construction. Only `RollupPlan.Full` (empty rollup table = first boot after the
  upgrade) is deferred; the ordinary trailing heal stays synchronous (267 ms, measured).
  `maintenance: "rollups"` reports it on `/api/boot`; the numbers fill in as it goes.
- **The runtime loss is detected by `/api/health`, not by `/api/boot`.** While the process
  serves, nothing writes to the boot state, so polling `/api/boot` would never notice that the
  database went away. The store polls `/api/boot` while booting and `/api/health` (the live

## 4. What was built (files)

| Area | Files |
|---|---|
| Boot state + classification | `internal/boot/state.go`, `internal/boot/classify.go` (+ `state_test.go`, `classify_test.go`) |
| Database | `internal/database/database.go` (`Connect(ctx, cfg, log, state)`), `rollup.go` (`RollupPlan`, `PlanRollups`, `RebuildRollups`) |
| HTTP | `internal/handlers/boot.go` (bootstrap engine, payload, `/api/boot`), `public.go` (`health`), `router.go` (`Container.State`) |
| Process | `cmd/server/main.go` (`prepareDatabase`, `startDeferredRollups`, `serveFailure`, `boot completed` timings), `cmd/server/app.go` (`handlerSwitcher`, `startHTTP`, `shutdownHTTP`) |
| Codes + copy | `internal/i18n/errors.go`, 7 × `web/src/locales/*.json` (`boot.*` + 6 codes) |
| Frontend | `web/src/stores/app.ts` (boot state machine, polling, `waitForBoot`), `web/src/views/BootView.vue`, `web/src/App.vue` (gate + degraded banner), `web/src/lib/{api-core,types-platform,errors}.ts`, `web/src/router/index.ts`, `web/src/i18n.ts` (`applyPreferredLocale`), `web/src/main.ts` + `web/index.html` (inline splash) |
| Docs | `docs/api.md` (§2, §11), `docs/architecture.md` (§3), `docs/development.md` (§11) |

## 5. Verified (not only built)

- `go build ./...`, `go vet ./...`, `go test ./internal/...` (the classification and the
  snapshot transitions are unit tested; the rollup integration test still passes through
  `BackfillHeartbeatRollups`).
- The three failures against a real MariaDB, end to end with `curl`: `ERR_DB_UNREACHABLE`
  (`dial tcp 127.0.0.1:3311: connect: connection refused`, attempts climbing, process still
  alive after 17 s), `ERR_DB_CREDENTIALS` (error 1045), `ERR_DB_MISSING` (error 1049 after
  granting the account the schema) - each one `503` on `/api/health`, `200` on `/api/boot`, and
  the SPA served on `/` with `200`.
- Headless Chrome against the real binary with a dead database (`up-e2e-runner`): `#app`
  rendered the boot screen and the text "Up cannot reach its database", **0 console errors**.
- The recovery loop: the database came back behind a TCP proxy while the page was open and the
  interface reached the dashboard **without a reload**; then the proxy died and the shell
  showed the degraded banner ~30 s later (the health poll), while `/api/health` answered
  `503 ERR_DB_UNREACHABLE`.
- `npm run typecheck`, `npm run check:i18n` (51 codes, 692 keys, 822 messages per locale),
  `npm run build`, `npm run smoke` (built bundle served statically, no backend: the login page
  is still reached, `#up-splash` removed, 0 console errors).

## 6. Follow ups (out of scope, deliberately)

- The inline splash of `index.html` is English only (it is rendered before any bundle exists);
  localizing it would mean inlining every locale into the HTML.
- `maintenance: "rollups"` is invisible in the UI today: the deferred pass starts *after* the
  swap, so the boot screen is already gone. It is kept in the payload as the machine readable
  answer for monitoring, and for a future "the statistics are still filling in" notice.

  ping, 30s) once serving, and both feed the same `boot` ref the shell's banner reads.
