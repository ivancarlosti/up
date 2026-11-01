# Plan - The per node state table and the bounded monitor deletion

> **Shipped plan** — task: *"`/api/dashboard` and `DELETE /api/monitors/:id` answer
> 500 with `i/o timeout` on a 1 900 monitor installation"*. Implemented on
> **2026-11-01**, after `plans/2026-10-30-dashboard-load-performance.md` fixed the
> reads that ran on every load: these are the two statements that were left, and
> both of them were unbounded by construction rather than merely slow.

## 1. Symptoms

```
GET  /api/dashboard          500  (client 30 s)
DELETE /api/monitors/96      500  (client 30 s)

SLOW SQL >= 500ms
...internal/services/stats_series.go:90  [rows:...] SELECT h.* FROM heartbeats h JOIN (...)
...internal/services/monitor_write.go:428 Exec DELETE FROM `heartbeats` WHERE monitor_id = 96
i/o timeout  ->  invalid connection
```

`github.com/ivancarlosti/up/...` in front of a slow query is **the file name of the
Go caller**, not an outbound request: the image is built with `-trimpath` (Dockerfile
line 47), so the compiler records every source path as the module path of `go.mod`
and GORM prints it verbatim (`utils.FileWithLineNum`). Nothing in the process talks
to GitHub. That is now said where the logger is configured
(`internal/database/database.go`), because it cost a day of looking at the wrong
thing.

The `i/o timeout` / `invalid connection` pair comes from the driver:
`internal/config/helpers.go` sets `readTimeout=30s`, so the *client* gives up while
MySQL keeps working on the statement, the connection is poisoned, and the handler
returns 500 for a query that would have finished.

## 2. Diagnosis

**The dashboard.** `EvaluateAll` asked `StatsService.LatestPerNode` for the newest
heartbeat of every `(monitor, node)` pair of the listing. The statement was:

```sql
SELECT h.* FROM heartbeats h
JOIN (SELECT monitor_id, node_id, MAX(id) AS max_id
      FROM heartbeats
      WHERE monitor_id IN (…) AND created_at >= ?
      GROUP BY monitor_id, node_id) latest ON latest.max_id = h.id
```

The vote window (`max(2 x interval, 120 s)`) was supposed to bound it, and it does
not. `heartbeats` carries two candidate indexes; the optimizer picks
`idx_hb_monitor_node (monitor_id, node_id, created_at)` because it also satisfies the
`GROUP BY`, and with the node unconstrained that index can only range on
`monitor_id`:

```
key: idx_hb_monitor_node   key_len: 8   Extra: Using where; Using index
```

`created_at` is then a filter applied while walking **every check the installation
ever stored** for those monitors. The window bounds the *answer*, not the *scan*.
One monitor with `interval_seconds = 43200` also widened the single bound of the
batch to 24 h for all 1 900 monitors, but the plan was already the problem: at 180
days of retention the index scan is hundreds of millions of entries.

**The deletion.** `MonitorService.Delete` ran `deleteMonitorCascade` inside one
transaction: a single `DELETE FROM heartbeats WHERE monitor_id = ?` with no limit
(~260 000 rows for a 60 s monitor kept 180 days) plus one `DELETE` per related table
and the monitor row. The undo log grows for the whole statement, the lock is held
for the whole statement, and concurrent dashboard reads queue behind it — which is
also why the *dashboard* 500s got worse while a deletion was running.

`HEARTBEAT_RETENTION_DAYS=0` in `.env` is not "keep forever": `0` falls back to the
180 day default of Admin > Settings (`settings.go`). It was not part of the bug.

## 3. What changed

**`monitor_node_states` (`internal/models/heartbeat.go`, `internal/services/node_state.go`).**
One row per `(monitor, node)`, primary key included, replaced by every check with one
`INSERT ... ON DUPLICATE KEY UPDATE` whose every column is guarded by
`IF(? >= checked_at, …)`: an out of order write (a retry landing after the next
check, a manual "check now") never moves a row backwards. `LatestPerNode` is now a
primary key range:

```sql
SELECT * FROM monitor_node_states WHERE monitor_id IN (?) AND checked_at >= ?;
```

