# Plan - Notifications link to the page they are about

> **Plan** — task: *add to the notifications the possibility to deliver the dashboard URL of the
> specific notification: for an up/down http, dns… alert send the monitor URL
> (`https://br01.up.icc.gg/monitors/445`), for a domain/certificate expiration send the generic
> page because it can back more than one URL (`https://br01.up.icc.gg/admin/expiry`)*, worked on
> **2026-09-28**. Working note (`plans/`, tracked in git), kept because §2 records the
> routing decision behind each target page (and why the expiry events cannot use a monitor link),
> and §3 lists exactly which surface each channel prints the link on.

## 1. Goal (as asked)

1. **Every notification carries the URL of the page it is about.** A status event points at its
   monitor detail page; an expiration reminder points at the expiry worklist.
2. **The link is derived, not configured.** No new field in the channel dialog, no new setting: the
   event already says which page is right, and `APP_URL` already says what the origin is.
3. **All the surfaces use it** — the plain text body (SMTP, Slack, Discord, Telegram) and the
   webhook payload, both the default JSON body and the template pre-filled in the dialog.

## 2. Decisions taken

- **One new field, `Message.DashboardURL`.** `Message.InstanceURL` (`APP_URL`) is kept as-is: it is
  documented, it is in the webhook payload and a stored template may already read
  `{{.InstanceURL}}`. The new field is the *specific* page; `Message.Link()` picks the specific one
  and falls back to the instance root, so a body that has neither templated the new field nor an
  `APP_URL` renders exactly as before.
- **The routing table is derived from the event, not from the monitor type.** The monitor type
  (`http`, `dns`, `tcp`, …) does not change which page shows the alert — every one of them has a
  `/monitors/<id>` page — while `cert_*` / `domain_*` do, because they are the only events whose
  subject is not the monitor.
- **`/monitors/<id>` for `up` and `down`.** That is the page with the statistics, the event log and
  the heartbeats of the alerting monitor, i.e. the thing an operator opens first. `test` has no
  stored monitor at all (the sample monitor is not in the database), so `/monitors/0` would be a
  dead link and the fallback is the dashboard root.
- **`/admin/expiry` for `cert_expiring`, `cert_expired`, `domain_expiring`, `domain_expired`.** As
  asked: a certificate belongs to a *host* and a registration to a *domain*, and the worklist
  deduplicates them, so one entry can back many monitors and no single `/monitors/<id>` page tells
  the whole story. The expiry page is the only place that shows the target, its date and every
  monitor that shares it.
- **The path is glued under a trimmed `APP_URL`.** `config.trimURL` already stores `APP_URL` without
  a trailing slash, but the helper trims again so a hand-written value (`https://up.example.com/`)
  cannot produce a `//monitors/445`. An empty `APP_URL` yields an empty link rather than a relative
  path: a webhook consumer receiving `"/monitors/445"` cannot resolve it, and an empty string is the
  honest "not configured".
- **The default webhook template gains one line** (`"dashboard_url": "{{.DashboardURL}}"`). Only
  channels created or saved *after* the change use it — `Normalize` fills `body_template` when the
  field is empty, and an existing row keeps its stored template — which is the same behaviour the
  tags/groups line had in `0ed0ca7`.
- **The SPA routes are mirrored, not invented.** `/monitors/:id` and `/admin/expiry` are the two
  entries of `web/src/router/index.ts`; the constants in `notify/message.go` name them and the
  docstring points at the router so a route rename has one place to look.

## 3. Files touched

| Layer | File | Change |
|---|---|---|
| Engine | `internal/notify/message.go` | `Message.DashboardURL` field, `monitorsPathPrefix` / `ExpiryPath` constants, `DashboardLink(event, monitorID, instanceURL)`, `Message.Link()`, `NewMessage` populates the field, `jsonPayload` gains `dashboard_url` |
| Engine | `internal/notify/text.go` | `Text()` and `HTML()` print `Message.Link()` (the event page, falling back to `APP_URL`) instead of the instance root |
| Models | `internal/models/notification.go` | `DefaultWebhookBodyTemplate` gains `"dashboard_url": "{{.DashboardURL}}"`, so a channel created or saved from the dialog sends the link |
| Tests | `internal/notify/notify_test.go` | `TestDashboardLink` (11 cases) and `TestMessageCarriesTheEventPage` (struct field, `Link()`, default payload, dialog template, text body, HTML body, expiry counterpart, no-`APP_URL` fallback) |
| Docs | `docs/notifications.md` | default template block, `jsonPayload` extras, the two new/updated placeholder rows and the new *Where a notification links to* table |
| Web | `src/locales/*.json` (7) | `notifications.webhookBodyHelp` lists `{{.DashboardURL}}` next to the other examples |

