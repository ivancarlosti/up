# Plan - Dashboard render cost at 1 822 monitors (the browser side)

> **Shipped plan** — task: *"the dashboard still takes ~34 s on a hard refresh at
> 1 822 monitors, even after the 2026-10-30 server work"*. The follow-up to
> `plans/2026-10-30-dashboard-load-performance.md`, which fixed the **server**
> (listing aggregates, indexes, purge, gzip) and measured `/api/dashboard` at
> ~0.4 s. Implemented on **2026-10-31**.

## 1. Goal

Find where the seconds go on a cold dashboard mount at 1 822 monitors, ship a
harness that measures it, and remove the cost that the harness points at —
without changing what the dashboard shows.

## 2. What the harness found

`web/scripts/e2e-dashboard-load.mjs` (`npm run e2e:dashboard-load`) seeds (or
reuses) an installation with N monitors, then loads the dashboard twice — once
with the live feed and once with it taken away — measuring the server, the
payload, the first painted row, the main-thread long tasks and the event rate.
Run against a throwaway MariaDB 11.8 with 1 822 monitors, 721 hourly rollup rows
each (1 313 662 rows) and 113 k raw heartbeats:

| | Before (HEAD `ecd45b9`) | After |
|---|---|---|
| `/api/dashboard` TTFB | 287–297 ms | 254–283 ms |
| Payload (gzip) | 113 kB | 111–114 kB |
| Server "monitors decorated" | 196–238 ms | 196–238 ms |
| **First row in the DOM** | **2 173 ms** | **570–609 ms** |
| Rows mounted | 1 822 | 100 (+1 sentinel) |
| DOM nodes | 146 071 | 8 313 (**−94 %**) |
| Long tasks during the load | 2 tasks, **1 791 ms** | 2 tasks, **240–251 ms** |
| WebSocket frames in a 9 s window | 269 | 133 (**−50 %**) |
| Scrolling from top to bottom | shows 1 822 | shows 1 822 |

Three conclusions shaped the work:

1. **The server was not the problem.** 200–340 ms of TTFB, 111 kB after gzip.
   The 2026-10-30 work holds at 1 822 monitors.
2. **The live feed was not the problem either** — and the reason matters. With
   the feed blocked the table painted at 2 176 ms versus 2 173 ms with it. At
   HEAD the feed delivered ~61 events/s, but Vue skips a write that does not
   change the value: the scheduler published `monitor.status` **on every check**,
   and every check said `down` for a monitor that was already `down`, so
   `monitor.status = 'down'` was a no-op and no row re-rendered. The events cost
   bandwidth and a counter recount per event, nothing visible.
3. **The mount was the problem.** One 1.8 s task that built 1 822 rows, their
   badges and 25 sparkline bars each (146 071 nodes) before the first paint. That
   is the cost that scales with the monitor count, and the one that explains a
   34 s refresh on a slower machine with real certificates, domains and tags:
   the same work, on a browser that is ~15× slower than this container.

## 3. The three changes

### A. `monitor.status` is published only when the verdict changes

`internal/services/cluster_notify.go` gained a pure helper and two call sites
became conditional on it:

```go
func publishStatus(previous, status models.AggregateStatus) bool {
	return previous != status
}
```

Both publishes (the manual "check now" branch and the scheduled one) go through
it. The payload is byte-identical, `last_message`/`last_latency`/`last_check_at`
still move on every check, the incident and subscription bookkeeping is
untouched: only the *message* is spared when it would say nothing new. A first
verdict (`previous` empty or `unknown`) is still published, so a new monitor
still appears with its badge.

Rejected alternative: publishing a "slow" heartbeat event with the latency only.
The `heartbeat` event already carries it, and the dashboard renders nothing from
it, so a second event would only add traffic.

### B. The store coalesces the feed