The window is still applied — a row older than the vote window is a stale verdict —
but it is a filter over one row per monitor and node, so it costs the same on a
fresh installation and on a 5 year old one.

The table is **derived**, so the boot seeds it (`SeedNodeStates`, a `boot` step
before the scheduler starts) instead of waiting for the next check of every monitor:
without it a monitor on a 6 h interval would read `pending` for 6 h after the
upgrade, which is a visible regression for exactly the monitors the vote window was
designed for. The seed binds one statement per vote-window group (two minutes for
the default interval, wider only for the slow monitors) and pins
`idx_hb_monitor_time`, where the timestamp *is* a range of the index:

```
FORCE INDEX (idx_hb_monitor_time)  →  key_len: 16  rows: <window>
without the hint                   →  key_len: 8   rows: <the whole history>
```

It is idempotent (the guard keeps the stored row) and it never reads more than the
checks inside each monitor's own window.

**The deletion (`internal/services/monitor_write.go`).** Two phases:

1. one short transaction — tombstones, the bounded rows (`monitor_node_states`,
   `monitor_states`, links, certificate/domain observations, lighthouse
   observations, notification locks, status page items) and the monitor row. The
   monitor disappears from every listing at once, and a failure anywhere rolls the
   whole phase back.
2. the history — `heartbeats`, `heartbeat_rollups`, `lighthouse_runs` — deleted
   after the commit with the same chunked loop the retention purge uses
   (`deleteInChunks`, `LIMIT 5000`), on a `context.WithoutCancel` with a 15 minute
   budget, so a browser that walks away cannot leave the statement half killed.

The schema declares no foreign keys, so splitting the two is safe; the chunked
purge is resumable, and a row that survives a crash belongs to a monitor id nobody
references (the retention policy collects it like any other old row). The
synchronisation apply path still deletes everything in its own transaction
(`deleteMonitorCascade`), because the sync cursor has to advance atomically with
the change — no HTTP client waits for that one.

**Not done, on purpose.** `ParameterizedQueries: true` in the GORM logger would hide
the values of every slow statement. The 30 s queries are gone, so the log flood is
gone with them, and the values are what makes a slow query debuggable. The
`github.com/...` prefix is a file name (see §1) and is documented instead of hidden.

## 4. Verification

Against a throwaway MariaDB 11 (`docs/development.md` §2) with the application
booted on it:

| Check | Result |
|---|---|
| Boot with the new table | 35 tables, `node_states_ms: 0` (empty table), no error |
| `GET /api/dashboard`, 1 900 monitors | **200 in 8 ms** |
| `DELETE /api/monitors/:id` (30 000 heartbeats) | **204 in 10 ms** |
| The history purge behind it | 30 002 rows in 356 ms, in chunks |
| `monitor_node_states` after the delete | the row of that monitor is gone |
| `LatestPerNode` plan | `range` on `PRIMARY`, 1 row |
| Seed plan | `range` on `idx_hb_monitor_time`, `key_len: 16` |

Tests: `TestGroupByVoteWindow`, `TestChunkIDs`,
`TestNodeStateStatementsBindTheirArguments` (placeholder count and the "checked_at is
the last assignment" rule the guard depends on) and `TestHeartbeatToAggregateMapping`
are plain unit tests; `TestNodeStateLive` is the opt-in integration test
(`UP_SCRATCH_DSN`) that pins the seed, the upsert guard, the read bound and the
chunked purge of a monitor with more rows than one chunk.

## 5. Follow-ups

- A peer delete (`sync_apply.go`) still removes the history of the monitor in the
  transaction of the sync batch. It is rare and nothing waits on it over HTTP, but a
  very old monitor pulled from a peer can still hold that transaction open; the
  shape of a fix is to apply the configuration change and defer the history to the
  same background purge.
- `History` still runs a `UNION ALL` over `heartbeats` for the hour boundaries of
  each window (`stats.go`). It is bounded by the boundary hours, not by the
  history, but it is the last statement on the load path that reads raw heartbeats,
  and it is worth watching in the slow query log after the next release.
