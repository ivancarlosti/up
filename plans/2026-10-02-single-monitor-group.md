# Plan - A monitor belongs to one group (two phases)

> **Plan** — task: *refactor the monitor groups from a list to a single group*, worked on
> **2026-10-02**, phase 1 (additive) shipped in this commit. Local-only working note
> (`plans/`, tracked in git), kept because §3 records the ORDER the consolidation must follow
> (a boot step in the wrong place fails the boot), §4 is the checklist phase 2 must complete
> and §5 records the two deviations from the first sketch of the plan.

## 1. Goal

1. **A monitor is in one group.** The grouping organises the list and publishes monitors on a
   status page; a monitor claimed by two groups appeared twice on a page that included both, and
   the "which group is this monitor in?" question had no answer an operator could predict.
2. **The API says it in the singular** (`group_id` / `group_name` / `group`), the way
   `notification_ids` says "the channels of this monitor".
3. **Nothing breaks for a client that still sends the list.** `group_ids` keeps being accepted
   (first entry honoured) and keeps being reported (a zero or one element list) for one release.
4. **The stored data matches the rule at boot**, once, without an operator touching a monitor: a
   monitor with three groups keeps the first group of the group list and drops the others.

## 2. Decisions taken

- **Two phases, not one.** Phase 1 (this commit) adds the singular keys, enforces the rule on the
  write paths and migrates the data; phase 2 (next commit) removes `group_ids`, the deprecated
  notification aliases and the template default list, and adds the unique index that makes the
  schema enforce what the code already does.
- **`group_id` wins when both keys are sent.** A request that names the singular key is the newer
  client, and a request that sends both is a client mid-upgrade: the singular value is what it
  means. `group_ids: []` (or `group_id: 0`) moves the monitor to no group; omitting both keeps the
  current group, exactly like `notification_ids`.
- **The group editor MOVES a monitor.** `PUT /api/monitor-groups/:id/monitors` deletes the
  memberships of the monitors it lists in the same statement that rewrites the members, so adding a
  monitor to a group takes it out of the one it was in. The sync apply path moves too
  (`linkRelation`): a peer that receives the add before the tombstone of the old group would
  otherwise hold the monitor in two groups.
- **The keeper is the FIRST group of the group list** (`monitor_groups.sort_order`, then the id).
  The membership `sort_order` is the position of a monitor INSIDE a group: it orders members, not
  groups, so it cannot decide which of two groups a monitor keeps.
- **The consolidation belongs to the boot, not to the read path.** `Decorate`, the dashboard and the
  notification payload resolve a single ref deterministically (first row by group order) so the API
  is coherent even before the pass runs, but the rows themselves are collapsed once, at boot.
- **The tombstones are emitted in the same transaction as the delete.** A federated peer keeps the
  memberships it was told about, and `reconcilePeer` pulls the peer snapshot on a checksum mismatch:
  a membership that was never tombstoned is a membership the peer hands back.
- **The notification keys stay, in the singular form.** `Message` gains `MonitorGroup` /
  `MonitorGroupID` and keeps `MonitorGroups` / `GroupsCSV` (plus `group` / `groups` / `groups_csv`
  in the default webhook payload) as deprecated aliases derived from the single value, so a webhook
  body an operator already stored keeps rendering.
- **Selection is not membership.** `MonitorFilter.GroupID`, the bulk-tag selection, the template
  link scope and the status page sections keep working with group ids: they *select* monitors, and a
  selection over several groups stays useful in a single-group world.

## 3. Order of the boot steps (load-bearing)

`migrate` → `backfill` → **`monitor_groups` (the consolidation)** → `template_auth` → rollups → seed.

- It runs AFTER `backfill` because the tombstones it publishes name the uuid of both ends, and a row
  that predates the sync identity has no uuid until the backfill stamped it.
- It reuses `boot.PhaseBackfilling`: it IS a data backfill (it rewrites rows an older release
  wrote), and reusing the phase keeps the progress the UI reports honest without adding a sentence
  to every locale.
- It is idempotent: a monitor with one membership (or none) is left completely alone, so every boot
  after the first changes nothing.

## 4. Phase 2 checklist (next commit)

1. `models/monitor.go`: drop `GroupIDs`; `models/monitor_template.go`: drop `Defaults.GroupIDs`.
2. `models/monitor_group.go`: `MonitorID` gets `uniqueIndex` (safe now: the data is clean and every
   write path keeps one row per monitor).
