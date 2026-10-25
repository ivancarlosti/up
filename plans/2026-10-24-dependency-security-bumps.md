# Plan - Dependency security patch round

> Task: close the advisories reported against `vite` 8.3.0, `source-map-js`
> 1.2.1 and `golang.org/x/net` 0.59.0, and clear the standard-library findings
> of the `go1.27.1` build toolchain that produces the shipped binary.

## 1. Why

The 2026-09-22 audit in [security.md](../docs/security.md) §10 predated this
round. Re-running the official databases on 2026-10-24 surfaced:

- `npm audit` (frontend): `vite` 8.3.0-8.3.2 carries six advisories
  (launch-editor command construction on Windows, dev-server file exposure and
  custom-middleware script execution) and the transitive `source-map-js` 1.2.1
  is affected by `GHSA-68fv-2mgg-jv7q` (HIGH, event-loop DoS). Both are
  **dev-only** and never embedded in the binary.
- `govulncheck` (backend, with `x/net` 0.59.0): five reachable HTTP/2
  advisories, all fixed in `v0.60.0` - `GO-2026-6603`, `GO-2026-6610`,
  `GO-2026-6611`, `GO-2026-6612`, `GO-2026-6617`. `x/net` **is** compiled in
  (`internal/expiry/targets.go` uses `publicsuffix`, and the HTTP/2 code is
  reachable through the monitoring clients and the gin server).
- `govulncheck` (standard library, `go1.27.1`): 11 advisories; 10 are fixed in
  `go1.27.2` and `GO-2026-6809` (crypto/x509 intermediate certificates) in
  `go1.27.3`. `Dockerfile` pinned `golang:1.27.1-alpine3.24`, so they were
  compiled into the release binary.

## 2. Changes

| file | change |
|---|---|
| `web/package.json` | `vite` `^8.3.0` -> `^8.3.5` |
| `web/package-lock.json` | `vite` 8.3.0 -> 8.3.5, `source-map-js` 1.2.1 -> 1.2.2, plus Vite's own transitive patch set (`postcss` 8.5.28 -> 8.5.31, `rolldown` 1.2.9 -> 1.2.15) |
| `web/dist/index.html` | rebuilt by `npm run build` (new hashed asset names, the tracked placeholder convention) |
| `go.mod` | `golang.org/x/net` v0.59.0 -> v0.60.0; new `toolchain go1.27.3` line (`go 1.27` stays for shoutrrr) |
| `go.sum` | the two `x/net` lines replaced; nothing else |
| `Dockerfile` | build stage `golang:1.27.1-alpine3.24` -> `golang:1.27.3-alpine3.24` |
| `docs/security.md` | §10 dated as a patch round, frontend package count corrected (136 -> 106), patch-round remediation table added, Trivy row marked as the last full image scan |
| `docs/development.md` | Go toolchain 1.27.1 -> 1.27.3 in the table and install block (new sha256) |
| `README.md` | quick start requires Go 1.27.3 |

## 3. Decisions

- **`vite` 8.3.5, not 8.3.3.** The advisories are fixed in 8.3.3; 8.3.5 is the
  current latest patch and stays inside the existing `^8` range. Its peers
  (`@vitejs/plugin-vue@6.0.9`, `@tailwindcss/vite@4.3.3`) accept Vite 8.
- **`source-map-js` stays transitive.** 1.2.2 satisfies every parent range
  (`^1.2.1` from postcss/vite, `^1.0.2` from intlify), so no `overrides` entry
  or direct devDependency was needed.
- **`x/net` 0.60.0, not 0.61.0.** 0.60.0 is the minimum fixed version and its
  `require` block matches the `x/crypto`, `x/sys`, `x/term` and `x/text`
  versions already in `go.mod`; 0.61.0 would force a four-module cascade.
- **Go 1.27.3 everywhere.** The toolchain line makes `GOTOOLCHAIN=auto`
  download and use the patched compiler/standard library locally, and the
  Docker build stage pins the same version. `golang:1.27.3-alpine3.24` was
  verified to exist on the registry before the change.
- **Trivy was re-run** against the freshly built image (`up:dep-check`): 0
  HIGH/CRITICAL/MEDIUM/LOW and only the informational `GO-2026-5932`
  (`x/crypto/openpgp`, not imported) as UNKNOWN.

## 4. Verification

| check | result |
|---|---|
| `npm ls vite source-map-js` | `vite@8.3.5`, `source-map-js@1.2.2` (deduped across postcss, `@tailwindcss/node`, the Vue compilers and `@intlify/message-compiler`) |
| `npm audit` | **0 vulnerabilities** |
| `npm run typecheck` | clean |
| `npm run build` | **✓ built in 2.88s**, `dist/index.html` regenerated |
| `npm ci` | `added 105 packages, and audited 106 packages`, **0 vulnerabilities** (proves the Docker stage-1 install still works from the lock) |
| `go version` | `go1.27.3 linux/amd64` (auto-downloaded from the `toolchain` line) |
| `go build ./...`, `go vet ./...` | both clean |
| `go test ./...` | every package `ok` (or `no test files`) |
| `gofmt -l .` | only the pre-existing `cmd/server/main.go` and `internal/database/rollup.go`; no Go source was touched in this round |
| `govulncheck -show verbose ./...` | **0 reachable vulnerabilities**; only the informational `GO-2026-5932` (`x/crypto/openpgp`, not imported) remains in the module set |
| `docker build -t up:dep-check .` | image built end to end: `npm ci` + `vite build` in stage 1, `go build` with `golang:1.27.3-alpine3.24` in stage 2 |
| `trivy image up:dep-check` | 0 HIGH/CRITICAL/MEDIUM/LOW; 1 UNKNOWN (`GO-2026-5932`, `x/crypto/openpgp`, not imported) |
