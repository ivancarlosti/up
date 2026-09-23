# Up - Authentication

> `AUTH_METHOD` selects one of three modes. There are no users inside Up: the
> identity comes from the environment (single account) or from an OIDC provider,
> and the session is a signed cookie. The public status pages and the public API
> are separate surfaces (see `status-pages.md` and `public-api.md`).

## 1. Choosing a mode

| `AUTH_METHOD` | Dashboard | Login UI | Requirements |
|---|---|---|---|
| `none` | fully open, every request is the anonymous admin | none | - |
| `account` | e-mail + password from the environment | form (+ optional captcha) | `ACCOUNT_LOGIN`, `ACCOUNT_PASSWORD` |
| `keycloak` | OpenID Connect Authorization Code + PKCE | "Sign in with Keycloak" | `KEYCLOAK_*` |

Validation is strict: an unknown method, a missing account or an incomplete
Keycloak configuration aborts the boot with a readable list of problems
(`internal/config/validate.go`).

## 2. Session model (all modes except `none`)

```mermaid
sequenceDiagram
  participant B as Browser
  participant A as Up API
  participant S as settings table

  B->>A: POST /api/auth/login (account) or GET /api/auth/oidc/login (keycloak)
  A->>S: read session_secret (created on the first boot)
  A-->>B: Set-Cookie up_session=<signed payload>; HttpOnly; SameSite=Lax; Secure when APP_URL is https
  B->>A: GET /api/monitors (cookie)
  A->>A: verify HMAC, check exp, build the identity
```

- Payload: `{sub, email, method, iat, exp}` base64url encoded and signed with
  HMAC-SHA256 (`utils.SignedValue`).
- **No session table**: the cookie is stateless, and because the secret lives in
  the shared `settings` table every cluster node accepts the same cookie.
- Rotating `session_secret` (delete the row and restart) invalidates **all**
  sessions at once.
- Lifetime: `SESSION_TTL_HOURS` (default 720 h = 30 days).
- CSRF: the cookie is `SameSite=Lax` and the API is JSON only, so the classic
  cross-site form POST cannot reach a mutating endpoint. Cross-origin requests
  are additionally rejected by the CORS middleware unless the `Origin` equals
  `APP_URL`.

## 3. Mode `none`

Nothing to configure; useful for a fully trusted internal network or a
read-only mirror. `GET /api/auth/session` reports
`{"authenticated":true,"identity":{"email":"anonymous"}}` and the SPA skips the
login screen.

## 4. Mode `account`

`.env`:

```env
AUTH_METHOD=account
ACCOUNT_LOGIN=admin@example.com
ACCOUNT_PASSWORD=admin123
RECAPTCHA_CLIENTID=
RECAPTCHA_CLIENTSECRET=
```

```bash
curl -c cookies.txt -X POST http://localhost:3000/api/auth/login \
  -H 'Content-Type: application/json' \
  -d '{"email":"admin@example.com","password":"admin123"}'
# -> {"authenticated":true,"identity":{"email":"admin@example.com",...}}

curl -b cookies.txt http://localhost:3000/api/auth/session
curl -b cookies.txt -X POST http://localhost:3000/api/auth/logout
```

Details:

- The e-mail comparison is case-insensitive; the password comparison is
  constant time (`utils.SecureCompare`) to avoid timing leaks.
- A wrong pair answers `401 ERR_AUTH_INVALID_CREDENTIALS` and is logged with the
  client IP (`failed login attempt`) plus the rate limiter accounting.
- `SECURITY_LOGIN_RATE_LIMIT` (default 20/min per IP) protects the endpoint.
- Errors are always the same shape, they never reveal which field was wrong.

### Optional Google reCAPTCHA

When **both** `RECAPTCHA_CLIENTID` and `RECAPTCHA_CLIENTSECRET` are set, the SPA
renders the widget and the login request must carry `recaptcha_token`:

- missing token -> `403 ERR_RECAPTCHA_MISSING`
- failed validation or score < 0.5 -> `403 ERR_RECAPTCHA_FAILED`

For manual tests Google publishes always-pass test keys (site key
`6LeIxAcTAAAAAJcZVRqyHh71UMIEGNQ_MXjiZKhI`, secret
`6LeIxAcTAAAAAGG-vFI1TnRWxMZNFuojJ4WifJWe`).

## 5. Mode `keycloak` (OIDC)

```env
AUTH_METHOD=keycloak
KEYCLOAK_BASE_URL=https://sso.example.com
KEYCLOAK_REALM=up-realm
KEYCLOAK_CLIENT_ID=up-client
KEYCLOAK_CLIENT_SECRET=YOURSECRET
KEYCLOAK_REDIRECT_URI=
KEYCLOAK_POST_LOGOUT_REDIRECT_URI=
KEYCLOAK_ACCOUNTS=admin@example.com @empresa.com domain2.com
APP_URL=https://up.example.com
```

- Discovery: `KEYCLOAK_BASE_URL` + `/realms/` + `KEYCLOAK_REALM` +
  `/.well-known/openid-configuration` (`coreos/go-oidc`).
- `KEYCLOAK_REDIRECT_URI` may be left empty: it is derived as
  `APP_URL + /api/auth/callback`. Register **exactly** that URL on the client.
- `KEYCLOAK_POST_LOGOUT_REDIRECT_URI` may be left empty too: it is derived as
  `APP_URL + /login` and is where the provider returns the browser after a
  logout. Register **exactly** that URL on the client as well (see
  "Signing out and switching accounts").
