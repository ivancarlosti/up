# Up - Security

> Layered defence for an instance that is often reachable from the internet: IP
> allow/deny rules, rate limiting, hashed API tokens, signed sessions, optional
> captcha and a strict e-mail allow list for OIDC.

## 1. Surfaces and default exposure

| Surface | Default | Protection |
|---|---|---|
| Dashboard (`/` and `/api/*`) | protected as soon as `AUTH_METHOD != none` | session cookie + IP rules (`dashboard`) |
| Public status pages (`/status/:slug`, `/api/public/*`) | intentionally public | IP rules (`public`) + rate limit |
| Public REST API (`/api/v1/*`) | requires a token | bearer token + scope + IP rules (`api`) |
| Cluster join (node to node) | requires the shared key | `X-Cluster-Key` / `private_key` comparison |
| Static assets | public | - |

## 2. IP rules (Admin > Security)

Model: `ip_rules(id, cidr, action, scope, note, enabled)`.

| Field | Values |
|---|---|
| `cidr` | single address (`203.0.113.7`) or network (`203.0.113.0/24`, `2001:db8::/32`) |
| `action` | `allow` / `deny` |
| `scope` | `all`, `dashboard`, `api`, `public` |
| `enabled` | rule is evaluated or not |

Evaluation order (implemented in `internal/services/iprule_decision.go`):

```mermaid
flowchart TB
  A[request] --> B{SECURITY_BYPASS_IP_RULES?}
  B -->|true| OK[allow everything]
  B -->|false| C{any enabled deny rule matches?}
  C -->|yes| NO[403 ERR_IP_BLOCKED]
  C -->|no| D{does an allow rule exist for this scope?}
  D -->|no| OK2[allow - default is open]
  D -->|yes| E{the address matches an allow rule?}
  E -->|yes| OK3[allow]
  E -->|no| NO2[403 ERR_IP_BLOCKED allow list]
```

Key properties:

- **Open by default**: with no rule at all everything is allowed.
- A `deny` rule always wins over an `allow` rule.
- As soon as **one allow rule exists for a scope**, that scope becomes an
  **allow list**: everyone else is rejected. Use it to restrict the dashboard to
  your office/VPN while keeping status pages public.
- Rules are cached in memory for 10 s (write operations invalidate the cache).
- The address used is the one resolved by `APP_TRUST_PROXY` (see
  `docs/reverse-proxy.md`); getting that wrong is the classic source of a
  "blocked my own office" incident.

### Lockout protection

- The UI warns and shows your current `client_ip` in Admin > Security.
- Emergency switch: set `SECURITY_BYPASS_IP_RULES=true` and restart (all rules
  are ignored, an `Alert` is shown in the UI).
- Last resort: `DELETE FROM ip_rules;` in the database.

## 3. Rate limiting

| Endpoint | Limit variable | Default |
|---|---|---|
| `POST /api/auth/login` | `SECURITY_LOGIN_RATE_LIMIT` | 20 requests/minute per IP |
| `/api/public/*` and `/api/v1/*` | `SECURITY_PUBLIC_RATE_LIMIT` | 240 requests/minute per IP |

A token bucket per client address (in memory, no external dependency). Exceeding
the limit answers `429 ERR_RATE_LIMITED` with `Retry-After: 60`. Because the
buckets are per process, a multi node deployment behind a load balancer should
prefer the proxy's own rate limiting (Traefik/Nginx) for global limits.

## 4. Public API tokens

Format `up_<prefix>_<secret>`:

- `prefix` (8 hex chars) is stored in clear text and indexed for lookup;
- only the **SHA-256 hash** of the complete token is stored (`token_hash`);
- validation uses a constant time comparison and updates `last_used_at` /
  `last_used_ip`;
- scopes: `read` (status, heartbeats, statistics, on-demand analysis) and `write`
  (pause/resume);
- optional expiry (`expires_at`) and revocation (`revoked_at`);
- the plain value is returned **once**, in the creation response.

```bash
TOKEN=$(curl -s -b cookies.txt -X POST localhost:3000/api/tokens \
  -H 'Content-Type: application/json' \
  -d '{"name":"ci","scopes":["read"],"expires_in_days":90}' | jq -r .token)

curl -H "Authorization: Bearer $TOKEN" localhost:3000/api/v1/monitors
curl -X POST -H "Authorization: Bearer $TOKEN" localhost:3000/api/v1/monitors/6/pause
# -> 403 ERR_TOKEN_SCOPE_INSUFFICIENT for a read-only token
```