3. `handlers/monitors.go`: drop `GroupIDs` from `monitorPayload`, the list branch of
   `monitorGroupSelection` and the `group_ids` fallback of the bulk create;
   `handlers/monitor_templates.go` too.
4. `services`: drop `GroupsForMonitors` (no callers today), the list contract of
   `firstGroupID` and the template fallback of `bulkGroupIDs` (keep it if the template keeps a
   group).
5. `notify`: drop `MonitorGroups`, `GroupsCSV`, the `groups` / `groups_csv` payload keys and the
   `Groups` rows of the text/HTML bodies; drop `groups` / `group_ids` from the template blocks in
   `models.DefaultWebhookBodyTemplate` and `docs/notifications.md`.
6. `web/src/lib/types.ts`: drop the deprecated `group_ids` (Monitor, MonitorPayload, BulkOptions)
   and the `MonitorSortContext.groupNames` fallback; `internal/services/sync_emit.go` /
   `sync_apply.go`: drop the template default groups from the sync payload.
7. Docs: `docs/api.md`, `docs/monitors.md`, `docs/notifications.md`, `docs/status-pages.md`.

## 5. Deviations from the first sketch

- **The unique index on `monitor_group_members.monitor_id` is DEFERRED to phase 2.** `AutoMigrate`
  runs before the consolidation, so adding it now would fail the boot of every database that still
  holds a duplicate (the exact reason the pass exists). Phase 2 adds it on top of data that is
  already clean.
- **The consolidation moved after `backfill`** (the sketch had it before `migrate`), which is what
  makes the ordering above a non-issue.

## 6. How it was verified

- `go build ./...`, `go vet ./...`, `go test ./...` (the write-path inventory guard of
  `sync_inventory_test.go` included: `setMonitorGroup` and `linkRelation` are on its allow-list with
  their reason).
- New unit tests: `monitorGroupSelection` + the payload shadowing of `group_id`
  (`internal/handlers/monitors_group_test.go`), `firstGroupID` / `bulkGroupIDs`
  (`internal/services/monitor_group_single_test.go`).
- New opt-in integration test `TestSingleGroupLive` (MariaDB): the consolidation keeps the first
  group of the list, is idempotent, and the write paths keep one group (`setMonitorGroup` with a
  list / nil / empty / an unknown id, and the group editor move). `TestNotificationGroupRefLive` was
  rewritten to pin the group ORDER (never the position inside a group) and the id tie-break.
- Live drill against a real MariaDB 11 container and the real binary: boot on a database holding a
  monitor in three groups (the keeper is the first group of the list, two memberships removed, the
  step reported as `monitor_groups_ms`), a second boot that changes nothing, the API round trips
  (`group_id`, `group_ids: [a,b]`, `group_id: 0`, omitted), the group editor move, and the bulk
  create fallback to the template group.
- Frontend: `npm run typecheck` and `npm run check:i18n` pass. `npm run build` cannot run in this
  sandbox (vite 8 needs Node >= 20.19; the container lacks the matching native rolldown binding).

## 7. Follow-up - the nested `group` object is gone

The singular phase left THREE fields for the same fact on `models.Monitor`: `group_id`, `group_name`
and a nested `group: {id, name}` object filled by `applySources`. The nested object duplicated the
pair exactly (same id, same name) and no consumer read it: no Go caller of `Monitor.Group`, and no
frontend reader of `monitor.group` (the `web/src/lib/types.ts` field was declared, never used). It
was dropped, so the payload keeps the flat pair only.

- Why flat wins: `group_id` is already the write key (`monitorPayload.GroupID`, `?group_id=` filter)
  and `group_name` is what the table and `sort.ts` render, so flat is the established contract. It
  also matches how a group travels in the notification payload (`group` = name, `group_id` = id) and
  on the status pages.
- `models.MonitorGroupRef` is kept: it is only the internal `(id, name)` helper of
  `GroupRefsByMonitor` / `groupRef` / `notify.NewMessage`, not a payload field.
- `monitor_clone.go` still reads `source.GroupID`, which is the flat field that stays.
- `internal/models/sync_payload.go` was NOT touched: its `MonitorPayload` deliberately carries no
  group (relations travel as `EntityMonitorGroupMember`).
- Pinned by `TestMonitorJSONExposesTheFlatGroupOnly` (`internal/handlers/monitors_group_test.go`): a
  marshalled monitor carries `group_id` / `group_name` and no `group` key.
- Docs updated: `docs/api.md` §12 and `docs/monitors.md` §5 no longer mention the nested object.
