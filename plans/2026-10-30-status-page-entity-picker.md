# Plan - The status page dialog picks monitors and groups

> Task: the page dialog (**Admin > Status pages > New/Edit page**) lists the
> monitors and the groups as two `Checkbox` grids. Replace them with the
> search-and-pick pair of the monitor group dialog, extended to the groups, so a
> large installation can find a target instead of scrolling a wall of checkboxes.

## 1. Why

The monitor group dialog already replaced its checkbox grid with a search box plus
a member list (`2026-10-12` era, see [monitors.md](../docs/monitors.md) §7): the
grid did not scale past a few dozen monitors. The status page dialog kept the same
grid, and it is the screen with the **most** candidates — every monitor of the
installation plus every group — so it is where the grid hurts first. Group
assignment on a page already exists at the backend (`PUT
/api/status-pages/:id/groups`, `planStatusPage`), the dialog simply had no UI for
it beyond a second checkbox row that was hidden entirely when no group existed.

The picker can therefore be a **pure UI change**: no endpoint, no migration, no Go
code.

## 2. Decisions

- **D1 - two selected lists, never one merged one.** The page renders the explicit
  monitors first, in their order, and then one section per group
  (`planStatusPage`), and the API stores the two orders separately. An interleaved
  list would promise an order the renderer does not keep.
- **D2 - a monitor covered by a selected group is listed but not clickable.** It
  already renders inside the group section, so an explicit entry would be a
  duplicate the renderer drops: the row shows `via <group>` and no `+`.
  `MonitorGroup.monitor_ids` (already in the `GET /api/monitor-groups` payload)
  is what tells the UI.
- **D3 - the reverse overlap is kept and marked.** A monitor selected explicitly
  that later joins a linked group keeps its row with `also in <group>`; removing
  it is the operator's call, so nothing disappears silently.
- **D4 - order is append-only.** A click appends at the end of its list, exactly
  like the member list of the group dialog. Reordering the page is still the API's
  `PUT .../monitors` with the ids in the wanted order; the dialog does not grow
  move buttons for it.

## 3. Changes

| file | change |
|---|---|
| `web/src/views/admin/AdminStatusPagesView.vue` | `Checkbox` import dropped, `Boxes`/`X` added; `pickerSearch` ref reset in `openCreate`/`openEdit`; `toggleMonitor`/`toggleGroup` replaced by the computeds `pickerTerm`, `matchingMonitors`, `matchingGroups`, `coveredByGroup`, `offeredMonitors`, `offeredGroups`, `selectedMonitorRows`, `selectedGroupRows`, `pickerEmpty`, `pickerHasMatch` and the functions `tagList`, `addMonitor`, `removeMonitor`, `addGroup`, `removeGroup` on the same `selectedMonitors`/`selectedGroups` arrays (so `save()` is untouched); the two checkbox blocks replaced by one search box (`id="status-page-picker"`), the offer box and the two selected lists |
| `web/src/locales/*.json` (7 locales) | `statusPages.picker*` added (15 keys: title, help, placeholder, all added, group size, `via`/`also in`, covered, add/remove per kind, both empty states, the no-groups hint); `selectMonitorsHelp` and `selectGroupsHelp` removed (only this dialog used them) |
| `web/scripts/e2e-status-page-groups.mjs` | third fixture monitor; the admin block now asserts the picker box, the group row with its remove button, that a monitor of the group is disabled with its `via` badge, and that a monitor typed in the box is stored by `PUT .../monitors` on save |
| `docs/status-pages.md` §3.1 | the picker described, with the `via` / `also in` rules and the explicit-first ordering |
| `docs/monitors.md` §7 | the search/pick pair is now shared by both dialogs |
| `docs/development.md` | the `e2e:status-page-groups` paragraph describes the picker check instead of the ticked checkbox |

## 4. Verification

- `npm run typecheck`, `npm run check:i18n`, `npm run build` in `web/`.
- `npm run e2e:status-page-groups -- --url http://localhost:3000` against a
  running instance: the dialog opens, lists the group, flags the covered monitor
  and persists a monitor picked in the search box.
- `npm run e2e:groups` as a regression check: the group dialog is untouched.
