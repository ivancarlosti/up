# Plan - A sortable Uptime column in the Systems table

> **Shipped plan** — task: *"add an Uptime column to the Systems table, sortable,
> backed by a persisted column on the `systems` table"*.
> Implemented on **2026-11-02**.

## 1. Goal

1. The Systems table shows **how long each machine has been up**, between the
   container count and the freshness of the last sample.
2. The column is **sortable** (`systems.uptimeColumn` / `uptime`), and the order is
   remembered per browser like every other column.
3. The figure is **persisted on the `systems` row** (`systems.uptime`), so the
   listing reads it with the row it already has instead of a join on the per minute
   history.

## 2. Why a denormalised column

The value already existed end to end for *display*: the agent reports it in
`Info.Uptime`, `metricFromSample` stores it in `system_metrics.uptime`
(`internal/services/system_ingest.go`), `GET /api/systems/overview` returns the
latest sample and the frontend maps it to `SystemPoint.uptime`. What was missing was
a cheap way to **sort** by it and to render it for a machine whose sample is not in
the overview map this pass.

Three options were on the table:

| Option | Verdict |
|---|---|
| read it from the latest sample the page already holds (`overview`) | the map is fetched separately from the listing and is keyed by `system_id`, so a row can render a dash for a moment after load. Acceptable for CPU (the page deliberately renders that only from the sample), wrong for a column whose point is being a property of the machine |
| join `system_metrics` on `MAX(id)` inside the listing query | the listing is already two queries; a third join for one number buys nothing |
| **denormalise the last sample's uptime onto `systems`** | one extra column, written by the ingest that already updates `last_sample_at`, `last_seen_at` and `last_error` in the same `UPDATE`. **Chosen** |

## 3. Data

| Table | Column | Written by | Travels to peers |
|---|---|---|---|
| `systems` | `uptime` (`uint64`, `not null;default:0`) | the owner node, in the sample transaction | **no** |
| `system_metrics` | `uptime` (unchanged) | the owner node, one row per sample | no |

`systems.uptime` is an **observation**, not configuration. `SystemPayload`
deliberately carries only what the peers must agree on (name, host, enabled, token
hash, prefix, fingerprint, agent version, alert rules), and `writeSystem` updates an
explicit column list — so a payload applied by a peer leaves the local `uptime`
(and `last_sample_at`, `last_seen_at`, `last_error`) untouched. The doc comment of
`SystemPayload` now says so explicitly, next to the identity fields.

Zero means **unknown**, never "just booted": the ingest writes the column only when
the agent reported a non-zero uptime, the same "never erase what a previous sample
knew" rule the identity fields follow. A federated node that does not own a machine
therefore shows `—`, exactly as it already does for CPU, memory, disk and
containers (the overview endpoint is local; it never proxies to a peer).

## 4. Implementation

| Where | Change |
|---|---|
| `internal/models/system.go` | `Uptime uint64` with `gorm:"not null;default:0" json:"uptime"`, next to `LastSampleAt`/`LastSeenAt`, documenting why it is denormalised |
| `internal/services/system_ingest.go` | the sample transaction writes `columns["uptime"]` when `data.Info.Uptime > 0` (same `UPDATE` as the freshness columns, no extra revision bump) |
| `internal/database/*` | nothing: `models.System` is already in the `AutoMigrate` list, which adds the column as `bigint unsigned NOT NULL DEFAULT 0` |
| `internal/models/sync_payload.go` | comment only: the payload deliberately omits the owner's observations (identity **and** uptime / last sample / last error) |
| `web/src/lib/types-systems.ts` | `System.uptime: number` |
| `web/src/lib/format.ts` | **new** `formatDuration(seconds)`: `42d 3h`, `4h 12m`, `7m`, `38s`; `—` for `0`/missing, like `formatUptime` does for a missing percentage |
| `web/src/lib/system-sort.ts` | `'uptime'` in `SystemSortKey` and in `systemSortKeys` (the guard of the remembered sort), and a comparator that reads the row: `sign * ((a.uptime \|\| -1) - (b.uptime \|\| -1)) \|\| byName()` — "no data" is the lowest value, like a missing sample |
| `web/src/components/systems/SystemTable.vue` | `SortHeader column="uptime"` + cell `formatDuration(system.uptime)` between Containers and Last sample |
| `web/src/locales/*.json` (7) | `systems.uptimeColumn`, English in every locale (the whole `systems` block is still untranslated: see §7 of `docs/hardware-monitoring.md`) |
| `internal/handlers/agent_connect_integration_test.go` | the end to end check asserts the row carries the sample's uptime |
| `docs/hardware-monitoring.md` | the `systems` row of the §3 table, plus a paragraph on why the value is stored twice |

`formatDuration` is separate from `formatInterval` on purpose: the latter stops at
hours (`1440m`) because it formats monitor intervals, and changing it would alter
what the monitors already show. Both keep short, locale-neutral suffixes, the same
choice `formatUptimeWindow` makes.

The store needed no change: `SystemsView` already polls `load()` every 30 s, and the
column reads `system.uptime` off that list. A `system.sample` event may lag it by
half a minute, which is irrelevant for a figure that counts seconds.

## 5. Tests

| File | What it pins |
|---|---|
| `internal/handlers/agent_connect_integration_test.go` (**opt-in**, `UP_SCRATCH_DSN`) | the whole path: the CBOR sample carrying `Uptime: 1234` reaches the handler, the ingest transaction stores it, and `History` returns a system row whose `Uptime` is 1234 |

There is no new unit test: the write happens inside the sample transaction, which
needs a database — the same reason `last_sample_at` and the identity columns have no
unit test either. `formatDuration` has no frontend test because the repository has no
JS test runner (validation is `typecheck` + `check:i18n` + `build`, plus the
Puppeteer e2e scripts).

## 6. Validation

- `gofmt -l internal cmd`, `go build ./...`, `go vet ./internal/...` — clean.
- `go test ./internal/models/... ./internal/database/... ./internal/services/...
  ./internal/handlers/...` — pass.
- `cd web && npm run typecheck && npm run check:i18n && npm run build` — clean
  (1 202 messages per locale, 1 024 used keys).
- **Live**, against a throwaway MariaDB 11.8 (`127.0.0.1:3307`):
  - a boot-time `database.Migrate` creates `systems.uptime` as
    `bigint(20) unsigned NO 0`;
  - dropping the column and migrating again re-adds it, so the upgrade of an
    existing database is covered by the same path;
  - `UP_SCRATCH_DSN=… go test ./internal/handlers/ -run TestAgentEndToEnd -v` —
    pass, including the new uptime assertion.
- `formatDuration` was exercised directly (0, 59, 60, 3599, 3600, 3661, 15120,
  86400, 97200, 1 000 000, `null`, `NaN`) and renders `—`, `59s`, `1m`, `59m`,
  `1h`, `1h 1m`, `4h 12m`, `1d`, `1d 3h`, `11d 13h`.

## 7. Results

The Systems table has an **Uptime** column between Containers and Last sample,
sortable in both directions with the order remembered per browser, and every row
renders the value the last sample reported (`—` until the first one arrives). The
value lives on the `systems` row, so the listing does not pay for a join, and a
machine owned by another node in federated mode shows the same dash it already shows
for the other counters.

## 8. Documentation

`docs/hardware-monitoring.md` §3 gained `uptime`, `last seen` and `last error` on the
`systems` row (and the owner node as a writer of the observations), plus a paragraph
explaining the deliberate duplication between `system_metrics.uptime` and
`systems.uptime`, the "zero means unknown" rule and why it does not travel between
peers.