`web/src/stores/monitors.ts` buffers `heartbeat` and `monitor.status` into two
`Map`s (last write wins per monitor) and applies them on a 500 ms timer: one pass
to index the list, one `refreshSummary()` per flush instead of one per event, and
no counter recount at all when a flush only carries heartbeats. `load()` and
`disconnect()` cancel the timer and drop the buffer, so a fresh listing can never
be overwritten by events that predate it.

### C. The table mounts its rows in pages

`MonitorTable.vue` renders the first 100 rows and appends the next 100 when a
sentinel row comes within 600 px of the viewport (`IntersectionObserver`, reset to
the first page whenever the caller hands a new array — a new filter, a new sort or
a new listing). `DashboardView.vue` computes every summary box in one pass instead
of eight `filter()` calls over the same list.

Rejected alternative: a virtual scroller with an absolutely positioned spacer.
It would cut the DOM further, but it needs an explicit row height (the rows wrap
at narrow widths) and it changes the scrollbar and Ctrl+F behaviour for the
operator. Paging by 100 rows keeps the native table.

Rejected alternative: `v-memo` on `<tr>`. After A the feed no longer re-renders
rows, and after C only 100 rows can be patched at a time; a memo dependency list
that has to repeat every printed field is a silent staleness bug waiting for the
next column someone adds.

## 4. Decisions taken

- **Publish on change, not on every check.** The live badge of an unchanged
  monitor has nothing to say. Clients that need the latency already get it from
  `heartbeat`. No external WebSocket consumer exists in the repo (checked:
  `web/src/lib/ws.ts` is the only client).
- **500 ms flush.** Half a second is invisible to a human and turns 61 events/s
  into 2 flushes/s. Shorter would spend the savings on renders; longer would make
  a "check now" feel dead.
- **100 rows per page.** 100 rows is ~4 000 px of table: enough that nobody
  scrolls to a blank area, small enough that the first paint is one frame.
- **Report, do not guess.** The 34 s was not reproduced here (this fixture mounts
  in 2.2 s on HEAD), so the harness ships with the fix and the numbers above are
  the ones measured, not extrapolated.

## 5. Acceptance

- 1 822 monitors, hard reload: first row in the DOM **< 2 s** — measured 0.57–0.61 s.
- No sustained long task after load — the longest task of the whole load is
  ~250 ms (it was 1.8 s).
- Scrolling from the top to the bottom still reaches every row — asserted by the
  harness (`scrolling from top to bottom reaches 1822 of 1822 rows`).
- `go build`, `go vet`, `go test ./...`, `npm run typecheck`, `npm run build`,
  `npm run smoke`, `npm run check:i18n`, `npm run e2e:table-sort` all pass.

## 6. Leftovers (deliberately out of scope)

- `AggregateService.ActiveForHost` still switches on `case "some"` for the host
  lookup used by the status pages; unrelated to this measurement, still odd.
- `ClusterService.Status` returns the node `PrivateKey` in a field that no page
  reads — a separate cleanup.
- A snapshot/ETag cache for `/api/dashboard` was considered and dropped: it
  would hide the real cost (200–340 ms) behind invalidation rules nobody asked
  for, and the server is not where the seconds are.
- The dashboard still sorts and filters the whole list client-side
  (`sortMonitors`, ~15 ms at 1 822 rows). Moving it to SQL is only worth it
  beyond a few thousand monitors.
- `npm run e2e:table-sort` used to fail on the **groups** table, and it already
  did at HEAD (verified against a HEAD build on the same database). Its fixture
  created group `a` with two monitors and group `b` with one of them, but a
  monitor belongs to at most one group by design (`replaceMonitorGroupMembers`
  moves it), so `a` and `b` both ended up with one monitor, the *Monitors*
  column tied and the expected order could not hold. Fixed in the script: the
  fixture now seeds a third monitor for `b`, and a new check reads the three
  counts back from `/api/monitor-groups` (`0,1,2`) so a fixture that drifts
  again fails where the mistake is, not in the sort assertions. Verified both
  ways: the suite passes, and with `b` put back on `m1` the fixture check goes
  red with `0,1,1`. The sort widget itself was fine all along (the templates and
  status pages tables passed).
