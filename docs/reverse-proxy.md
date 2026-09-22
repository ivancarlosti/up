# Up - Reverse proxy

> Up is designed to run behind a reverse proxy. Two variables make that safe:
> `APP_URL` (the canonical public URL) and `APP_TRUST_PROXY` (whether the
> `X-Forwarded-*` headers may be trusted).

## 1. What each variable does

| Variable | Effect |
|---|---|
| `APP_URL` | Canonical public URL. Used for links inside notifications, the OIDC redirect URI (`APP_URL + /api/auth/callback`), the CORS allow list, the WebSocket origin check and the `nodes.api_url` recorded in a cluster. |
| `APP_PORT` | Port the application listens on **inside the container**. `docker/docker-compose.yml` also publishes this same port on the host (Compose interpolates it from `docker/.env`), so `APP_PORT=8080` means `http://host:8080`. |
| `HOST_PORT` | Optional, compose only: published host port when it must differ from `APP_PORT` (e.g. `APP_PORT=3000` + `HOST_PORT=80`). **The reverse proxy must point to `HOST_PORT`** (the published port), not to `APP_PORT`. |
| `APP_TRUST_PROXY=false` | `X-Forwarded-For`, `X-Forwarded-Proto`, `X-Real-IP` are **ignored**; the client address is `RemoteAddr`, which is the proxy. Correct when Up is exposed directly. |
| `APP_TRUST_PROXY=true` | The headers above are trusted. Correct only when Up cannot be reached without going through your proxy. |
| `APP_TRUST_PROXY=10.0.0.0/8,172.16.0.0/12` | Trusted proxy CIDR list: only requests coming from those networks may spoof the headers (recommended). |

Why it matters: the IP rules (`docs/security.md`) and the rate limiter use the
resolved client address. With `APP_TRUST_PROXY=true` on a directly exposed
instance anyone could bypass an IP rule by sending `X-Forwarded-For`.

```env
APP_URL=https://up.example.com
APP_TRUST_PROXY=true          # or a CIDR list
APP_PORT=3000
```

## 2. WebSocket

The dashboard uses `/api/ws`. The proxy must:

1. forward the `Upgrade`/`Connection` headers,
2. keep the connection idle for more than 60 s (Up pings every 25 s),
3. **not** buffer the response.

### 2.1 What the badge in the header means

The header shows the real state of the channel, and the same channel covers every
page (it is opened by the session, not by the dashboard):

| Badge | Meaning |
|---|---|
| `Live` | `/api/ws` is connected; heartbeats and status changes arrive in real time. |
| `Connecting…` | the first handshake is in flight (visible for a fraction of a second). |
| `Reconnecting…` | the connection dropped and a retry is already scheduled (transient failures only, with exponential backoff and jitter, up to 30 s). |
| `Offline` | the channel gave up and the reason is fatal: no upgrade at all through the proxy (HTTP 400/426), an expired session (401), a disabled hub (503) or a missing endpoint (404). Hover the badge to read the reason. |

While the badge shows `Offline` the dashboard is refreshed by polling every 20 s
(that is a fallback, not a fix), and returning to the tab or recovering the
network triggers an immediate retry. If you see `Offline`, the tooltip tells you
what to fix in the proxy.

## 3. Traefik (labels or file provider)

```yaml
# docker-compose.yml service labels
services:
  up:
    image: ghcr.io/ivancarlosti/up:latest
    env_file: .env
    extra_hosts: ["host.docker.internal:host-gateway"]
    labels:
      - "traefik.enable=true"
      - "traefik.http.routers.up.rule=Host(`up.example.com`)"
      - "traefik.http.routers.up.entrypoints=websecure"
      - "traefik.http.routers.up.tls.certresolver=letsencrypt"
      - "traefik.http.services.up.loadbalancer.server.port=3000"
    networks: [proxy]
networks:
  proxy:
    external: true
```

Traefik sets `X-Forwarded-*` automatically and handles WebSocket upgrades without
extra configuration. `APP_TRUST_PROXY=true` is correct when Up only joins the
proxy network.

## 4. Nginx

```nginx
# Outside the server block: only WebSocket requests get Connection: upgrade,
# a plain request keeps the connection header of the client (a fixed
# `Connection "upgrade"` breaks normal keep-alive traffic).
map $http_upgrade $connection_upgrade {
  default upgrade;
  ''      close;
}

server {
  listen 443 ssl http2;
  server_name up.example.com;

  ssl_certificate     /etc/letsencrypt/live/up.example.com/fullchain.pem;
  ssl_certificate_key /etc/letsencrypt/live/up.example.com/privkey.pem;

  client_max_body_size 8m;

  location / {
    proxy_pass http://127.0.0.1:3000;
    proxy_http_version 1.1;

    # WebSocket (/api/ws)
    proxy_set_header Upgrade    $http_upgrade;
    proxy_set_header Connection $connection_upgrade;
    proxy_read_timeout 3600s;
    proxy_send_timeout 3600s;
    proxy_buffering off;

    # real client information
    proxy_set_header Host              $host;
    proxy_set_header X-Real-IP         $remote_addr;
    proxy_set_header X-Forwarded-For   $proxy_add_x_forwarded_for;
    proxy_set_header X-Forwarded-Proto $scheme;
    proxy_set_header X-Forwarded-Host  $host;
  }
}

server {
  listen 80;
  server_name up.example.com;
  return 301 https://$host$request_uri;
}
```

