# Plan - Heartbeat statistics: covering index + hourly rollups

> **Shipped plan** — task: *SLOW SQL (1.3–6.1 s) on `StatsService.History`*
> reported from a production-ish log (`internal/services/stats.go:101`, GORM's 500 ms
> `SlowThreshold` of `internal/database/database.go:42`). Implemented on **2026-09-26**
> (staged, not yet committed). Folder tracked in git (the `/plans/` exclusion was
> removed on 2026-10-11), excluded from the Docker build context (`.dockerignore`).

## 1. Goal

Every dashboard load, monitor detail page and public-API hit recomputed the 24 h / 7 d /
30 d uptime and latency windows by scanning **every heartbeat of the last 30 days**. The
query was correct, it was just doing all the work at read time:

- one monitor checked every 60 s stores **43 200 rows** in 30 days, 518 400 at a 5 s
  interval, and a two-node cluster doubles it;
- `idx_hb_monitor_time (monitor_id, created_at)` selected the rows but did not carry
  `status` or `latency_ms`, so MySQL fetched **one clustered row per heartbeat** to read
  them. A single-monitor request (`monitor_id IN (206)` from
  `internal/handlers/publicapi.go:118`) paid that cost on its own.

Two goals, in this order: (1) stop the pathological read, (2) make the numbers provably
identical, because `StatsService` is the one implementation behind the dashboard, the
status pages and the public API (`internal/services/stats.go:13-16`).

## 2. Diagnosis (measured, not guessed)

Reproduced in a throwaway MariaDB 11.8 container with a 30 day fixture (one monitor,
60 s interval, 86 400 rows for two monitors) using `SHOW SESSION STATUS` row counters,
which do not depend on cache state:

| Path | Plan | Rows touched |
|---|---|---|
| Raw aggregate, no covering index | `type=ALL`, `rows=86200` | 86 401 clustered rows (`Handler_read_rnd_next`) |
| Raw aggregate on a covering index | `type=range`, `key=idx_hb_stats`, `Extra: Using where; Using index` | 43 199 index entries, `Handler_read_rnd_next=0` |
| Rollup read (this change) | `type=range`, `key=PRIMARY`, `rows=720` | 720 index entries |
| Boundary hour read (this change) | `type=range` | 119 index entries |

Contributing factors found while investigating:

- the 30 day `WHERE created_at >= ?` window is fixed regardless of the selected period,
  so even a 24 h dashboard read a month of rows;
- `idx_heartbeats_status` (a lone index on `status`) was never chosen by the optimizer:
  no query filters on a status without a monitor and a time range, so it only taxed every
  insert;
- no caching layer, and the same decorated monitor is recomputed for the listing, the
  detail page and each public API call;
- MariaDB's default `innodb_buffer_pool_size` (128 MB) is smaller than a single monitor's
  30 day history, so the scan reached the disk;
- `HEARTBEAT_RETENTION_DAYS=0` in `.env` is *not* unbounded: `0` is the fallback that
  yields the 180 day default, and only the Admin > Settings value `0` disables the purge.

## 3. Decisions taken

### Phase 1 - make the read cheap (shipped)

- **Covering index `idx_hb_stats (monitor_id, created_at, status, latency_ms)`** declared
  on the model (`internal/models/heartbeat.go`), so `AutoMigrate` creates it on existing
  installations. The old `idx_hb_monitor_time` and `idx_hb_monitor_node` stay: the first
  serves the listings, the second the per-node voting.
- **`idx_heartbeats_status` is dropped** by an explicit, idempotent
  `dropLegacyIndexes` step in `database.Migrate` (GORM never drops anything on its own -
  it only creates what is missing), and its model tag removed so a fresh schema never
  creates it.
- **Buffer pool**: `docker/docker-compose-bundle.yml` passes
  `--innodb-buffer-pool-size=${DB_BUFFER_POOL_SIZE:-256M}` and `docker/.env.example`
  documents it. Deliberately a conservative default with an env override, because a value
  larger than the host's free RAM prevents MariaDB from starting.

### Phase 2 - stop reading the raw table at all (shipped)

- **New table `heartbeat_rollups`**, primary key `(monitor_id, bucket_at)`: the exact
  reduction of one hour of one monitor (`up/down/pending/maintenance/total`,
  `up_latency_sum/up_latency_count`, `latency_min/latency_max`). Seven hundred and twenty
  rows per monitor for the 30 day horizon instead of 43 200.
