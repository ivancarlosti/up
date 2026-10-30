# Plan - reCAPTCHA v2 widget -> reCAPTCHA v3 (score based)

> Task: the login form and the public subscription form stop rendering Google's
> v2 checkbox and start minting **v3** tokens, with the minimum score editable in
> Admin > Settings. Supersedes the client half of
> [2026-10-30-recaptcha-settings-page.md](2026-10-30-recaptcha-settings-page.md).

## 1. Goal

The captcha had stopped working for anybody who followed current advice: the
reCAPTCHA console hands out **v3** key pairs by default, and a v3 pair rendered
through `grecaptcha.render()` answers

```
ERROR for site owner: Invalid key type
```

with no widget and therefore no token, so the login always ended in
`403 ERR_RECAPTCHA_MISSING`. `web/src/components/ui/Recaptcha.vue` was pure v2:
`api.js?render=explicit` plus `grecaptcha.render()`, which is exactly the API v3
refuses. The backend was almost ready (it already decoded `score` and `action`),
so the fix is a client rewrite plus the missing threshold knob.

v3 is also a better fit for this dashboard: no third-party checkbox in the middle
of the form, no "confirm the captcha" step that can be forgotten, and a policy
that is a number an operator can tune instead of a widget Google might disable.

## 2. Decisions

- **D1 - v3 only, no fallback.** The component speaks v3 and nothing else. A
  silent v2 branch would double the client code and hide the real error (a v2 key
  pair pasted in the card) instead of surfacing it as a rejected login plus a
  `captcha rejected` log line. The card now says "v3 (score based) keys".
- **D2 - the token is minted at submit.** `grecaptcha.execute()` is called from
  `submit()`/`subscribe()`, never on mount: a v3 token is single use and expires
  after two minutes, so one minted while the page loaded would be dead by the
  time a human finished typing.
- **D3 - one action per form.** `login` (`models.RecaptchaActionLogin`) and
  `status_page_subscribe` (`models.RecaptchaActionStatusSubscribe`). The server
  compares the action carried inside the token with the action of the endpoint,
  so a token harvested from the public page cannot open the dashboard. The action
  is a constant in each view, not user input.