Misses/hits are logged; `ERR_TOKEN_REQUIRED`, `ERR_TOKEN_INVALID`,
`ERR_TOKEN_EXPIRED` and `ERR_TOKEN_SCOPE_INSUFFICIENT` are distinguishable on
purpose so a client can react correctly.

## 5. Sessions and cookies

| Property | Value |
|---|---|
| Name | `up_session` |
| Flags | `HttpOnly`, `SameSite=Lax`, `Path=/`, `Secure` when `APP_URL` is `https://` |
| Content | base64url JSON `{sub,email,method,iat,exp}` signed with HMAC-SHA256 |
| Secret | `settings.session_secret` (32 random bytes, generated on the first boot, shared by the cluster) |
| Lifetime | `SESSION_TTL_HOURS` (default 720) |
| Revocation | rotate `session_secret` (deletes all sessions) |

CSRF: the API is JSON-only and the cookie is `SameSite=Lax`, so a cross-site form
cannot mutate state; CORS additionally rejects an `Origin` different from
`APP_URL`.

## 6. Authentication hardening

- Constant time comparisons for the account password, the cluster key and the
  API tokens.
- OIDC with PKCE (S256), nonce and a single use signed state cookie.
- `KEYCLOAK_ACCOUNTS` allow list (exact addresses and/or domains) so a public
  identity provider cannot open the dashboard to the world.
- Optional reCAPTCHA on the login form.
- Rate limiting per IP on the login endpoint.
- Failed logins are logged with the client IP (`failed login attempt`); OIDC
  rejections are logged with the refused e-mail.

## 7. Transport and headers

Up always sends: `X-Content-Type-Options: nosniff`, `X-Frame-Options: SAMEORIGIN`,
`Referrer-Policy: strict-origin-when-cross-origin`,
`Permissions-Policy: geolocation=(), microphone=(), camera=()`.

Enable HTTPS at the proxy and consider adding `Strict-Transport-Security` there
(`docs/reverse-proxy.md`). When `APP_URL` is `https://`, the session cookie is
marked `Secure` automatically.

## 8. Secrets inventory

| Secret | Where | Rotation |
|---|---|---|
| `ACCOUNT_PASSWORD` | environment | change and restart |
| `KEYCLOAK_CLIENT_SECRET` | environment | rotate in the provider, restart |
| `RECAPTCHA_CLIENTSECRET` | environment | rotate in the console, restart |
| `CLUSTER_PRIVATE_KEY` | `settings.cluster_private_key` (or env) | Admin > Cluster > regenerate (nodes must rejoin) |
| `session_secret` | `settings.session_secret` | delete the row and restart (invalidates sessions); propagated between nodes only when `CLUSTER_SYNC_SESSION_SECRET=true` |
| API tokens | `api_tokens.token_hash` | revoke/delete in Admin > Security |
| notification channel credentials (SMTP password, webhook/Slack/Discord/Telegram tokens) | `notifications.config` (JSON) | edit the channel in Admin > Notifications |
| status page subscriber addresses | `status_page_subscribers.email` | delete the subscriber in Admin > Status pages; the address is retained after an unsubscribe on purpose |
| confirmation token hashes | `status_page_subscribers.confirm_token_hash` | single-use, 7 days; a new confirmation replaces the hash |
| unsubscribe tokens | derived from the cluster private key, only the SHA-256 is stored | rotating `CLUSTER_PRIVATE_KEY` invalidates every existing unsubscribe link |

The database therefore contains credentials (SMTP passwords, chat tokens, the
cluster key): protect the database accordingly and remember that the MySQL
connection is plaintext unless `DB_SSL=true`. In federated mode the
`status_page_subscribers` rows (e-mail addresses) also replicate to every peer, so
the peer transport carries PII as well as configuration; run it over TLS
(`APP_URL=https://`).

## 9. Peer API trust model

Since the peer API shipped ([clustering-modes.md](clustering-modes.md) §11) a node
can call another node over HTTP on `/api/cluster/sync/*`. Those routes are **not**
covered by the IP rules, the session or a bearer token: they are authenticated by
an HMAC-SHA256 signature over the request, computed with the cluster private key.

What an operator should know:

- **Every node is trusted with the whole configuration.** That is the same trust
  the shared database already implies today; the peer API does not widen it.
