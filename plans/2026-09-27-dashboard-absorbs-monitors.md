# Plan - The dashboard becomes the one monitors page

> **Plan** — task: *remove the separate Monitors page and make the Dashboard the main
> monitors screen*, worked on **2026-09-27**. Working note (`plans/`, tracked in git),
> kept because §2 records decisions the shipped code only states in passing: why a redirect and
> not a 404, why the dashboard loads the group list at start-up, and where the target line is
> derived.

## 1. Goal (as asked)

1. **Drop the Monitors page.** `/admin/monitors` (`AdminMonitorsView.vue`) and its sidebar entry
   disappear; the Dashboard becomes the one place a monitor is listed and managed.
2. **Bring the missing toolbar down to the dashboard.** The group / type / status filters and the
   *Add in bulk* / *Apply template* actions sit **below the summary boxes**, exactly the toolbar
   the removed page had, so nothing the operator could do there is lost.
3. **Show the target under the monitor name.** For a URL the line is a real link that opens in a
   new tab; for a host, `host:port` or a DNS record it is plain text.
4. **Fix the group of every dashboard row.** It rendered the raw `group_ids` number because the
   dashboard never loaded the group list; it must render the group name like the admin table did.

## 2. Decisions taken

- **A redirect, not a 404.** `/admin/monitors` keeps existing as `redirect: { name: 'dashboard' }`.
  Bookmarks, the five `e2e-*.mjs` scripts that land on `/admin/monitors` and the browser history of
  an operator all keep working, and the removed view is not silently turned into "not found". The
  route carries no component, so the page itself is gone.
- **One toolbar, dashboard own filters kept.** The summary boxes keep filtering the status and the
  two expiries (that is the dashboard's own idea), and the new selects add the admin filters the
  boxes cannot express (a single group, `ssl`, `pending`/`maintenance`/`unknown`). Both run: the
  row must satisfy the box **and** the selects, which is the least surprising reading of "box +
  filter".
- **Reuse the existing keys, add none.** The toolbar reuses `monitor.allGroups`, `bulk.button`,
  `apply.button`, `dashboard.allTypes`/`allStatus`, `common.clone`… so all seven locales stay in
  step without a single new message. `nav.monitors` is left in the files (an unused key is
  harmless for `check:i18n`, which only checks the keys the source asks for) because deleting a
  message from seven locales buys nothing.
- **The target mirror is a frontend twin of `notify.MonitorTarget`.** `lib/monitor-config.ts` gains
  `monitorTarget` (url for http/keyword, `host:port` for tcp, `record host @resolver` for dns,
  `host:port` with the 443 default for ssl) and `monitorTargetURL` (only a parsed `http:`/`https:`
  URL is linkable). It lives next to `CONFIG_FIELDS`, the module that already mirrors the Go
  config rules per type.
- **Realtime stays the store's job.** Mutations (create, edit, clone, bulk, apply, delete) reload
  through the store's `load()` and refresh the group list, instead of the local `ref` the removed
  page kept — the dashboard's WebSocket keeps working exactly as before.

## 3. Files touched

| Layer | File |
|---|---|
| Web | `views/DashboardView.vue` (toolbar, groups, clone, bulk/apply, target line), `components/monitors/MonitorTable.vue` (target line under the name), `lib/monitor-config.ts` (`monitorTarget` / `monitorTargetURL`), `components/layout/AppSidebar.vue` (Monitors entry removed), `router/index.ts` (redirect), `lib/monitor-sort.ts` + `MonitorTable.vue` docstrings |
| Removed | `views/admin/AdminMonitorsView.vue` |
| Scripts | `scripts/shots.mjs` (the duplicated `admin-monitors` shot) |
| Docs | `docs/monitors.md` (four "Admin > Monitors" mentions, the file table) |

## 4. What the dashboard shows now

- Header: title + subtitle, the last-check time and *Refresh*.
- The seven summary boxes, unchanged, still filtering.
- The toolbar: *Groups* / *Type* / *Status* selects, the search box, *Clear filter*, then the
  actions pushed to the end — *Add monitor*, *Add in bulk*, *Apply template*.
- The shared table, now with the group **names**, the clone action and a target line under every
  name (`https://…` opens in a new tab, `mail.example.com:993` is plain text).

## 5. Verification commands

```bash
cd web && npm run typecheck && npm run check:i18n
node --check ../web/scripts/*.mjs      # scripts still parse after the shots edit
```

`npm run build` still needs Node >= 20.19 / 22.12 (vite 8); the lab's Node is 18.19.1, so the
build is verified on the same throwaway Node 22 used for the previous plan when available.

Result (2026-09-27, throwaway Node 22 at `/tmp/node22`): `typecheck` clean, `check:i18n` ok
(621 keys, 5201 messages), every `scripts/*.mjs` parses, `vite build` ok — the
`AdminMonitorsView` chunk is gone and `DashboardView` grew to 31.3 kB, which is the page it
absorbed. `web/dist/index.html` (the one tracked file under `dist/`) now points at the new
hashes, as in commit `0b672b0`.

## 6. Risks / notes

- **The five e2e suites** (`e2e-groups`, `e2e-templates`, `e2e-certificates`, `e2e-domain-expiry`,
  and any other that lands on `/admin/monitors`) rely on the *Add monitor* / *Add in bulk* /
  *Apply template* buttons and the row *clone* title. All of them are rendered by the dashboard
  now, and the buttons keep their existing labels, so the suites keep passing through the
  redirect. `scripts/shots.mjs` drops the now-duplicate shot.
- **Two "add" entry points become one page.** The dashboard store already polled and held the
  monitor list; the removed view kept a second copy. Loading the group list on the dashboard is
  the only new request at start-up.
- **The group list can lag a group rename** edited on another tab until the dashboard reloads;
  that was already true of the removed page, which reloaded only after its own mutations.