- **Maintained incrementally on write**: `HeartbeatService.Record` follows its insert with
  one `INSERT ... ON DUPLICATE KEY UPDATE` that adds the heartbeat to its bucket. The
  increments are additive, so a shared-mode cluster's two nodes simply sum into the same
  row. `VALUES(col)` is avoided (deprecated in MySQL 8.0.20+, absent from MariaDB's alias
  form): the new values are passed twice instead.
- **Reconciled at boot** (`internal/database/rollup.go`, called from `cmd/server/main.go`
  before the scheduler starts): an empty table (the first boot after the upgrade) rebuilds
  the whole 30 day horizon; otherwise only the trailing six hours plus any gap since the
  newest bucket are recomputed. Absolute values, so it is idempotent.
- **`History` reads rollups + the two partial hours.** The rollups only hold *elapsed*
  hours, so each window is completed by the hour it starts in and the hour in progress,
  read raw with disjoint ranges. This is what keeps the numbers identical instead of
  rounding the windows to the hour. The split is pure Go (`historyWindowSet`,
  `historyTotals` in `internal/services/stats.go`), so it is unit testable without a
  database.
- **The average keeps MySQL's rounding.** The old `AVG()` came back as a DECIMAL with four
  fractional digits (the database's `div_precision_increment`); the pure-Go average is
  rounded the same way so the dashboard and the API show the very same value.
- **Retention covers the rollups**: `StatsService.Purge` and
  `HeartbeatService.PurgeOlderThan` delete `heartbeat_rollups` with the same cut-off
  *before* the heartbeats, so a rollup cannot outlive its source; `deleteMonitorCascade`
  removes them with the monitor.

### Rejected, and why

- **Redis (or any external cache).** It does not help the cold path, and it contradicts the
  deployment premise: `docs/architecture.md:165` ("no message broker, cache or service
  discovery") and `docs/clustering-modes.md` D10 ("no third-party component").
- **An in-process TTL cache as the first move.** Cheaper than Redis but still pays the full
  scan on every miss and every restart, and adds an invalidation surface. The rollups make
  the query cheap enough that a cache is no longer justified. Kept as a possible follow-up
  (see §7).
- **Rounding the windows to whole hours** (the simpler rollup read). Rejected: it would
  change every uptime percentage in the API and on the status pages, and would hide the
  current hour's checks.
- **Hourly rollups for the status-bar column (`RecentBars`).** A 24 h window split into 30
  slots is 48 min per slot, finer than an hour, so rollups cannot serve it. It stays on the
  raw table, but it is bounded by the selected window (24 h by default), not by 30 days.
- **Changing `status bigint` to `tinyint`.** A full table rebuild on a multi-GB table for a
  few bytes per row, with no query-plan benefit once the covering index exists.

## 4. Files touched

| File | Change |
|---|---|
| `internal/models/heartbeat.go` | `idx_hb_stats`; `status` index tag removed; `HeartbeatRollup` model, `HeartbeatBucket`, `HeartbeatRollupHorizonHours` |
| `internal/services/stats.go` | `History` rewritten over rollups + boundary hours; `historyWindowSet`/`historyTotals` pure helpers; two new queries |
| `internal/services/heartbeat.go` | rollup upsert in `Record` (non-fatal: a stored check is never lost); rollups purged in `PurgeOlderThan` |
| `internal/services/stats_series.go` | `Purge` deletes the rollups first |
| `internal/services/monitor_write.go` | `deleteMonitorCascade` removes the monitor's rollups |
| `internal/database/rollup.go` | **new**: `BackfillHeartbeatRollups` + `rollupBackfillQuery` |
| `internal/database/database.go` | model registered; `dropLegacyIndexes` |
| `cmd/server/main.go` | boot reconciliation call |
| `docker/docker-compose-bundle.yml`, `docker/.env.example` | `DB_BUFFER_POOL_SIZE` |
| `docs/database.md` | table inventory, both DDLs, §5 rewritten, "Sizing the server" |
| `internal/services/stats_history_test.go` | **new**: window split, disjoint ranges, equivalence with the raw aggregate |
| `internal/database/rollup_integration_test.go` | **new, opt-in** (`UP_SCRATCH_DSN`): both rollup writers and `History` against a real engine |

## 5. Validation

Two layers, because the pure unit tests cannot reach the SQL:

1. **Unit** (`internal/services/stats_history_test.go`, no database): the placeholder count
   of the upsert statement (a mismatch would silently break every rollup), `ceilHour`, the
   layout and deduplication of the windows, the disjointness of the raw ranges, and the
   equivalence of the split with the raw aggregate over 1 440 synthetic heartbeats. The
   equivalence test was mutation-checked: disabling the boundary read makes it fail with the
   expected three missing heartbeats per window.
2. **Integration, opt-in** (`internal/database/rollup_integration_test.go`, needs
   `UP_SCRATCH_DSN`): drops and recreates both tables on a throwaway MariaDB 11.8, stores
   3 080 heartbeats through `HeartbeatService.Record`, compares the stored rollups with a
   `GROUP BY` over the raw table, rebuilds them with the boot backfill (and a second time,
   for idempotence), then compares `History` with the aggregate it replaced for the four
   windows (24/168/336/720 h) and two monitors: counters, uptimes, average, minimum and
   maximum all match exactly.

### Three bugs the validation caught (all fixed)

1. **Boot failure on a fresh install.** `MAX(bucket_at)` is NULL on an empty rollup table
   and `Scan` into a bare `*time.Time` refuses NULL: the very first boot after the upgrade
   would have exited with "unsupported Scan, storing driver.Value type `<nil>`". Fixed with
   `sql.NullTime`.
2. **Empty table rolled up only six hours.** With the NULL case handled the wrong way, the
   first boot backfilled the healing window instead of the whole horizon, so the history
   would have shown a mostly empty 30 days until the rollups accumulated. Fixed by branching
   on emptiness.
3. **`avg_ms` lost its rounding.** `AVG()` returned a DECIMAL (four fractional digits); the
   pure-Go average returned full float64 precision, so every average in the API and the UI
   would have changed shape. Fixed by rounding to four digits.

## 6. Rolling it out on an existing deployment

The first boot after the upgrade does more than usual, in this order, before the HTTP
server starts:

1. `heartbeat_rollups` is created;
2. `idx_hb_stats` is added to `heartbeats` - an online `ALTER TABLE ... ADD INDEX` in
   InnoDB, so it does not block the writes of a running cluster, but it reads the whole
   table: expect it to take a while on a multi-GB history;
3. `idx_heartbeats_status` is dropped (metadata only);
4. `BackfillHeartbeatRollups` rolls up the 30 day horizon - one grouped scan of the
   heartbeats of the last 30 days, with the new index in place.

Watch for these log lines: `legacy index dropped`, `heartbeat rollups reconciled`
(with `from`, `to` and the number of rows). A failure of step 4 aborts the boot on purpose,
exactly like the other backfills: starting with half-empty rollups would silently
under-report every window.

**Verifying the parity.** Pick a monitor with a long history, note its uptime percentages
and latency figures before the upgrade (dashboard, monitor detail, `GET /api/v1/monitors/:id`),
then compare after: the counters, the uptimes and the latency figures are identical by
construction, and the `avg_ms` keeps its four decimals.

**Rolling back** is reverting the image: the switched read path lives in the code, and the
new table and index are inert for the old query. To reclaim the space by hand:

```sql
DROP TABLE heartbeat_rollups;
ALTER TABLE heartbeats DROP INDEX idx_hb_stats;
ALTER TABLE heartbeats ADD INDEX idx_heartbeats_status (status);   -- only for a full revert
```

## 7. What is deliberately left over

- **`StatsService.Window` still reads the raw table** (`internal/handlers/monitors.go:267`,
  and the two public-API payloads that also return the 95th percentile,
  `internal/handlers/publicapi.go:122,154`). It is bounded by the requested period (24 h by
  default, ~1 440 rows) instead of 30 days, and its `percentile95` needs individual latency
  samples, so it cannot read rollups. If it ever shows up in the slow log, the answer is a
  cache or a minute-granularity rollup, not this table.
- **`RecentBars`** (the status-bar column) stays on the raw table for the reason in §3.
- **An in-process TTL cache + `golang.org/x/sync/singleflight`** (already a dependency) over
  `History`, if a deployment appears where even the rollup read is hot. Any cache must key on
  the same hour boundaries, or the numbers of two concurrent reads would disagree.
- **Retention below 30 days** shrinks the readable history as it always did: the rollups are
  pruned with the heartbeats, so a 7 day retention keeps 7 days of windows and no rollup can
  resurrect a period the operator deleted.
- **The `HEARTBEAT_RETENTION_DAYS` naming trap** is now documented in this plan: `0` in
  `.env` means "use the 180 day default", and only the Admin > Settings value `0` disables
  the purge. No code change was made, because it is the documented behaviour of
  `SettingService.HeartbeatRetentionDays`.