- **The cluster key is a signing key here, not a session secret.** It never
  travels on the peer routes (only the join endpoint still sends
  `X-Cluster-Key`, which is the bootstrap with nothing to sign yet). A leaked key
  is still a full compromise of the peer API, so it is inventoried below and
  rotatable in Admin > Cluster.
- **Requests expire.** A timestamp outside ±5 minutes (past or future) is refused
  and each nonce is accepted once, so a captured request cannot be replayed. The
  nonce cache is in memory; a restart forgets it, which is safe because every
  current peer endpoint is a read or an idempotent pull.
- **Redirects are never followed** and the peer URL is validated before anything
  is signed, so the key cannot be aimed at a host the operator did not name.
- **Run the peers over TLS.** `CLUSTER_INSECURE_SKIP_VERIFY` exists only for
  internal labs: without TLS the traffic is authentic but still readable.
- **Secrets only travel behind explicit switches.** The channel credentials
  (`CLUSTER_SYNC_NOTIFICATIONS`) and the session secret
  (`CLUSTER_SYNC_SESSION_SECRET`) are off by default. Enabling either makes the
  peer traffic secret-bearing, so it must run over TLS (decision **D4** in
  [clustering-modes.md](clustering-modes.md)). `CLUSTER_SYNC_SETTINGS` only
  carries the non-secret whitelist (app name, default locale, default theme);
  API tokens, IP rules and the per-node rate limits never synchronise.

## 10. Dependency posture (full audit on 2026-09-22, patch rounds on 2026-10-24 and 2026-10-29)

Vulnerability audit of the shipped artifact, performed with the official
databases (OSV/Google, `govulncheck` and Trivy), separating what is compiled
into the binary from test-only dependencies. The 2026-10-24 patch round re-ran
`govulncheck` with the patched `go1.27.3` toolchain, `npm audit`, and a Trivy
scan of a freshly built image. The 2026-10-29 round closed two `vue-i18n`
advisories and one `go-jose` advisory, re-ran `govulncheck` and `npm audit`
against the rebuilt `web/dist`, and repeated `check:i18n` and the smoke test.

| Check | Tool | Result |
|---|---|---|
| Go modules reachable from `cmd/server` | `govulncheck ./...` (go1.27.3) | **0 vulnerabilities** affecting the code; 0 in imported packages |
| Go modules required but not called | `govulncheck` / OSV | 3 informational: `GO-2026-5932` (`golang.org/x/crypto/openpgp`, unmaintained **by design**, `Fixed version: N/A`, package not imported - confirmed with `go mod why`), `GO-2026-6866` (`CVE-2026-97045`) and `GO-2026-6860` (`CVE-2026-78661`), both in `golang.org/x/net/html`, which nothing imports (only `x/net/publicsuffix` is used) and both fixed in `x/net` v0.62.0 |
| Frontend packages (`web/package-lock.json`, 106 packages) | OSV + `npm audit` (2026-10-29) | **0 advisories**; `vite` and `source-map-js` are dev-only and never embedded in the binary, while `vue-i18n` *is* shipped code and had to be patched (see below) |
| Container image (`alpine` packages + Go binary) | `trivy image` (2026-10-24) | **0** HIGH/CRITICAL; 0 MEDIUM; 1 UNKNOWN (the same `openpgp` advisory above, no fix exists) |

Remediations applied in the 2026-09-22 audit:

| Item | From | To | Reason |
|---|---|---|---|
| `github.com/quic-go/quic-go` | v0.59.0 | v0.63.0 | CVE-2026-40898 (QPACK trailer expansion, MODERATE) reachable through `gin`'s http3 support |
| `filippo.io/edwards25519` | v1.1.0 | v1.2.0 | CVE-2026-26958 (LOW), pulled in by the MySQL driver |
| `github.com/go-sql-driver/mysql` | v1.8.1 | v1.10.1 | driver on the hot path of every query |
| `github.com/nicholas-fedor/shoutrrr` | v0.21.0 | v0.21.1 | notification engine patch |
| `github.com/go-jose/go-jose/v4`, `go-playground/validator/v10`, `goccy/go-json`, `leodido/go-urn`, `ugorji/go/codec`, `mongo-driver/v2` … | - | latest patch | routine patch bumps |
| Base images | `alpine:3.22`, `golang:1.27-alpine`, `node:24-alpine` | `alpine:3.24`, `golang:1.27.1-alpine3.24`, `node:24-alpine3.24` | newer busybox/musl/openssl/wget patch levels |

Patch round applied on 2026-10-24:

