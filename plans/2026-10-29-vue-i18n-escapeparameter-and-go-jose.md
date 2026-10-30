# Plan - vue-i18n `escapeParameter` advisories and go-jose JWK DoS

> Task: close the two advisories reported against `vue-i18n` 11.4.12 (and its
> pinned `@intlify/*` 11.4.12) and merge the open dependabot bump of
> `github.com/go-jose/go-jose/v4` 4.1.5 -> 4.1.6, which fixes a different
> advisory that the 2026-10-24 round did not cover.

## 1. Why

Re-running `npm audit` on 2026-10-29 (after the 2026-10-24 patch round) reported
five HIGH entries - one per package, all propagation of two root advisories:

- `GHSA-83rm-6c8r-g728` / `CVE-2026-118257` (HIGH, CVSS 7.2
  `AV:N/AC:L/PR:N/UI:N/S:C/C:L/I:L/A:N`), affected `>=11.1.10 <11.4.14`.
  `sanitizeTranslatedHtml()` escapes `<`/`>` inside quoted attribute values and
  rewrites `on*` to `&#111;n`, but it does not handle the `srcdoc` attribute of
  `<iframe>`. `srcdoc` holds a whole HTML document, so the browser decodes those
  character references and parses the value again in the same origin: the
  escaping is undone. This is a bypass of the fix for `CVE-2025-53892`.
- `GHSA-8rc2-428q-7ww3` / `CVE-2026-118256` (LOW, CWE-79/116), affected
  `>=9.0.0-beta.12 <11.4.14`. `escapeParams()` escaped either the list values or
  the named values (`if (list) ... else if (named)`), so a call that passes both
  (`t(key, ['a'], { named: { ... } })`) left the named values unescaped.

Both are fixed in **11.4.14**; `11.4.15` and `11.4.16` are later patches of the
same line (`11.4.16` is the current `latest`, published 2026-10-18).

Separately, dependabot PR #7 proposed `go-jose/v4` 4.1.5 -> 4.1.6 for
`GHSA-mgcc-cww5-fp2q` / `CVE-2026-116587` (HIGH): parsing a JWK that carries a
crafted RSA key is cubic in the modulus/prime size on Go 1.24+, because the
values reach `crypto/rsa.PrivateKey.Validate()` unbounded. Up compiles go-jose
through `github.com/coreos/go-oidc/v3`, which parses the issuer JWKS when the
OIDC login flow verifies an ID token (`internal/handlers/oidc.go`,
`internal/handlers/oidc_callback.go`).

### Exposure of this application

- `web/src/i18n.ts` creates the i18n instance **without `escapeParameter`**, so
  the escaping/sanitizing path the advisories live in is not enabled.
- No component renders a translation as HTML: `v-html`, `innerHTML` and
  `<i18n-t>` do not appear anywhere in `web/src` (the translated string is always
  a text node, which Vue escapes).
- The locale bundles are static files imported at build time
  (`web/src/locales/*.json`); only the *selection* is dynamic (`up.locale` in
  `localStorage`, `settings.default_locale`), and it picks one of those bundles.
- Unlike `vite`/`source-map-js` (dev-only), `vue-i18n` **is** bundled into the
  embedded SPA, so the vulnerable code ships even though the vulnerable feature
  is off.

Net: not exploitable as configured, but the artifact is flagged by scanners and
the "0 advisories" row of [security.md](../docs/security.md) §10 was stale.

## 2. Changes

