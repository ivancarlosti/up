# Plan - The scope of a link run is the scope of the template

> **Plan** — task: *changing a template's link from "all monitors of type" to "monitors of
> specific groups" does not persist: the chosen groups are ignored (the monitors outside them keep
> following the template) and the setup is not remembered when the dialog is reopened*, worked on
> **2026-09-28**. Working note (`plans/`, tracked in git), kept because §2 records the
> semantics decision behind the fix and §3 lists every surface that shows the counts.

## 1. Goal (as asked)

1. **A group-scoped run replaces the scope of the template.** After linking "only the monitors in
   the selected groups", exactly the monitors of those groups follow the template: the monitors
   that followed it before and live outside the selection stop following it.
2. **The dry run says so before anything is written** (how many monitors start following, how many
   keep a different value, how many stop following), and the dialog warns in words.
3. **The result reports what happened** (the toast includes the monitors that stopped following).

## 2. Decisions taken

- **Authoritative re-scope, the operator's choice.** The alternative — an additive run plus a
  second "also unlink" switch — was rejected: the dialog already asks for a scope, so the honest
  reading of "only monitors in the selected groups" is that everything else is out of scope. The
  preview + the warning make the consequence visible before the click, which is what a switch
  would have added anyway.
- **"Every monitor of this type" never unlinks.** Everything of the template type is inside that
  scope by definition, so there is nothing outside it to detach. Only a run that carries
  `group_ids` re-scopes (the same rule that already distinguishes the two scopes).
- **A group with no member is still an empty scope** (documented since the first version of the
  feature): it links nobody and, now, also detaches everybody. That is the consistent extension of
  the rule "never *everything*", and the preview shows `monitors: 0` before the click.
- **A monitor is detached, never deleted.** The detach clears `template_uuid` through
  `MonitorService.Update`, i.e. the normal monitor write: it validates the row, bumps the revision
  (the merge order of the cluster) and publishes the monitor and its relations, so the peers stop
  applying the template too. A direct `UPDATE` would have left every node's outbox unaware of the
  change — the same reason the template delete path clears the column inside the service layer.
- **A monitor that cannot be detached is reported, not forced.** `Update` validates: a follower
  with a row that no longer validates (legacy data) keeps following the template, the failure is
  logged (`could not detach the monitor from the template`) and it is not counted, so the toast
  never claims a detach that did not happen.
- **The preview counts the detach, so the UI has three numbers.** `monitors` (the scope),
  `linked`/`updated` (what the scope gets) and the new `unlinked` (what the scope leaves behind).
  The group-scoped wording is a separate message key per surface instead of a conditional sentence
  inside one key: a translator cannot put a plural form in the wrong branch of an `if`, and the
  "every monitor of this type" wording stays untouched.
- **The scope is not stored on the template.** The scope of a run is the *effect* of the run (the
  `template_uuid` of each monitor), which is what the follower count and the monitors table already
  show. Remembering the last selection in the dialog was considered and left out: it would survive
  as UI state only (a reload forgets it) and would have suggested a stored scope that does not
  exist.

## 3. Files touched

| Layer | File | Change |
|---|---|---|
| Service | `internal/services/monitor_template_link.go` | `TemplateLinkResult.Unlinked`, pure `unlinkScope`, `unlinkOutOfScope`, `LinkAll` re-scopes when `group_ids` is sent, log/publish payload carry the count |
| Handler | `internal/handlers/monitor_templates.go` | docstring of `linkAllMonitorTemplate` (no logic change: the result is passed through) |
| Tests | `internal/services/monitor_template_link_test.go` | `TestUnlinkScope` (pure scope rule: nobody, some, everybody) |
| Tests | `internal/services/monitor_template_link_integration_test.go` | opt-in (`UP_SCRATCH_DSN`) `TestLinkAllRescopesLive`: a real re-scope, the dry run writing nothing, the detach keeping the monitor |
| Web | `web/src/lib/types.ts` | `TemplateLinkResult.unlinked` |
| Web | `web/src/views/admin/AdminMonitorTemplatesView.vue` | dialog description, preview line and toast switch to the group wording when `unlinked`/the group scope applies |
| Web | `web/src/locales/*.json` (7 locales) | `templates.linkAllWarningGroups`, `templates.linkAllPreviewGroups`, `templates.linkAllDoneGroups` |
| e2e | `web/scripts/e2e-templates.mjs` | a group-scoped run through the dialog: preview counts, one monitor detached, the in-scope one kept |
| Docs | `docs/monitors.md`, `docs/api.md` | the authoritative scope rule and the `unlinked` field |

## 4. Verification

1. `go build ./... && go vet ./... && go test ./internal/...`.
2. `npm run check:i18n` and `npm run build` in `web/`.
3. `/tmp/repro-link-ui.mjs` (headless Chrome, clean database): link every monitor of the type,
   then switch to one group and confirm that the out-of-scope follower appears in the preview and
   is detached; `/tmp/verify-link.sh` for the API-level pass.
4. `npm run e2e:templates -- --url http://127.0.0.1:3005` against the live instance.
5. `UP_SCRATCH_DSN=… go test ./internal/services/ -run TestLinkAllRescopesLive -v`.