- **D4 - the threshold is a setting.** `recaptcha_min_score`
  (`models.SettingRecaptchaMinScore`), `0.1`-`0.9`, default
  `models.DefaultRecaptchaMinScore` (`0.5`, Google's recommendation). Absent or
  `0` normalizes to the default (an older client, or a fresh install, cannot lock
  the login), out of band is refused with `400 ERR_VALIDATION`, and a hand edited
  row outside the band is read as the default. Band rather than free number
  because `0.05` accepts everything and `0.95` rejects ordinary humans: both are
  support tickets, not policies.
- **D5 - the component renders nothing.** v3 has no widget; Google injects its
  own badge in the corner, which is the visible proof that the script loaded. The
  `<span class="sr-only" aria-hidden="true">` in the template only keeps a
  mount point, and `login.captchaHint` ("Confirm the captcha to continue") is
  gone: there is nothing to confirm.
- **D6 - the public payloads are unchanged.** `GET /api/settings` and
  `GET /api/auth/session` keep `recaptcha_enabled` + `recaptcha_client_id`; the
  threshold is server side policy and is edited only through
  `GET`/`PUT /api/admin/recaptcha`, which gains `min_score` in its payload.
- **D7 - the script URL carries the key.** `api.js?render=<site key>`, not
  `?render=explicit`: the API binds to that key, so rotating the pair in the card
  removes the old tag and loads the new one (`loaded` remembers which key the
  tag belongs to).

Operationally nothing new is needed to switch the captcha off: clearing both key
rows still does it, and the threshold is ignored while the pair is empty.

## 3. Changes

Backend:

| File | Change |
|---|---|
| `internal/models/security.go` | `SettingRecaptchaMinScore`, `Default/Min/MaxRecaptchaMinScore`, `RecaptchaActionLogin`/`RecaptchaActionStatusSubscribe`, `RecaptchaSettings.MinScore`, `Normalize` (0 -> 0.5) and `Validate` (band, empty pair always valid) |
| `internal/services/settings.go` | `Recaptcha()` parses the third row (missing/out of band -> default); `SetRecaptcha()` writes it with two decimals |
| `internal/handlers/auth.go` | `verifyRecaptcha(ctx, token, expectedAction, clientIP)` reads secret + threshold per call, fails closed; pure `recaptchaVerdict(...)` returns the rejection reason (`""` = accept); debug/warn log with `action`, `score`, `minimum`; the login passes `RecaptchaActionLogin` |
| `internal/handlers/statuspage_subscriptions.go` | subscribe passes `RecaptchaActionStatusSubscribe` |
| `internal/handlers/recaptcha.go` | `min_score` in both payloads, v3 wording |
| `internal/models/security_test.go` | threshold defaults/band/rejections, action constants |
| `internal/handlers/recaptcha_test.go` | verdict table, `min_score` in the fresh-install payload, `400 ERR_VALIDATION` for an out of band score |

Frontend:

| File | Change |
|---|---|
| `web/env.d.ts` | `UpRecaptcha` becomes `{ ready(cb), execute(siteKey, {action}) }`; `render`/`reset` removed |
| `web/src/components/ui/Recaptcha.vue` | v3 loader (`?render=<sitekey>`, reload on key rotation) + `defineExpose({ execute })`; props `{ sitekey, action }`; no widget |
| `web/src/views/LoginView.vue` | no `v-model`, no `reset()`: `submit()` awaits `execute()`, shows `common.captchaUnavailable` on failure, sends the token; component stays mounted with `action="login"` |
| `web/src/views/StatusPagePublicView.vue` | same pattern with `action="status_page_subscribe"` |
| `web/src/lib/types-platform.ts`, `web/src/lib/api-admin.ts` | `min_score` documented and required by `updateRecaptchaSettings()` |
| `web/src/views/admin/AdminSettingsView.vue` | "Minimum score" field (`min="0.1" max="0.9" step="0.1"`), saved with the pair |
| `web/src/locales/*.json` (7) | `settings.recaptchaMinScore`, `settings.recaptchaMinScoreHelp`, `common.captchaUnavailable`; reworded `settings.recaptchaCardHelp` and `errors.ERR_RECAPTCHA_MISSING`; `login.captchaHint`, `login.captchaUnavailable` and `publicStatus.subscribeCaptchaRequired` deleted |

Docs: `authentication.md` (v3 flow, actions, 2 min single use, threshold, badge,
the v2 test keys no longer apply), `status-pages.md`, `security.md`, `api.md`
(`min_score`, action check, error semantics), `database.md` (third row),
`docker/.env.example`.

## 4. Verification

1. `gofmt -l internal/ cmd/` (only the pre-existing `internal/database/rollup.go`),
   `go build ./...`, `go test ./internal/...`.
2. `cd web && npm run typecheck && npm run check:i18n && npm run build`
   (`web/dist` is tracked).
3. `grep -rn 'grecaptcha.render\|render=explicit' web/src` -> no hits.
4. Manual with a real v3 key pair: no widget on the login page, Google's badge in
   the corner, the login succeeds; a replayed token -> `403 ERR_RECAPTCHA_FAILED`;
   a token minted for `status_page_subscribe` sent to the login endpoint -> `403`
   with `the endpoint expects login`; `min_score` outside `0.1`-`0.9` -> `400
   ERR_VALIDATION`; the log shows `captcha accepted` with the score.

## 5. Out of scope

- A v2 mode, or auto-detecting the key type.
- Per-form thresholds (one instance wide value).
- Rendering the v3 badge ourselves (`grecaptcha` injects it; hiding it is blocked
  by Google's terms).
- Migrating stored keys: an operator with a v2 pair has to create a v3 pair.