- The token exchange uses **PKCE (S256)** with a signed, single use state cookie
  (`up_oidc_state`, 10 minutes) that also carries the nonce.
- The ID token is verified against the provider JWKS, including the nonce.
- The client must be a **confidential** client (client secret) with the
  Authorization Code flow enabled.
- Scopes requested: `openid profile email`.

### The `KEYCLOAK_ACCOUNTS` allow list

After a successful token validation the e-mail claim is checked against
`KEYCLOAK_ACCOUNTS`, which makes it safe to reuse a public identity provider
(Google, Azure AD, Keycloak with social login) while keeping the dashboard
private:

| Entry form | Matches |
|---|---|
| `you@example.com` | that exact address (case-insensitive) |
| `@empresa.com` | any address of that domain |
| `empresa.com` | same as above (`@` is optional) |
| `domain2.com` | additional domains can be mixed with exact addresses |

Entries are separated by spaces (comma/tabs also work). Sub-domains match as
well: `empresa.com` also allows `user@mail.empresa.com`.

The e-mail is taken from `email`, falling back to `preferred_username` and then
`upn`. When the address is not allowed the callback answers
`403 ERR_AUTH_DOMAIN_NOT_ALLOWED` with a small HTML page (the browser is
involved) and logs the rejected address with the client IP.

### Signing out and switching accounts

Up's session cookie is independent from the provider session: after a login the
browser keeps the Keycloak SSO cookie, so starting the login again silently signs
the **same** account back in. A rejected address would therefore loop forever on
`ERR_AUTH_DOMAIN_NOT_ALLOWED` ("Try again" alone can never succeed), and a plain
`POST /api/auth/logout` would not help either: it only clears `up_session`.

Two endpoints cover that:

| Action | Endpoint | What it does |
|---|---|---|
| Sign in as somebody else | `GET /api/auth/oidc/login?prompt=login` | `prompt=login` makes the provider show its form again instead of reusing the SSO session. No provider side configuration is needed. |
| Sign out | `GET /api/auth/oidc/logout?redirect=/login` | clears `up_session`, the OIDC state and the ID token cookies, then redirects to the provider `end_session_endpoint` (RP-initiated logout) so the provider session is dropped as well |

The error page shown after a rejected login offers both actions, and the dashboard
"Sign out" uses `/api/auth/oidc/logout` whenever `AUTH_METHOD=keycloak`.

Details:

- `KEYCLOAK_POST_LOGOUT_REDIRECT_URI` (default `APP_URL + /login`) is sent as the
  `post_logout_redirect_uri` of that redirect and **must be registered on the
  client**: *Clients → your client → Login settings → Valid post logout redirect
  URIs*. Keycloak answers `400 INVALID_REDIRECT_URI` otherwise.
- The callback stores the raw ID token in a 15 minute `HttpOnly` cookie so the
  logout can pass it back as `id_token_hint`. Without that hint Keycloak cannot
  tell which session belongs to this client and displays its own logout
  confirmation page first (the logout still works, one click later).
- The `redirect` query parameter is only used when the provider advertises no
  `end_session_endpoint`: the browser is then sent to `APP_URL + redirect`.
- Both endpoints are public (they only ever destroy state); a `GET` logout is
  what the RP-Initiated Logout specification describes.

### Testing the flow locally

```bash
# a throwaway Keycloak with the admin/admin credentials
docker run -d --name up-dev-keycloak --network up-test -p 8080:8080 \
  -e KC_BOOTSTRAP_ADMIN_USERNAME=admin -e KC_BOOTSTRAP_ADMIN_PASSWORD=admin \
  quay.io/keycloak/keycloak:26.7.4 start-dev
```

Checklist on the Keycloak side:

1. Realm `up-realm`, client `up-client` (confidential).
2. Valid redirect URI: `https://up.example.com/api/auth/callback` (or
   `http://localhost:3000/api/auth/callback` for a local test).
3. Valid post logout redirect URI: `https://up.example.com/login` (or
   `http://localhost:3000/login`) — needed by
   `GET /api/auth/oidc/logout`. Use the same value as
   `KEYCLOAK_POST_LOGOUT_REDIRECT_URI` when it is set explicitly.
4. A user with a real e-mail belonging to `KEYCLOAK_ACCOUNTS`.
5. A user **outside** the allow list, to exercise the rejection page: sign in
   with it, then use "Sign in with another account" (or "Sign out") and check
   that the allowed account can be entered afterwards.
6. `KEYCLOAK_BASE_URL` must be reachable **from the Up container**: use
   `http://up-dev-keycloak:8080` for a container on the same Docker network, or
   the public URL. If discovery works but the callback fails, check the
   redirect URI character by character.

## 6. Authorization boundaries

| Surface | Middleware | Notes |
|---|---|---|
| `/api/auth/*`, `/api/health`, `/api/version`, `/api/settings` | none (rate limited where needed) | public bootstrap endpoints |
| `/api/public/*` | IP rules (`public` scope) + rate limit | status pages and badges |
| `/api/v1/*` | bearer token (`read`/`write` scope) + IP rules (`api` scope) | public API |
| `/api/cluster/join` | cluster private key (node flavour) or session (UI flavour) | see `clustering.md` |
| every other `/api/*` | session cookie + IP rules (`dashboard` scope) | admin API |
| `/api/ws` | session cookie | live updates |
| SPA routes | none (the API decides) | the shell is public, the data is not |