Nothing else needed a touch: `Notification.Normalize` and `handlers/notifications.go` only copy
`DefaultWebhookBodyTemplate` around, the delivery log stores no message body (so no migration), and
the sync payload is untouched (the field is derived on the sending node only), so nodes can be
upgraded one by one.

## 4. Which URL each channel receives

| Channel | Surface | Example |
|---|---|---|
| SMTP (text) | `Dashboard:` line at the end of the body | `Dashboard: https://br01.up.icc.gg/monitors/445` |
| SMTP (HTML) | an `<a href>` under the table | `…/monitors/445` |
| Slack / Discord / Telegram | the same plain text body (they share `Message.Text()`) | `…/monitors/445` |
| Webhook (no template stored) | `dashboard_url` of the default JSON payload | `"dashboard_url":"https://br01.up.icc.gg/monitors/445"` |
| Webhook (dialog template) | `{{.DashboardURL}}` | as above |
| Certificate / domain reminder | any of the above, pointing at the worklist | `https://br01.up.icc.gg/admin/expiry` |

## 5. Verification commands

```bash
# Go toolchain of the module cache (no system-wide go in this sandbox)
export PATH="/home/ivan/go/pkg/mod/golang.org/toolchain@v0.0.1-go1.26.3.linux-amd64/bin:$PATH"
gofmt -l internal/notify internal/models && go vet ./... && go test ./...
go test ./internal/notify/ -run 'DashboardLink|EventPage' -v

cd web && npm run typecheck && npm run check:i18n
```

Results (2026-09-28):

- `gofmt -l` → nothing; `go vet ./...` → nothing; `go test ./...` → `ok` in every package that has
  tests (checkers, database, expiry, handlers, middleware, models, notify, scheduler, services,
  utils). The two live tests skip themselves without `UP_SCRATCH_DSN`.
- `go test ./internal/notify/ -run 'DashboardLink|EventPage' -v` → `TestDashboardLink` and
  `TestMessageCarriesTheEventPage` pass; the second one also pins the "no `APP_URL`" path (no
  `Dashboard:` line, no anchor, `Link()` empty) and that `{{.DashboardURL}}` renders through the
  dialog template as valid JSON.
- `npm run typecheck` (`vue-tsc --noEmit`) → clean; `npm run check:i18n` → `ok 5201 messages
  compiled`, 743 messages per locale, 622 keys used by the source present in every locale (the help
  text edit adds no key).
- `npm run build` cannot run here: `vite` imports `node:util`'s `styleText`, which the sandbox's
  Node 18.19 does not provide (CI uses Node 20). No frontend source file changed, only locale
  strings, so the type/i18n gates are the meaningful ones.

## 6. Risks / notes

- **The link needs a session.** `/monitors/<id>`, `/admin/expiry` and `/` are authenticated routes,
  so a recipient who is not logged in (a Telegram channel read by an operator without a dashboard
  account) lands on the login page. That was already true of the old `Dashboard:` line, which
  pointed at the same authenticated root.
- **`APP_URL` is the only origin.** An instance reachable under several hostnames sends the one
  configured in `APP_URL`; that is the same value the Keycloak callback and the cluster join links
  use, so there is one place to fix.
- **A stored webhook template keeps its old body.** Existing channels do not gain `dashboard_url`
  until the line is added to their `body_template` (emptying the field before saving restores the
  new default). Nothing breaks: the payload keeps every field it had.
- **`EventTest` links to the dashboard root, not to a monitor page** (and to nothing when `APP_URL`
  is unset): the sample monitor is not stored, and pointing a test at `/monitors/0` would be a 404
  the operator would file as a bug.
- **The expiry events are the ones that can look "imprecise"**: the message names a monitor while
  the link opens the worklist. That is deliberate - the date belongs to the certificate/domain, and
  the worklist shows every monitor that shares it, which is the more useful page.
