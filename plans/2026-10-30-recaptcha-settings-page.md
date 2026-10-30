# Plan - reCAPTCHA moves from the environment to Admin > Settings

> Task: stop configuring the Google reCAPTCHA keys through `.env`. The site key
> and the secret become two rows of the `settings` table, edited in a new card of
> Admin > Settings ("Login protection"), with `RECAPTCHA_CLIENTID` /
> `RECAPTCHA_CLIENTSECRET` removed from the configuration entirely.

## 1. Goal

The reCAPTCHA pair was the last piece of authentication state that lived only in
the environment, and it was the more annoying one: rotating the secret meant
editing a file, restarting the process, and doing it again on every node. Every
other runtime knob (locale, timezone, retention, PageSpeed key, session secret,
cluster key) already lives in the shared `settings` table and is edited in the
dashboard, so the captcha was both an outlier and a support burden.

Accepting the goal also means accepting its cost: the variables are **removed**,
not deprecated. A deployment that upgrades and does nothing loses the captcha
until an operator pastes the two keys in the settings page once. The release
answers with a boot `WARN` while the old variables are still set, so nobody has
to guess why the widget disappeared.

## 2. Decisions

- **D1 - two keys, no "enabled" flag.** `settings.recaptcha_client_id` and
  `settings.recaptcha_client_secret` (`models.SettingRecaptchaClientID`,
  `models.SettingRecaptchaClientSecret`). Enablement is derived:
  `RecaptchaSettings.Enabled()` is `client_id != "" && client_secret != ""`, and
  `SettingService.RecaptchaEnabled()` is what the handlers ask. A third "enabled"
  row could contradict the pair (on, with no secret) and would have to be
  re-validated everywhere.
- **D2 - both or neither.** `RecaptchaSettings.Validate()` rejects a half filled
  pair with `400 ERR_VALIDATION`. A site key without a secret renders a widget
  whose token the server cannot verify (every login fails), and a secret without
  a site key demands a token no client can produce. Clearing both is the way to
  switch the captcha off, and it is a valid payload.
- **D3 - the secret is admin only.** `GET /api/admin/recaptcha` returns the pair
  (the card has to show what is stored so a rotation is possible); the public
  payloads (`GET /api/settings`, `GET /api/auth/session`) keep returning only
  `recaptcha_enabled` and `recaptcha_client_id`, so `LoginView.vue`,
  `StatusPagePublicView.vue` and `Recaptcha.vue` need no change at all.
- **D4 - a dedicated endpoint, not `settingsWhitelist`.**
  `GET`/`PUT /api/admin/recaptcha` mirrors the `lighthouse` card pattern, and the
  pair is deliberately kept **out** of the federated settings whitelist: like the
  cluster key and the session secret it is a per-node security value, and a
  cluster should not silently copy one node's captcha credentials to the others.
- **D5 - no migration, no seed.** `settings` is a key/value table: the rows are
  created by the first successful save. `database.Seed` gets **no** entry on
  purpose - importing the environment into the table at boot would defeat the
  "configure it in the UI" decision and resurrect exactly what D1 removes.
- **D6 - read on every request.** `verifyRecaptcha` reads
  `h.Settings.Recaptcha().ClientSecret` per call (the value comes from the cached
  settings map), so a rotation applies to the next attempt. It fails closed if the
  secret disappeared between the gate and the call instead of posting an empty
  secret to Google.
- **D7 - the boot warning is the migration note.** `warnDeprecatedRecaptchaEnv`
  (`cmd/server/main.go`) logs one `WARN` per leftover variable. The process does
  not fail: an ignored variable must not keep an upgraded instance down.

## 3. Changes

Backend:

| File | Change |
|---|---|
| `internal/models/security.go` | the two setting keys, `RecaptchaSettings` with `Normalize`/`Validate`/`Enabled` |
| `internal/services/settings.go` | `Recaptcha()`, `RecaptchaEnabled()`, `SetRecaptcha()` (normalizes and re-validates before writing) |
| `internal/handlers/recaptcha.go` | **new** `adminRecaptcha`, `updateAdminRecaptcha`, `recaptchaPayload` |
| `internal/handlers/router_admin.go` | `GET`/`PUT /api/admin/recaptcha` |
| `internal/handlers/auth.go` | session payload, login gate and `verifyRecaptcha` read the settings |
| `internal/handlers/public.go` | `/api/settings` reads the settings |
| `internal/handlers/statuspage_subscriptions.go` | subscribe gate reads the settings |
| `internal/config/config.go`, `validate.go` | `RecaptchaClientID`, `RecaptchaClientSecret` and `RecaptchaEnabled` deleted |
| `cmd/server/main.go` | `warnDeprecatedRecaptchaEnv` |
| `internal/models/security_test.go` | **new** enablement, validation and normalization tables |
| `internal/handlers/recaptcha_test.go` | **new** route registration, the fresh-install payload and the `400 ERR_VALIDATION` of a half pair |

Frontend:

| File | Change |
|---|---|
| `web/src/lib/types-platform.ts` | `RecaptchaSettings` |
| `web/src/lib/api-admin.ts` | `recaptchaSettings()`, `updateRecaptchaSettings()` |
| `web/src/views/admin/AdminSettingsView.vue` | "Login protection (reCAPTCHA)" card: site key, secret (`type="password"`), active/inactive alert and its own Save (which re-bootstraps the SPA so the login form follows) |
| `web/src/locales/*.json` (7) | the eight `settings.recaptcha*` keys |

Docs and environment: `docker/.env.example` (the block becomes a pointer to the
settings page), `docs/authentication.md`, `architecture.md`, `security.md`,
`status-pages.md`, `api.md` (the two endpoints), `database.md` (the two rows).

## 4. Verification

1. `gofmt -l internal/ cmd/` (only the pre-existing `internal/database/rollup.go`
   shows up), `go build ./...`, `go test ./internal/...`.
2. `cd web && npm run typecheck && npm run check:i18n && npm run build`.
3. `grep -rn 'RECAPTCHA_CLIENT' --include='*.go' --include='*.md' .`: only the
   deprecation notes (model comment, boot warning, docs, `.env.example`) match.
4. Manual, fresh database: the card is empty, the alert says the captcha is off,
   no widget on the login form. Save the Google test pair
   (`6LeIxAcTAAAAAJcZVRqyHh71UMIEGNQ_MXjiZKhI` /
   `6LeIxAcTAAAAAGG-vFI1TnRWxMZNFuojJ4WifJWe`): the widget appears on the next
   visit without a restart, and `GET /api/settings` still carries no secret. Save
   only one key -> `400 ERR_VALIDATION`. Clear both -> the widget disappears.

## 5. Out of scope

- A per-node or per-page captcha policy (the pair is instance wide).
- Migrating the environment values into the table at boot (D5).
- Replacing reCAPTCHA with another provider.