With `APP_TRUST_PROXY=true`, Up reads `X-Forwarded-For` (first entry) and
`X-Forwarded-Proto`. Prefer `APP_TRUST_PROXY=127.0.0.1` when Nginx runs on the
same host.

## 5. Caddy

```caddyfile
up.example.com {
    encode zstd gzip
    reverse_proxy 127.0.0.1:3000 {
        # Caddy handles Upgrade/Connection and sets X-Forwarded-* automatically
        header_up X-Real-IP {remote_host}
    }
}
```

Caddy terminates TLS, adds `X-Forwarded-Proto`/`X-Forwarded-For` and transparently
proxies WebSockets; nothing else is required.

## 6. Cloudflare Tunnel

```bash
cloudflared tunnel create up
cloudflared tunnel route dns up up.example.com
cat > ~/.cloudflared/config.yml <<'YAML'
tunnel: up
credentials-file: /home/user/.cloudflared/<tunnel-id>.json
ingress:
  - hostname: up.example.com
    service: http://127.0.0.1:3000
    originRequest:
      noTLSVerify: true
      connectTimeout: 30s
  - service: http_status:404
YAML
cloudflared tunnel run up
```

Cloudflare already terminates TLS and forwards the original scheme, so set
`APP_URL=https://up.example.com` and `APP_TRUST_PROXY=true` (Cloudflare's IP
ranges are not stable, so a CIDR list is impractical here; keep the origin
firewalled to Cloudflare instead). Remember that a WebSocket path
(`/api/ws`) must not be blocked by a page rule.

## 7. Direct exposure (no proxy)

```env
APP_URL=http://192.168.1.10:3000
APP_TRUST_PROXY=false
APP_PORT=3000
```

In this mode do not enable the forwarded headers: the instance is reachable
directly and the headers could be spoofed. HTTPS is recommended anyway, because
the session cookie is only marked `Secure` when `APP_URL` starts with `https://`.

## 8. Checklist after switching to a proxy

1. `APP_URL` matches the browser address exactly (scheme + host, no trailing
   slash).
2. The Keycloak client redirect URI is `APP_URL + /api/auth/callback`.
3. `APP_TRUST_PROXY` is `true` (or the proxy CIDRs) **only** when the instance is
   not directly reachable.
4. `/api/health` answers `200` through the proxy.
5. The dashboard badge shows `Live` (the WebSocket connected). If it shows
   `Offline`, hover it: the tooltip names the cause, and `docs/reverse-proxy.md`
   §9 lists the fixes.
6. The Admin > Security page shows your **public** address as `client_ip`.
7. Optional: set security headers in the proxy as well (`HSTS`, `X-Frame-Options`)
   - Up already sends a defensive set.

## 9. Troubleshooting

| Symptom | Cause |
|---|---|
| Badge shows `Offline` (tooltip: *the reverse proxy does not forward the WebSocket upgrade*) | the proxy strips `Upgrade`/`Connection`: for Nginx use the `map $http_upgrade $connection_upgrade` block of §4 (a fixed `Connection "upgrade"` breaks plain requests). `curl -i -H 'Upgrade: websocket' -H 'Connection: Upgrade' https://up.example.com/api/ws` answers `400/426` when this is the case. |
| Badge stays on `Reconnecting…` forever | the backend is unreachable or restarting (transient): the client retries with backoff and recovers by itself. If it never recovers check the Up logs. |
| Badge shows `Offline` with *the server disabled real time updates* | `WS_ENABLED=false` (or the hub failed to start); the dashboard still works through the 20 s polling fallback. |
| Badge shows `Offline` with *the session expired* | the session cookie is gone or the token expired: sign in again. |
| The dashboard is not live but the page does not look broken | the polling fallback is doing its job - the badge (and its tooltip) says so. |
| `client_ip` shows the proxy address | `APP_TRUST_PROXY=false` while behind a proxy |
| IP rule blocks everybody after enabling a proxy | the allow list contains public addresses but the resolved IP is the proxy: fix `APP_TRUST_PROXY` |
| OIDC `redirect_uri` mismatch | `APP_URL` differs from the address used by the browser, or `KEYCLOAK_REDIRECT_URI` was set manually and diverged |
| Links in notifications point to `localhost` | `APP_URL` was not updated |
| 502/503 only on `/api/ws` | the proxy has a short read timeout: raise it (Nginx `proxy_read_timeout`) |
| Tunnel/proxy drops the channel every minute | Cloudflare (and some load balancers) close idle WebSockets; Up pings every 25 s, so raise the idle timeout above that if your provider allows it |