| Item | From | To | Reason |
|---|---|---|---|
| `golang.org/x/net` | v0.59.0 | v0.60.0 | five reachable HTTP/2 advisories: `GO-2026-6603` (Trailer memory exhaustion), `GO-2026-6610` (malformed framing headers), `GO-2026-6611` (CPU from repeated window changes), `GO-2026-6612` (double flow-control refund), `GO-2026-6617` (HPACK encoder race) |
| `vite` (dev only) | 8.3.0 | 8.3.5 | six advisories fixed in 8.3.3 (`GHSA-698v-4r3x-3vcv`, `GHSA-r825-w8mm-5pxw`, `GHSA-587h-7wmj-h5pm`, `GHSA-9jrq-w75r-8gcw`, `GHSA-rq7h-c2jc-7f22`, `GHSA-vfpm-58rq-9qcg`); 8.3.5 is the latest patch |
| `source-map-js` (dev only, transitive) | 1.2.1 | 1.2.2 | `GHSA-68fv-2mgg-jv7q` (HIGH, event-loop DoS through indexed source-map section offsets) |
| Build toolchain / base image | `go1.27.1`, `golang:1.27.1-alpine3.24` | `go1.27.3`, `golang:1.27.3-alpine3.24` | 11 standard-library advisories reported by `govulncheck` against go1.27.1; 10 are fixed in 1.27.2 and `GO-2026-6809` in 1.27.3. `go.mod` now pins `toolchain go1.27.3` |

Patch round applied on 2026-10-29:

| Item | From | To | Reason |
|---|---|---|---|
| `vue-i18n` and its pinned `@intlify/{core-base,devtools-types,message-compiler,shared}` (shipped in the embedded SPA) | 11.4.12 | 11.4.16 | `GHSA-83rm-6c8r-g728` (`CVE-2026-118257`, HIGH 7.2): the `escapeParameter` sanitizer escapes `<`/`>` inside quoted attributes but does not handle `<iframe srcdoc>`, whose value the browser decodes and re-parses, undoing the escaping (bypass of `CVE-2025-53892`); `GHSA-8rc2-428q-7ww3` (`CVE-2026-118256`, LOW): named values are left unescaped when a call also passes a list. Both fixed in 11.4.14; 11.4.16 is the latest patch of the v11 line |
| `github.com/go-jose/go-jose/v4` (indirect, through `coreos/go-oidc`) | v4.1.5 | v4.1.6 | `GHSA-mgcc-cww5-fp2q` (`CVE-2026-116587`, HIGH): parsing a JWK that carries a crafted RSA key is cubic in the key size, and Up parses the issuer JWKS whenever the OIDC verifier checks an ID token (`internal/handlers/oidc.go`, `oidc_callback.go`). 4.1.6 also refuses RSA moduli above 4096 bits |

Neither `vue-i18n` advisory is exploitable in Up as configured, and they were
patched anyway because the library is compiled into `web/dist` and therefore into
the binary that scanners inspect. `createI18n()` in `web/src/i18n.ts` leaves
`escapeParameter` off, no component renders a translation as HTML (`v-html`,
`innerHTML` and `<i18n-t>` do not appear in `web/src`), and the locale bundles are
static files imported at build time, so a translated string can only ever be a
text node. Do not turn `escapeParameter` on, nor start rendering HTML from a
locale file, without sanitizing at that boundary first. The `go-jose` JWKS comes
from the issuer an administrator configured, so that advisory needed a malicious
or compromised IdP (or a TLS-stripped MITM) to be reachable at all.

`govulncheck` on 2026-10-29 also lists two `x/net` advisories that are not
reachable and are left for a later round, because clearing them means moving
`x/net` to v0.62.0 together with `x/crypto` v0.58.0, `x/sys` v0.49.0, `x/term`
v0.47.0 and `x/text` v0.43.0 - the same four-module cascade the 2026-10-24 round
avoided. Both live in `golang.org/x/net/html`, which this repository never
imports.

How to reproduce the audit:

```bash
go install golang.org/x/vuln/cmd/govulncheck@latest && govulncheck ./...
docker run --rm -v /var/run/docker.sock:/var/run/docker.sock \
  aquasec/trivy:latest image --severity HIGH,CRITICAL --ignore-unfixed \
  ghcr.io/ivancarlosti/up:latest
cd web && npm audit
```


The final image runs as the non-root user `up` (uid 1000) on Alpine, contains a
single statically linked binary and ships its own `HEALTHCHECK`. There is no
shell requirement and no package manager at runtime.
