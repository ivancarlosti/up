# Plan - Drop the web performance history dialog and button

> Task: the *Results* page of a monitored page (`/admin/web-performance/:id`) now
> carries the vitals, the audited URL, the node, the trend chart and the table of
> every stored run, so the *History* button and the dialog of the listing are
> redundant. Remove both.

## 1. Why

`607c7d7` added the results detail page and, on purpose (§5 of
`2026-10-12-lighthouse-both-results-detail-page.md`), left the listing's *History*
dialog in place. Revisited: the dialog shows a strict subset of the results page
(a chart plus the current scores), it re-reads the same `lighthouseHistory`
endpoint with the same limit, and it duplicates the `history*` state and the
`openHistory` call the detail page already owns. The listing row already links to
the results page through *Results* (`lighthouse.details`), which is the single
place the history is read.

## 2. Changes

| file | change |
|---|---|
| `web/src/views/admin/WebPerformanceView.vue` | imports `History`, `Dialog`, `LighthouseHistoryChart` and the `LighthouseRun` type dropped; `HISTORY_LIMIT`, the four `history*` refs and `openHistory()` removed; the row *History* button and the trailing `<Dialog>` block deleted; header comment points at the results page |
| `web/src/views/admin/WebPerformanceDetailView.vue` | header comment no longer describes the listing's history dialog as untouched |
| `web/src/locales/*.json` (7 locales) | the now-unused `lighthouse.history` and `lighthouse.historyTitleOf` keys removed (`historyTitle` and `noHistory` stay: `LighthouseHistoryChart` uses them) |
| `docs/monitors.md` | the admin-page paragraph drops "and a per-monitor history chart" and the "*History* dialog of the list is kept" sentence |

Kept, untouched: the search box, *Refresh*, *Run now*, *Purge now*, the `compact`
`LighthouseScores` per row and the *Results* link. `api.lighthouseHistory` stays
(the detail page still reads the history through it).

## 3. Verification

- `npm run typecheck`, `npm run check:i18n`, `npm run build` in `web/`.
- `grep`: no `openHistory` / `historyOpen` / `historyTitleOf` / `lighthouse.history`
  left in `web/src` (the `HISTORY_LIMIT` hits that remain are the detail page's own).