| file | change |
|---|---|
| `web/package.json` | `vue-i18n` `^11.4.12` -> `^11.4.16` |
| `web/package-lock.json` | `vue-i18n` and `@intlify/{core-base,devtools-types,message-compiler,shared}` 11.4.12 -> 11.4.16 (5 packages, 26 lines) |
| `web/dist/index.html` | rebuilt by `npm run build` (`i18n-*.js` and `index-*.js` hashes changed) |
| `go.mod` | `github.com/go-jose/go-jose/v4` v4.1.5 -> v4.1.6 (from PR #7, fast-forward) |
| `go.sum` | the two `go-jose` lines replaced |
| `docs/security.md` | §10 dated for the 2026-10-29 round: new remediation table, refreshed frontend row (0 advisories, `vue-i18n` is shipped code, `escapeParameter` off, no `v-html`), informational row now lists the two new `x/net` advisories |
| `docs/i18n.md` | §3 "Translations are text, never HTML" note: translations are text only, so `escapeParameter` stays off; enabling HTML rendering would require sanitizing at the boundary |
| `plans/2026-10-29-vue-i18n-escapeparameter-and-go-jose.md` | this plan and its verification log |

## 3. Decisions

- **11.4.16, not 11.4.14.** 11.4.14 is the first patched version; 11.4.16 is the
  latest patch of the line. This follows the precedent of the previous round
  (`vite` 8.3.5 instead of the minimum fixed 8.3.3). v12 is alpha-only and out of
  scope.
- **Raise the manifest floor to `^11.4.16`.** The old `^11.4.12` range already
  admitted 11.4.16, but only the lock pinned 11.4.12; the floor documents the
  security requirement (same reasoning as `vite ^8.3.0` -> `^8.3.5`).
- **No `overrides` entry.** `vue-i18n` pins its `@intlify/*` dependencies to the
  exact same version, so bumping the parent moves all five packages.
- **go-jose came from PR #7, merged fast-forward.** Its merge base was already
  the tip of `main`, so `git merge --ff-only` reproduces the dependabot pull
  request without a merge commit and without hand-editing `go.sum`. The npm
  dependabot PR #3 (`vite` 8.3.4 + `source-map-js`) was already superseded by
  `bd8cba5` (which took `vite` to `^8.3.5`); merging it would have *lowered* the
  floor, which is why dependabot closed it as "no longer updatable".
- **go-jose risk is low but the bump is free.** The JWKS comes from the issuer
  the administrator configured (`OIDC_ISSUER`), not from an arbitrary client, so
  exploitation needs a malicious/compromised IdP or a TLS-stripped MITM. Note the
  behaviour change in 4.1.6: RSA moduli above 4096 bits are rejected
  (`jose.MaxRSAModulusBits`); Keycloak/Entra/Google publish 2048-4096-bit keys.
- **`npm run check:i18n` keeps its teeth.** 11.4.15 changed the handling of
  messages that cannot be compiled, but only for builds *without* a message
  compiler; `getCompileContext()` still throws `SyntaxError` from `onError`, so
  the check (which runs in Node with the compiler) still fails on a broken
  message.
- **The two new `x/net` advisories are left for the next round.** `govulncheck`
  reports `GO-2026-6866` (`CVE-2026-97045`, raw-text rendering) and
  `GO-2026-6860` (`CVE-2026-78661`, quadratic text coalescing), both in
  `golang.org/x/net/html`, which this repository never imports (only
  `x/net/publicsuffix` is used, in `internal/expiry/targets.go`), and both are
  fixed only in `x/net` v0.62.0 - which requires `x/crypto` v0.58.0, `x/sys`
  v0.49.0, `x/term` v0.47.0 and `x/text` v0.43.0, the same four-module cascade
  the previous round avoided. They are unreachable, so they stay informational
  and are recorded in `docs/security.md`.
- **One `fix(deps):` commit.** The angular preset in
  `.github/workflows/semantic-release.yml` only releases on
  `feat`/`fix`/`perf`/`BREAKING`, so this cuts a patch release and dispatches
  `build.yml`; dependabot's own `build(deps):` message would not have produced an
  image.

## 4. Verification

| check | result |
|---|---|
| `npm ci` | `added 105 packages, and audited 106 packages`, **0 vulnerabilities** |
| `npm ls vue-i18n @intlify/core-base @intlify/message-compiler @intlify/shared @intlify/devtools-types` | all **11.4.16** |
| `npm audit` | **0 vulnerabilities** |
| `npm run typecheck` | clean (no output) |
| `npm run check:i18n` | `ok 6706 messages compiled`, 52 error codes and 808 used keys checked in all 7 locales |
| `npm run build` | `built in 1.30s`; `dist/index.html` regenerated (`i18n-COJkQvs1.js`, `index-DDm7ZSqe.js`) |
| `npm run smoke` | `ok smoke test passed` - `#app` rendered, 0 console errors |
| `go build ./...` | clean (downloaded `go-jose` v4.1.6) |
| `go vet ./...` | clean |
| `go test ./...` | every package `ok` (or `no test files`) |
| `gofmt -l .` | only the pre-existing `cmd/server/main.go` and `internal/database/rollup.go`; no Go source was touched |
| `govulncheck ./...` (go1.27.3) | **0 reachable vulnerabilities**; 3 in required-but-not-called modules (`GO-2026-5932`, `GO-2026-6866`, `GO-2026-6860`) |
| `govulncheck -show verbose ./...` | the go-jose advisory is gone from the module set; the remaining three are the `openpgp` (no fix) and the two `x/net/html` entries above |
| Trivy | not re-run in this round (no image was rebuilt; a full scan was done on 2026-10-24) |
