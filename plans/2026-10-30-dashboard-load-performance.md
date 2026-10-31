# Plan - Dashboard load, Settings and probe-path performance at 1 000 monitors

> **Shipped plan** — task: *"30 s+ Dashboard loads at 1 200 monitors, slow Settings
> pages, slow boot"* on a 180 day retention installation (1 000 monitors is the
> usual deployment, so 1 000 is the design point; 3–5 k was explicitly out of
> scope). Implemented on **2026-10-30**.
>
> Owner decisions taken before coding: (1) 1 000 monitors is the plateau,
> (2) `uptime_7d`/`uptime_30d` may stop being reported *by the listings* (the
> dashboard and the status pages render the selected window only; the data must
> stay available for the single-monitor payloads), (3) **180 days of retention
> stays** — the policy is not reduced, so the purge has to become cheap instead.

## 1. Goal

Make the dashboard, the monitors table and Admin > Settings usable again on a
deployment whose history is a few hundred million heartbeat rows, without
changing a single reported number.

Five independent costs added up:

1. **No index on `heartbeats.created_at`.** The retention purge, its preview and
   the Admin > Settings card all select or aggregate by date alone and walked the
   whole retained history — at boot, every six hours and on every Settings visit.
2. **The listing read 30 days of rollups to render 24 hours.** `DecorateList`
   asked `History` for 24 h/7 d/30 d/selected, i.e. 720 bucket rows per monitor
   (720 000 for 1 000 monitors, ~100 MB) that Go then folded — while the
   dashboard renders one percentage per monitor.
3. **The probe path decorated the monitor before every check.**
   `worker.executeOnce` called `MonitorService.Get`, which reads the statistics,
   bars, certificate, domain, web performance runs, links and votes of the monitor:
   ~14 queries per check, ~280 queries/s at 1 000 monitors, all discarded as soon
   as the probe starts.
4. **The retention purge was one `DELETE`.** On a history that had never been
   pruned that is hundreds of millions of rows in a single statement: one huge
   lock, minutes of undo growth, every probe queued behind it.
5. **The payload was not compressed** and the dashboard re-rendered 1 000 rows on
   every websocket burst.

## 2. Diagnosis (measured, not guessed)

Reproduced against a throwaway MariaDB 11.8 (`docs/development.md` §2) with a
1 000 monitor fixture of 720 hourly buckets each (720 000 rollup rows), the shape
of a 30 day retention:

| Read | Before | After |
|---|---|---|
| Listing, 24 h window (the default) | 1 005 ms just to transfer the buckets, then folded in Go into a ~100 MB slice | **40 ms** |
| Listing, 30 d window | 956 ms transfer + fold | **350 ms** |
| Single monitor, all four windows | 720 rows to Go | 30–40 ms |
| `MIN`/`MAX(created_at)` for the Settings card | full scan of `heartbeats` | index seek |
| Retention purge | one unbounded `DELETE` | 5 000-row chunks |

Two shapes of the SQL aggregate were measured before choosing one:

| Shape (same 30 day range, 1 000 monitors) | Time |
|---|---|
| Plain `SUM(up), SUM(total), …` over one index range | **30 ms** |
| `SUM(CASE WHEN bucket_at >= ? THEN up ELSE 0 END)` per period | 700–1 250 ms |

## 3. Decisions taken

- **`idx_hb_created_at (created_at)` on `heartbeats`**, declared on the model so
  `AutoMigrate` creates it on existing installations. It is the only index that
  does not start with `monitor_id`, and it serves the three reads that cross every
  monitor (purge, purge preview, `MIN`/`MAX(created_at)`). The first boot after
  the upgrade pays one online `ALTER TABLE … ADD INDEX`.
- **The listing asks for the periods it renders.** `StatsService.HistoryForListing`
  runs the same computation on a narrowed window set (`newListingWindowSet`: the
  selected window plus the fixed 24 h one), and `windowRow` reports a period the
  call did not compute as `0` (`totalsOf`) instead of reading another period's
  counters. `StatsService.History` is unchanged for the detail page and
  `GET /api/v1/monitors/:id`, which keep reporting all four fixed windows; the
  public status pages keep calling `DecorateWithWindow` and keep their payload.
- **The server reduces the rollups.** `historyRollupTotals` issues one plain
  aggregate per period (`SUM(up)`, `SUM(total)`, plus the failure counters and
  latency figures for the 24 h period) over its own index range, and folds the
  boundary hours as before. The Go-side fold of the buckets is gone, so a listing
  allocates one row per monitor instead of 720.
- **`MonitorService.GetConfig`** for the probe path: the worker reads the stored
  configuration (and `active`) with one `SELECT`, and `Get` is now `GetConfig` +
  `Decorate`.
- **The purge is chunked** (`purgeChunk = 5 000`, rollups first, then heartbeats,
  stopping on context cancellation). GORM only builds the `LIMIT` of a `DELETE`
  when the dialector declares it — the MySQL one does — so the statement really is
  `DELETE … LIMIT 5000`; `docs/database.md` §5 shows it.
- **The Settings card counts exactly only while that is cheap**
  (`heartbeatExactCountLimit = 2 000 000`, `information_schema.TABLES.TABLE_ROWS`
  above it): `COUNT(*)` has no index that answers it without a full walk.
- **gzip for anything textual** (`middleware.Gzip`): the JSON payloads, the
  embedded SPA assets and the SVG badge. The websocket handshake is never wrapped
  (its response is a hijacked connection), nor is an event stream, a body that is
  already encoded or a partial response (`Content-Range`).
- **20 idle database connections** instead of 5: the decoration runs ~10 reads
  concurrently per listing.
- **180 day retention unchanged** (owner decision). Its cost is addressed by the
  index, the chunked delete and the row estimate, not by keeping less data.

