# Plan - Bulk import ceiling (500 -> 2000) + mobile header

> **Plan** — task: *a paste of 1700 URLs answers "Invalid bulk request"; review the bulk
> importer and raise its limit to 2k unless there is a reason to keep it, and drop the instance
> title from the header on mobile*, worked on **2026-09-27**. Local-only working note
> (`plans/`, tracked in git), kept because §2 records why the ceiling stays (and at which value)
> although the number moved, and why the scheduler hand-off had to change with it.

## 1. Goal (as asked)

1. Adding **1700 URLs in bulk** fails with `Invalid bulk request`. Review the importer, find the
   rejection and raise the limit to **2k** — unless the review finds a reason to keep a lower
   ceiling.
2. **Mobile view**: remove the instance title from the header so the locale switcher, the theme
   toggle and the account button get the room.

## 2. What the review found

The only rejection path is `maxBulkRows` in `internal/services/monitor_bulk.go`: **500 rows**.
`BulkCreate` answered `400 ERR_MONITOR_BULK_INVALID` ("too many rows: 1700, the limit is 500")
before validating anything. Everything else in the path scales linearly and accepts a 1700 row
paste:

- **No body cap.** Gin reads the body through `c.ShouldBindJSON` with no `MaxBytesReader`, the
  server (`cmd/server/app.go`) sets `ReadTimeout: 60s` (the small body is read long before it),
  `WriteTimeout: 0`, and the shipped stack has **no reverse proxy** (`docker/` runs the app
  directly). A 2k row paste of `name,url` is ~50–400 kB, well under the `client_max_body_size`
  of any proxy an operator might add.
- **No frontend cap.** `BulkAddDialog.vue` has no `maxlength` and sends the paste as-is; the
  "invalid bulk request" the operator saw was the backend code, translated by `translateError`.
- **The per-row work is bounded by the paste, not quadratic in it** (with one exception noted
  below): the duplicates are checked against a map loaded once (`existingMonitorKeys`), each row
  creates its own transaction, and the report is one entry per row.
- **The one real reason to keep a ceiling** is that a paste is not a data import pipeline: the
  whole text is parsed into memory, 2k rows mean 2k transactions and 2k scheduler commands, and
  an unbounded request would let one paste monopolise the process. `maxBulkRows` therefore stays,
  now at **2000** (`2k`), and the comment states what would have to change to raise it further.

## 3. Decisions taken

- **`maxBulkRows` 500 → 2000.** 2000 covers a real spreadsheet export with room to spare (the
  reported 1700 fits) while keeping the request short-lived. The message still names the real
  numbers: `too many rows: N, the limit is 2000`.
- **The check is a named helper.** `bulkRowLimitError(count int) error` is split out of
  `BulkCreate` so the ceiling and the code it answers (`ERR_MONITOR_BULK_INVALID`) are pinned by
  `TestBulkRowLimit`, a unit test that needs no database. The test also parses the exact reported
  case (1700 `site,https://example.com` lines) to prove it is now accepted.
- **The scheduler hand-off changes with the limit.** The handler used to enqueue one
  `Scheduler.Upsert` per created monitor. The scheduler's command channel buffers **64** commands
  and drops what does not fit (`default:` in `Scheduler.Upsert`), so a 2k import would silently
  leave most new monitors unscheduled until the periodic reconciliation. `scheduleUpserts` now
  emits one upsert per monitor while the batch fits the buffer and a single `Reload()` above
  `bulkScheduleThreshold = 64`: one database walk configures them all, the same hand-off
  `link-all` already uses.
- **No new translation keys, no protocol bump, no migration.** The limit is a backend constant
  and the wording of the error is a backend message, so the seven locales and the cluster wire
  format are untouched.
- **Mobile header: hide the name, keep the logo.** The instance name `<span>` becomes
  `hidden … sm:inline`: on phones only the logo remains as the home link, and its `alt` text still
  carries the instance name for screen readers. `sm` is the breakpoint the header already uses for
  its other text (the realtime badge and the account e-mail are both `sm:inline`), so everything
  that competes for the header appears and disappears at the same width.

## 4. Files touched

| Layer | File |
|---|---|
| Service | `internal/services/monitor_bulk.go` (constant + comment), `monitor_bulk_create.go` (`bulkRowLimitError`) |
| Handler | `internal/handlers/scheduling.go` (`scheduleUpserts` + `bulkScheduleThreshold`), `monitor_templates.go` (bulk create uses it) |
| Test | `internal/services/monitor_bulk_test.go` (`TestBulkRowLimit`) |
| Web | `web/src/components/layout/AppHeader.vue` (title hidden below `sm`) |
| Docs | `docs/monitors.md` (the row ceiling, now 2000 and explained) |

## 5. Verification commands

```bash
cd /home/ivan/Documents/Git/up
gofmt -l internal/services internal/handlers
go build ./... && go vet ./internal/services/ ./internal/handlers/
go test ./...                                    # incl. TestBulkRowLimit
cd web && npm run typecheck && npm run check:i18n
```

Result (2026-09-27, Node 18.19.1): `gofmt` clean, `go build`/`go vet` clean, `go test ./...` all
packages ok, `TestBulkRowLimit` PASS, `typecheck` clean, `check:i18n` ok (621 keys, 5201 messages;
no key added or removed). `npm run build` still needs Node >= 20.19 / 22.12 (vite 8) and was not
run, exactly as the previous plans note.

## 6. Risks / notes

- **A 2k import is a slow request on purpose.** It is still one transaction **per row**, so the
  browser waits for the whole paste; the dialog already shows a spinner on *Create* and the report
  when it returns. Making it faster means batching the inserts, which is a separate change — the
  ceiling is what bounds the worst case until then.
- **The dialog dry-runs while typing.** The preview is debounced 400 ms and asks the server to
  validate the paste on every pause, so typing into a 2k-line paste costs repeated full
  validations. It is the same dry run as before, just on a bigger paste; if it becomes noticeable
  the fix is to require an explicit *Preview* above a row count, not to lower the ceiling.
- **One path is quadratic inside a single row.** `Create` → `resolveDomainExpiresAt` scans the
  monitors at each insert when domain watching is on, so a 2k import of domain watchers does more
  total work than 2k independent inserts. It was already true at 500; the ceiling does not make it
  wrong, and fixing it (a batched insert) is out of scope here.
- **`existingMonitorKeys` loads every monitor** into two maps for the duplicate check. At current
  sizes this is the simplest correct choice; a very large installation would want a targeted query.
- **An external reverse proxy** an operator adds in front (Traefik/Nginx/Caddy/Cloudflare Tunnel,
  see `docker/.env.example`) can impose its own body limit. 2k rows stay far below the common 1 MB
  default, but a proxy with a lower cap must be adjusted — that is operator configuration, not a
  reason to keep the 500 row ceiling.
- **No cluster impact.** No model, payload or protocol version changed, so nodes do not need to be
  upgraded together for this.

