# Plan - A `websocket` monitor type (`ws://` / `wss://`)

> Task: add a probe that opens a WebSocket connection, with the common
> authentication, an optional `send`/`expect` frame exchange and certificate
> watching for `wss://` targets — and track the `plans/` directory in git.

## 1. Goal

A new monitor `type: "websocket"` whose target is an absolute `ws://` or `wss://`
URL. The probe performs the RFC 6455 handshake; `up` means the server answered
`101` with a valid `Sec-WebSocket-Accept`, `down` means the upgrade was refused
(404, stripped `Upgrade` header, TLS failure). `headers`, `auth_type`
(none/basic/bearer) and `ignore_tls` are reused as-is, `send`/`expect` reuse the
TCP fields (one text frame after the upgrade, substring search in the answer) and
a `wss://` target feeds the existing certificate watch (`cert_watch`, badges,
daily reminders) with no new machinery.

## 2. Decisions

- **No new dependency.** `github.com/gorilla/websocket v1.5.3` is already a direct
  dependency (`internal/ws/hub.go`). Its `Dialer` exposes `NetDialContext` (the
  address family plan), `TLSClientConfig` (`ignore_tls`) and `HandshakeTimeout`,
  and `Conn.UnderlyingConn()` is the `*tls.Conn` of a `wss://` handshake, so the
  leaf certificate is read exactly like the `ssl` probe reads it.
- **No migration.** `monitors.type` is `size:20` and `"websocket"` is 9 chars.
- **No schema change.** Every option already exists on the flat `MonitorConfig`
  (`url`, `headers`, the auth triplet, `ignore_tls`, `ip_family`, `send`,
  `expect`), which is why the type is the only thing to add.
- **The handshake is the probe.** No `accepted_status_codes`, `method`, `body` or
  `cache_buster`: the upgrade is always a `GET` and `101` is the only accepted
  answer, so the type does not pretend to speak HTTP.
- **Certificates for free.** `SupportsCertificate()` gains the type, which wires
  `expiry.CertificateTargetFor` (scheme `wss`), the daily job, the UI badges and
  the reminders without touching them.
- **`send`/`expect` instead of a scripted protocol.** One text frame out, a
  substring search over the frames that come back (a greeting is skipped), the
  whole read bounded by the monitor timeout and `maxBodyBytes`.
- `plans/` is documented work, not scratch: un-ignore it and commit the plans.

## 3. Changes

| file | change |
|---|---|
| `internal/models/enums.go` | `MonitorTypeWebSocket`; added to `AllMonitorTypes`, `SupportsCertificate`, `SupportsDomainWatch`, `Valid` |
| `internal/models/monitor_config.go` | `Normalize` (trim the url, default `auth_type`) and `PruneToType` (url/headers/auth/ignore_tls/send/expect/ip_family) |
| `internal/models/monitor_validate.go` | `case MonitorTypeWebSocket` (url required + `ws://`/`wss://`, auth) and the extracted `validateAuthConfig` shared with the HTTP branch |
| `internal/models/monitor_template.go` | `TargetField` -> `url`; the `cert_watch` message list |
| `internal/checkers/websocket.go` (new) | `checkWebSocket`: dialer + handshake headers, refused-upgrade message (`websocket upgrade failed: 404 Not Found`), `exchangeFrames`, `certificateFromConn` (and the certificate of a failed verification) |
| `internal/checkers/checker.go` | dispatch + package doc; `http.go` gains the shared `probeUserAgent` |
| `internal/checkers/websocket_test.go` (new) | handshake, send/expect (match, mismatch, greeting skipped), refused upgrade, unreachable, scheme guard, headers/basic/bearer, `wss` certificate (captured, captured on failure, not captured without `cert_watch`, none on `ws://`) |
| `internal/expiry/targets.go` | `CertificateTargetFor`/`DomainFor`/`hostFromMonitor` read a `wss://` url |
| `internal/services/monitor_validate.go`, `monitor_bulk_create.go`, `monitor_template_apply.go` | `cert_watch` message, `ws://`/`wss://` target column, `withProbeTarget` url |
| `internal/notify/message.go` | `MonitorTarget` renders the url |
| tests (`monitor_config_test.go`, `monitor_validate_test.go`, `monitor_template_test.go`, `expiry/targets_test.go`, `services/monitor_bulk_test.go`, `monitor_template_apply_test.go`) | websocket cases added to the per-type loops |
| `web/src/lib/types.ts`, `monitor-config.ts` | `'websocket'`, `CONFIG_FIELDS`, `supportsCertificate`, `supportsDomainWatch`, `monitorTarget` |
| `web/src/components/monitors/MonitorConfigFields.vue`, `MonitorForm.vue`, `web/src/views/DashboardView.vue`, `web/src/views/admin/AdminMonitorTemplatesView.vue` | the type in the pickers, the websocket option section, the template auth note |
| the seven locales | `monitor.typeWebSocket`, `monitor.wsHelp` |
| `docs/monitors.md` (§1/§8/§9, new §12, §13/§14 renumbered), `docs/api.md`, `docs/architecture.md`, `README.md`, `docs/status-pages.md` | documentation |
| `.gitignore` | `/plans/` removed (the plan docs are tracked) |

## 4. Verification

| command | result |
|---|---|
| `gofmt -l .` (inside `golang:1.27.1-alpine`) | new/changed files clean (`internal/database/rollup.go`, `cmd/server/main.go` were already unformatted before this change) |
| `go build ./...` | ok |
| `go vet ./...` | ok |
| `go test ./internal/...` | every package ok (incl. the new `TestCheckWebSocket*`) |
| `npx vue-tsc --noEmit` (web, `node:22-alpine`) | ok |
| `npm run check:i18n` | ok (847 messages per locale, 716 keys used) |

Go is not installed in the work container; the commands ran inside
`golang:1.27.1-alpine` (warm `up-gomod`/`up-gocache` volumes) and the frontend
checks inside `node:22-alpine`.

## 5. Out of scope

- `Subprotocols` (`Sec-WebSocket-Protocol`) — no option for it yet.
- Payload matching beyond the `expect` substring (JSONPath, regex, frame count).
- `web/scripts/seed-demo.mjs` and the e2e scripts: no websocket row is seeded.
- Message-level `ping`/`pong` keepalives: the handshake plus the timeout bound the
  probe already.