## 4. Files touched

| File | Change |
|---|---|
| `internal/models/heartbeat.go` | `idx_hb_created_at`, index rationale |
| `internal/services/stats.go` | `newListingWindowSet`, `totalsOf`, `counted`, `HistoryForListing`, `historyRollupTotals` (per-period aggregates) |
| `internal/services/stats_series.go` | chunked `Purge`, `HeartbeatStats` without `COUNT(*)` on big tables |
| `internal/services/monitor.go` | `GetConfig`, `decorationHistory`, listing decoration |
| `internal/scheduler/worker.go`, `workers.go` | probe path uses `GetConfig` |
| `internal/database/database.go` | idle connections 5 → 20 |
| `internal/middleware/compress.go` (+ test) | gzip middleware |
| `internal/handlers/router.go` | registers it |
| `internal/services/stats_history_test.go` | listing window set, `row` reporting, `counted` |
| `internal/database/rollup_integration_test.go` | index assertion, listing parity, purge/stats fixture |
| `docs/api.md`, `docs/public-api.md`, `docs/monitors.md`, `docs/database.md`, `docs/architecture.md` | payload, SQL, index, gzip |

## 5. Verification

```bash
go build ./... && go vet ./... && go test ./...

# the rollups, the parity with the raw aggregate, the purge and the index, against
# a throwaway MariaDB (drops and recreates heartbeats: never a real database)
docker run --rm -d --name up-rollup-verify -p 127.0.0.1:3307:3306 \
  -e MARIADB_ROOT_PASSWORD=verify -e MARIADB_DATABASE=up \
  -e MARIADB_USER=up -e MARIADB_PASSWORD=verify mariadb:11
UP_SCRATCH_DSN='up:verify@tcp(127.0.0.1:3307)/up?charset=utf8mb4&parseTime=true&loc=UTC' \
  go test ./internal/database/ -run TestRollupLive -v
docker rm -f up-rollup-verify
```

`TestRollupLive` now also pins that `idx_hb_created_at` exists after
`AutoMigrate`, that `HistoryForListing` reports the selected window and the 24 h
window exactly as `History` does (and zero for the periods it does not read), and
`TestRollupLivePurgeStats` covers a 12 000 row purge (three chunks), the rollups
removed with it and the card that describes what is left.

On a running installation, `LOG_LEVEL=debug` prints `monitors decorated` with
`long_windows`, `votes` and `duration_ms` for every listing load: that line is the
regression check.


The conditional sums look like the cheaper shape (one scan for every period) and
are 20× slower: the server re-converts the `DATETIME` parameter bound to
`bucket_at >= ?` for **every row of every expression**. One plain aggregate per
period, each over its own index range, wins by a wide margin even though it reads
the narrow ranges more than once.


## 6. Rolling it out on an existing deployment

The first boot after the upgrade does more than usual, in this order, before the
HTTP server starts:

1. `idx_hb_created_at` is added to `heartbeats` — an online
   `ALTER TABLE … ADD INDEX` in InnoDB, so it does not block the writes of a
   running cluster, but it reads the whole table: expect it to take a while on a
   multi-gigabyte history and let the boot finish before opening the dashboard;
2. the usual rollup reconciliation (`heartbeat rollups reconciled` in the log);
3. the retention purge now runs in chunks: the first pass after a history that
   had not been pruned takes as long as the deletes take, but no probe is locked
   behind it, and a shutdown interrupts it safely (the next boot resumes).

Rolling back is reverting the image: the new index is inert for the old code, and
the narrower listing changes nothing the old build reads. To reclaim the space by
hand: `ALTER TABLE heartbeats DROP INDEX idx_hb_created_at;`.

**The one visible change**: `uptime_7d` and `uptime_30d` are `0` in the listing
payloads (`GET /api/dashboard`, `GET /api/monitors`, `GET /api/v1/monitors`,
`GET /api/v1/status?include_monitors=true`) and stay reported by
`GET /api/v1/monitors/:id` / `GET /api/monitors/:id`. The fields are still in the
JSON, so a consumer that decodes them keeps working; a consumer that *renders*
them from a listing has to read the single-monitor endpoint.

## 7. What is deliberately left over

- **`RecentBars`** already reads the rollups with one grouped query (bounded by
  monitors × 30 slots), so it did not need to change.
- **`StatsService.Window`** still reads the raw heartbeats of one monitor: it is
  bounded by the selected window and its 95th percentile needs individual latency
  samples.
- **A short-lived snapshot cache** (`singleflight` + 3–5 s TTL over the listing)
  is the next step if the 350 ms of a 30 day window ever matters: it must be keyed
  on the same hour boundaries, or two concurrent loads would disagree.
- **Frontend pagination or virtualization of the monitors table**: the server side
  is now cheap enough that 1 000 rows render in the time the browser takes to
  paint them. Changing that is a UX decision (which rows, which order, which
  filters survive a reload), not a backend one.
- **`AggregateService.ActiveForHost` has no `case "some"`**: a monitor whose
  `run_on` is `some` is probed by *every* node instead of the nodes listed in
  `run_on_nodes`. Not a performance bug of the dashboard, and not touched here
  because it changes which node alerts; it deserves its own change.
- **`ClusterService.Status` embeds the raw cluster `PrivateKey`** in the payload
  the dashboard polls on every load. It is authenticated (session), but it is the
  hottest payload of the interface and the key has no business in it; left for a
  dedicated security change.
- **`monitor.status` is still published for every heartbeat** (~40 events/s at
  1 000 monitors), including when the status did not change; the frontend already
  ignores `changed: false` badges. Publishing only on transition would cut the
  websocket traffic, but the "last check" timestamp of the row also rides on that
  event, so it needs a frontend decision.
