# Plan - Latency chart context + one expiry switch per badge

> **Plan** — task: *give the monitor detail latency chart min/avg/max context and split the
> public status page expiry toggle into a certificate and a domain one*, worked on **2026-09-27**.
> Working note (`plans/`, tracked in git), kept because §2 and §6 record decisions the
> shipped code only states in passing: why the version moved to 3, and why no migration was written.

## 1. Goal (as asked)

1. The **"Latency over the period"** graph of a monitor must say what the numbers are: the period,
   how many samples are drawn, the min / avg / max of those samples, and why some bars are dimmed.
2. The **public status page** must not hide two different facts behind one switch: the expiry of
   the TLS **certificate** and the expiry of the **domain registration** become two controls, and
   the badges they publish must be labelled, because a bare "42 days" tells a visitor nothing.

## 2. Decisions taken

- **No data migration.** `StatusPage.ShowCertExpiry` / `ShowDomainExpiry` replace `ShowExpiry`,
  both `gorm:"not null;default:false"`. `AutoMigrate` adds the two columns and never drops the
  legacy `show_expiry`, so it stays on an existing database as an inert column (~1 byte per page).
  Writing a migration to drop it would cost more than it saves and would make a rollback lossy.
  The orphan is documented in `docs/database.md`.
- **Protocol version 3.** The wire change REMOVES a field (`show_expiry`) instead of adding one: a
  version 2 receiver cannot see either new field, keeps its own switch and publishes a badge the
  origin hides. Add-only changes stay compatible (no bump); this one is not add-only, so
  `models.ProtocolVersion` moves to 3 and a mixed cluster refuses to sync with the known
  "incompatible" line instead of diverging silently.
- **Two switches, not one with a field selector.** They are independent columns, they travel as
  independent booleans, and `PublicPayload` strips each object on its own (`certificate` for the
  cert switch, `domain` for the domain one). A page that publishes the certificate does not
  publish the registration date of the domain as a side effect.
- **Labelled badges, no new wording.** The public badge reuses `certificate.column` /
  `domain.column` ("Certificate" / "Certificat" / "证书" …) in front of the existing
  `daysLeft`, so `Certificate: 12 days` needs **zero** new translation keys and stays consistent
  with the column headers of the admin tables.
- **Chart stays dependency free.** No charting library is added to `package.json`: the graph is
  still divs, now with a p90 scale label, a dashed average line and a min/avg/max legend that is
  computed from the plotted samples (not from the window) so the chart cannot contradict its own
  legend.
- **`monitorDetail.samples` / `scale` / `scaleHint` / `overScale`** are the only new message keys;
  the plural ones follow the file's existing convention (2 forms for en/es/fr/pt, a single form
  for ar/hi/zh).

## 3. Files touched

| Layer | File |
|---|---|
| Model | `internal/models/statuspage.go`, `internal/models/sync_payload.go`, `internal/models/sync.go` |
| Service | `internal/services/statuspage.go` (Update), `statuspage_public.go`, `sync_emit.go`, `sync_apply_entities.go` |
| Web | `views/MonitorDetailView.vue`, `views/StatusPagePublicView.vue`, `views/admin/AdminStatusPagesView.vue`, `lib/types-platform.ts`, `locales/*.json` (7) |
| Docs | `status-pages.md`, `database.md`, `public-api.md`, `monitors.md`, `clustering-modes.md` |
| E2E | `web/scripts/e2e-status-page-groups.mjs` |

## 4. How the chart reads now

- Header: title on the left, `24 h · 60 checks` on the right.
- Plot: bars, a `Scale: 240 ms` chip top-right with `scaleHint` in its tooltip, and a dashed line
  at the average with its own label.
- Legend under the plot: `Minimum 8 ms`, `Average 41 ms`, `Maximum 1.2 s`, `2 above the scale`
  (only when the p90 cap actually clipped something), plus the existing "oldest to newest".
- Empty period: the "No heartbeat recorded yet" line replaces the plot instead of an empty box.
- `role="img"` + `aria-label` summarising min/avg/max; each bar keeps a tooltip that is now
  `Up · 41 ms · 2026-09-27 04:10:12` instead of a bare `41 ms`.

## 5. Verification commands

```bash
gofmt -l . && go build ./... && go vet ./... && go test ./...
cd web && npm run typecheck && npm run check:i18n && npm run build
```

The `e2e:status-page-groups` script (real browser, real API) additionally flips only the
certificate switch and asserts that the anonymous payload publishes `show_cert_expiry: true`,
`show_domain_expiry: false` and no `domain` object.

## 6. Risks / notes

- **`npm run build` needs Node >= 20.19 / 22.12** (vite 8 loads `node:util.styleText`). The lab
  here has Node 18.19.1, where `npm run build` fails inside rolldown before reading any source:
  the local `dist/index.html` was rebuilt with a throwaway Node 22 in `/tmp`. Nothing else in the
  checkout needs it (`typecheck` and `check:i18n` run fine on 18).
- **Mixed cluster during the rollout**: a node still on protocol 2 will show `incompatible` until
  it is upgraded. That is the intended, documented behaviour — update the nodes together, or wait
  for the sync to refuse loudly instead of publishing a page the origin does not show.
- **The legacy `show_expiry` column keeps its old value** and is now read by nobody. A page that
  had the single switch on comes back with both new switches **off**: an operator decision is
  still required, and the alternative (guessing "on" for both) would publish the domain date of
  every page that only ever asked for the certificate.
