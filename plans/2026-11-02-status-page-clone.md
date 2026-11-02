# Plan - Clone a status page

> **Shipped plan** — task: *"add the clone/copy action to the status pages, like the
> monitors, the groups and the templates"*. Implemented on **2026-11-02**.
>
> Owner decision taken before coding (variant **B**): the copy takes the **full
> settings and both selections**, but `subscription_enabled` is forced to `false` on
> the copy while the SMTP channel and the delay travel as inert configuration. The
> alternatives were "a bare copy (settings only)" and "everything including the
> subscription switch".

## 1. Goal

One operator action duplicates a status page: same settings, same monitors in the
same order with their overrides, same linked groups in the same order — and a page
that is safe to publish, because it starts with no subscribers and no mails.

## 2. What travels and what stays behind

| Travels | Detail |
|---|---|
| Settings | title, description, footer, theme, `is_public`, `show_uptime`, `show_charts`, `show_tags`, `show_cert_expiry`, `show_domain_expiry`, `uptime_window_hours`, `custom_css` |
| `copy_monitors` (default `true`) | the explicit rows of `status_page_monitors`: monitor, order, `display_name`, `group_name`, `show_uptime`, `show_chart` |
| `copy_groups` (default `true`) | the rows of `status_page_groups`: group, order, `display_name` |
| Subscription config | `subscription_smtp_uuid` and `subscription_delay_minutes`, **inert** |

| Stays behind | Why |
|---|---|
| uuid, revision, origin node, timestamps | the copy is a new page (`revision = 1`, origin this node), so the peers see a create and never an edit of the source |
| `subscription_enabled` | a duplicate must never mail the subscribers of another page; keeping the channel makes re-enabling one click, and a disabled page skips `validateSubscription` |
| subscribers, incidents, notices | PII of another page and a history nobody lived through |

Identity: title `<title> (copy)`, slug `<slug>-copy` → `-copy-2`, `-copy-3`… on a
collision; both are cut to the column width (200 / 120) with the suffix reserved, so
a page at the limit still clones.

## 3. Implementation

| Where | Change |
|---|---|
| `internal/services/statuspage.go` | `Create` is now a wrapper over the **new** `createWithSelection(ctx, page, items, links)`: page + selection + groups in one transaction, one revision, one outbox row. `createStatusPageRow` is **new** (see §4) |
| `internal/services/statuspage_public.go` | `statusPageMonitorRows` **new** (id list → rows with both switches on), `writeStatusPageItems` **new** (keeps order and overrides, drops a duplicate id), `writeMonitorSelection` now delegates |
| `internal/services/statuspage_groups.go` | `writeStatusPageGroupLinks` **new** (keeps order and display names, drops a duplicate group instead of a 500); `SetGroups` delegates to it |
| `internal/services/statuspage_clone.go` | **new**: `StatusPageCloneOptions`, `Clone`, `statusPageCloneIdentity`, `statusPageFitText`, `uniqueSlug`, `statusPageClone`, `statusPageCloneItems`, `statusPageCloneGroupLinks` |
| `internal/handlers/statuspages.go` | `cloneStatusPage`: empty body valid, `copy_monitors`/`copy_groups` as `*bool` so an omitted field keeps `true` (the template clone body, verbatim) |
| `internal/handlers/router_admin.go` | `POST /api/status-pages/:id/clone` in the `pages` group |
| `web/src/lib/types-platform.ts` | `StatusPageCloneOptions` |
| `web/src/lib/api-admin.ts` | `adminApi.cloneStatusPage` |
| `web/src/views/admin/AdminStatusPagesView.vue` | copy button (between Subscribers and Edit) + clone dialog: title, slug, the two switches, a static note about subscriptions, ids `#status-page-clone-name` / `#status-page-clone-slug` |
| `web/src/locales/*.json` (7) | 10 new `statusPages.*` keys, translated |
| `internal/services/sync_inventory_test.go` | allow-list: `writeStatusPageItems`, `writeStatusPageGroupLinks` (`caller publishes the page`) |

## 4. The `default:true` trap (found while writing the clone)

`models.StatusPage.IsPublic`, `ShowUptime` and `ShowCharts` — and
`models.StatusPageMonitor.ShowUptime` / `ShowChart` — carry
`gorm:"not null;default:true"`. GORM **replaces a zero value with the column default
on insert** and writes the default back into the struct
(`callbacks/create.go` + `schema/field.go`), so `tx.Create` with `false` inserts
`true`. A clone of a private page would have gone live, silently.

Two helpers fix that on the two write paths that can carry a `false`:

- `createStatusPageRow` captures the three booleans before the insert and, when any
  is false, writes them back with `UpdateColumns` (no hooks, no `updated_at`) and
  restores the struct so the response matches the row;
- `writeStatusPageItems` does the same for the two per-monitor switches, per row,
  only when one of them is off.

This also fixes the pre-existing create path: `POST /api/status-pages` with
`"is_public": false` used to store (and answer) `true`. `Update` was never affected —
it writes a `map[string]any`.

## 5. Tests

| File | What it pins |
|---|---|
| `internal/services/statuspage_clone_test.go` | `TestStatusPageClone` (every setting travels, identity blank, subscription inert), `TestStatusPageCloneIdentity` (defaults, caller names, the two column limits, trailing dash), `TestStatusPageCloneSelection` (order + overrides travel, row identity stays), `TestStatusPageMonitorRows` (both switches on) |
| `internal/services/statuspage_clone_integration_test.go` | **new, opt-in** (`UP_SCRATCH_DSN`) `TestStatusPageCloneLive`: full copy end to end, the `default:true` columns keep their `false`, one outbox row, subscribers and the source selection untouched, slug counter on the second copy, both switches off, unknown source |
| `internal/services/sync_inventory_test.go` | the two new writers stay on the allow-list with a reason |

## 6. Validation

- `gofmt -l internal`, `go vet ./internal/...`, `go build ./...` — clean.
- `go test ./internal/services -run 'TestStatusPageClone|TestStatusPageMonitorRows|TestPlanStatusPage|TestEverySyncedWritePublishes|TestApplySwitchCoversEverySyncedEntity' -v` — pass (19 write paths inspected).
- **Live**, against a throwaway MariaDB 11.4 (`UP_SCRATCH_DSN=…:3309`, schema created
  by booting the server once): `go test ./internal/services -run
  TestStatusPageCloneLive -v` — pass.
- `cd web && npm run typecheck && npm run check:i18n && npm run build` — clean
  (1 028 messages per locale, 874 used keys checked in every locale); `web/dist`
  rebuilt.
- `go test ./...` — see §7.

## 7. Results

`go test ./...` — clean (14 packages `ok`, the rest without test files; the opt-in
integration tests skip without `UP_SCRATCH_DSN`). The clone itself was run live once
against the throwaway MariaDB, §6.

## 8. Documentation

`docs/status-pages.md` §3 gained the clone row and the new **§3.2 Clone a page**
(what travels, what stays, the defaults, the one-transaction guarantee);
`docs/api.md` §6 lists `POST /api/status-pages/:id/clone`; `README.md` mentions the
status page clone next to the other clones.

