# Up - Development

> Everything needed to run, test and extend Up locally. The toolchain used by
> this machine is documented here so another agent can reproduce the setup
> without guessing.

## 1. Toolchain

| Tool | Version used | Why |
|---|---|---|
| Go | **1.27.1** | `github.com/nicholas-fedor/shoutrrr` requires `go 1.27` |
| Node.js | **24.21.0 LTS (Krypton)** | Vite 8 / vue-i18n 11 require Node >= 22.12 |
| Docker | 29.x + Compose v2 | image build and end-to-end testing |
| MariaDB | 11.8 (container) | the database is always external |
| python3 + PIL | optional tooling | icon generation (`favicon.ico`) |

Installation without `sudo` (everything under `~/.local`, the pattern already
used on this machine):

```bash
# Go 1.27.1
cd /tmp
curl -fsSLO https://dl.google.com/go/go1.27.1.linux-amd64.tar.gz
echo "63d339f0da5ab53635a56f2490a7984dfe12dfcff22ad749f63edaf590168445  go1.27.1.linux-amd64.tar.gz" | sha256sum -c -
mkdir -p ~/.local && tar -C ~/.local -xzf go1.27.1.linux-amd64.tar.gz
grep -q 'local/go/bin' ~/.bashrc || printf '\n# Go 1.27.1\nexport PATH="$HOME/.local/go/bin:$PATH"\n' >> ~/.bashrc

# Node 24.21.0 LTS
curl -fsSLO https://nodejs.org/dist/v24.21.0/node-v24.21.0-linux-x64.tar.xz
echo "fd8e59d5a511510f6a298afb548f18c7d2b1be404d8b4a27d94fbe49f56cb2d6  node-v24.21.0-linux-x64.tar.xz" | sha256sum -c -
tar -C ~/.local -xJf node-v24.21.0-linux-x64.tar.xz
grep -q 'node-v24.21.0-linux-x64' ~/.bashrc || printf '\n# Node.js 24.21.0 LTS\nexport PATH="$HOME/.local/node-v24.21.0-linux-x64/bin:$PATH"\n' >> ~/.bashrc

# verify
export PATH="$HOME/.local/go/bin:$HOME/.local/node-v24.21.0-linux-x64/bin:$PATH"
go version && node -v && npm -v
```

## 2. Database for local development

There is no database service in `docker/docker-compose.yml` (by design), so a
disposable container is used for development and tests:

```bash
docker run -d --name up-dev-mariadb \
  -e MARIADB_ROOT_PASSWORD=root -e MARIADB_DATABASE=up \
  -e MARIADB_USER=up -e MARIADB_PASSWORD=secret \
  -p 3306:3306 \
  --health-cmd='healthcheck.sh --connect --innodb_initialized' \
  --health-interval=5s --health-timeout=5s --health-retries=20 \
  mariadb:11

# wait for readiness
until docker exec up-dev-mariadb healthcheck.sh --connect --innodb_initialized >/dev/null 2>&1; do sleep 2; done
docker exec up-dev-mariadb mariadb -u up -psecret up -e 'SELECT VERSION();'
```

## 3. Environment files

| File | Used by | `DB_HOST` |
|---|---|---|
| `docker/.env` | `docker compose -f docker/docker-compose.yml up` | `host.docker.internal` |
| `.env` (repository root, git-ignored) | `go run ./cmd/server` | `127.0.0.1` |
| `docker/.env.example` | the canonical reference (all variables) | `host.docker.internal` |

> `docker/.env` is used twice: `env_file` injects it into the container and Compose
> also interpolates the compose file with it (that is how `APP_PORT` drives the
> published port). The root `.env` is only read by the Go binary itself.

```bash
cp docker/.env.example .env          # local backend
sed -i 's/host.docker.internal/127.0.0.1/' .env
```

## 4. Running the backend

```bash
export PATH="$HOME/.local/go/bin:$PATH"
cd /home/ivan/Documents/Git/up
go run ./cmd/server            # reads .env, migrates, seeds, starts the scheduler
```

Useful flags/behaviours:

- `LOG_LEVEL=debug` enables GORM SQL logging and verbose scheduler logs.
- The server refuses to start with an invalid `.env` and prints every problem at
  once (exit code 2).
- `GET /api/health` is the quickest smoke test; `GET /api/settings` proves the
  SPA bootstrap data is reachable without a session.
- Without a frontend build the embedded SPA shows a placeholder page; build it
  (next section) to see the real UI.

## 5. Running the frontend

```bash
export PATH="$HOME/.local/node-v24.21.0-linux-x64/bin:$PATH"
cd web
npm ci                 # install exactly the locked versions
npm run dev            # http://localhost:5173, proxies /api (and the WS) to :3000
npm run build          # writes web/dist (embedded by the Go binary)
npm run typecheck      # vue-tsc --noEmit (strict)
```

- The Docker build uses `npm ci` + `npm run build`; the committed
  `package-lock.json` is the source of truth.
- `web/dist/index.html` is a committed placeholder so `go build` works before
  the first frontend build. `web/dist` is otherwise git-ignored.
- `VITE_API_TARGET` overrides the dev proxy target (e.g.
  `VITE_API_TARGET=http://localhost:3000`).

## 6. Tests and static checks

```bash
cd /home/ivan/Documents/Git/up
export PATH="$HOME/.local/go/bin:$PATH"

go build ./...          # compile everything
go vet ./...            # static analysis
go test ./...           # unit tests (checkers, aggregation, IP rules, notify)
go test ./internal/services -run AggregateVotes -v
gofmt -l .              # formatting check (empty output = clean)

cd web && npm run typecheck && npm run build
```

Security checks (see `docs/security.md` §10 for the last audit results):

```bash
# reachability-aware scan of the shipped code
go install golang.org/x/vuln/cmd/govulncheck@latest
govulncheck ./...

# dependency advisories for the frontend
cd web && npm audit

# container image (OS packages + the Go binary)
docker run --rm -v /var/run/docker.sock:/var/run/docker.sock \
  aquasec/trivy:latest image --severity HIGH,CRITICAL --ignore-unfixed \
  ghcr.io/ivancarlosti/up:latest
```

## 7. Building and running the image

```bash
docker build -t ghcr.io/ivancarlosti/up:latest .
docker compose -f docker/docker-compose.yml up -d
docker compose -f docker/docker-compose.yml logs -f up
docker compose -f docker/docker-compose.yml down
```

Validate the compose file without starting it:

```bash
docker compose -f docker/docker-compose.yml config
```

### Changing the published port

`APP_PORT` (in `docker/.env`) is the port the container listens on **and** the port
published on the host, because `docker/docker-compose.yml` interpolates it out of
the same `.env` file (Compose reads the `.env` of the *project directory*, which is
`docker/` when you use `-f docker/docker-compose.yml`). Nothing else needs to be
edited:

```bash
# 1) container + host on 8080
sed -i 's/^APP_PORT=.*/APP_PORT=8080/' docker/.env
docker compose -f docker/docker-compose.yml up -d
curl -s localhost:8080/api/health          # {"status":"ok",...}

# 2) different host port, container stays on 3000
#    (optional HOST_PORT variable, defaults to APP_PORT)
printf 'HOST_PORT=80\n' >> docker/.env     # APP_PORT=3000 remains
docker compose -f docker/docker-compose.yml up -d
curl -s localhost:80/api/health

# 3) one-off override without touching the file
APP_PORT=9090 docker compose -f docker/docker-compose.yml up -d

# inspect what Compose resolved
docker compose -f docker/docker-compose.yml config | grep -A3 ports:
docker compose -f docker/docker-compose.yml config --environment | grep APP_PORT
```

Notes:

- The image `HEALTHCHECK` already uses `${APP_PORT}`, so `docker inspect` keeps
  reporting `healthy` on any port.
- `EXPOSE 3000` in the Dockerfile is only metadata (it documents the default and
  does not restrict the published port).
- `APP_URL` must match the address you actually browse to (scheme + host + port),
  otherwise redirects, CORS and notification links point to the wrong place.
- On a Compose version without nested defaults (`${A:-${B:-3000}}`), replace the
  `ports` line in `docker/docker-compose.yml` with
  `"${APP_PORT:-3000}:${APP_PORT:-3000}"` and use `APP_PORT` alone.

## 8. Notification sinks for testing

```bash
# SMTP sink (Mailpit): SMTP on 1025, web UI on http://localhost:8025
docker run -d --name up-dev-mailpit -p 1025:1025 -p 8025:8025 axllent/mailpit

# Webhook sink (a tiny Python logger on 0.0.0.0:1080)
python3 - <<'PY'
from http.server import BaseHTTPRequestHandler, HTTPServer
class H(BaseHTTPRequestHandler):
    def log_message(self, *a): pass
    def _h(self):
        n = int(self.headers.get('content-length') or 0)
        print(f"[{self.command}] {self.path} BODY={self.rfile.read(n).decode()}", flush=True)
        self.send_response(200); self.end_headers(); self.wfile.write(b'ok')
    do_GET = do_POST = do_PUT = do_PATCH = _h
HTTPServer(('0.0.0.0', 1080), H).serve_forever()
PY
```

> A container must reach these services as `host.docker.internal:1025` and
> `http://host.docker.internal:1080/hook`; the sink must listen on `0.0.0.0`.
> From a `go run` backend, `127.0.0.1` is correct.

Inspect what arrived:

```bash
curl -s localhost:8025/api/v1/messages | python3 -m json.tool | head -20
tail -5 /tmp/up-webhook-sink.log
```

## 9. Two node cluster in three commands

```bash
docker network create up-test
docker run -d --name up-node1 --network up-test --add-host host.docker.internal:host-gateway \
  -p 3000:3000 --env-file docker/.env \
  -e CLUSTER_ENABLED=true -e NODE_ID=up-node-1 -e NODE_NAME='Primary Node' \
  -e APP_URL=http://up-node1:3000 ghcr.io/ivancarlosti/up:latest

docker run -d --name up-node2 --network up-test --add-host host.docker.internal:host-gateway \
  -p 3002:3000 --env-file docker/.env \
  -e CLUSTER_ENABLED=true -e NODE_ID=up-node-2 -e NODE_NAME='Node 2' \
  -e APP_URL=http://up-node2:3000 ghcr.io/ivancarlosti/up:latest

# join (key from GET /api/cluster/private-key on node 1)
curl -c c2.txt -X POST localhost:3002/api/auth/login -H 'Content-Type: application/json' \
  -d '{"email":"admin@example.com","password":"admin123"}'
curl -b c2.txt -X POST localhost:3002/api/cluster/join -H 'Content-Type: application/json' \
  -d '{"primary_url":"http://up-node1:3000","private_key":"<key>","node_id":"up-node-2","node_name":"Node 2"}'

# health of the cluster
curl -s -b c1.txt localhost:3000/api/cluster/status | python3 -m json.tool | head -30
```

> The cluster commands above use plain `docker run`, so the container side of
> `-p` must stay equal to `APP_PORT` (3000 by default): `-p 3002:3000` publishes the
> second node on 3002 while it still listens on 3000 internally, and `APP_URL` must
> stay `http://up-node2:3000` (the port other nodes reach it on).

## 10. Regenerating the icons

The logo is a hand written SVG (`web/public/logo.svg`); the rasterized assets are
derived from it:

```bash
# PNGs (16..512) through resvg-js, installed outside the repo on purpose
npm install --prefix /tmp/up-icons @resvg/resvg-js
NODE_PATH=/tmp/up-icons/node_modules node tools/generate-icons.mjs /path/to/up

# favicon.ico (16/32/48) through Pillow
python3 - <<'PY'
from PIL import Image
sizes = [(16, 16), (32, 32), (48, 48)]
imgs = [Image.open(f'web/public/favicon-{s[0]}.png').convert('RGBA') for s in sizes]
imgs[0].save('web/public/favicon.ico', sizes=sizes, append_images=imgs[1:])
PY
```

## 11. Debugging cheatsheet

| Symptom | Check |
|---|---|
| `Up cannot start: invalid configuration` | the printed list; `docker/.env.example` is the reference |
| Database retries forever | `DB_HOST`, credentials, and whether the DB accepts connections from the container (`host.docker.internal`) |
| Monitor stays `pending` | the worker log line `monitor worker started`; then `GET /api/monitors/:id/heartbeats` |
| No notification | `GET /api/notifications/logs`, then `POST /api/notifications/:id/test` |
| Dashboard not updating live | `GET /api/ws` requires the session cookie; check the reverse proxy upgrade headers |
| `web/dist` placeholder still shown | run `npm run build` then rebuild the Go binary (the assets are embedded) |

## 12. Conventions for contributors

- Comments and identifiers in **English** (UI strings live only in the locale
  JSON files).
- Business rules belong to `internal/services`; handlers stay thin.
- New API errors need a code in `internal/i18n/errors.go` **and** a translation in
  the three locale files.
- Docker/docs references: keep `docker/.env.example`, `docker/.env` and this
  document in sync when a variable is added.
- `docker/` contains exactly `docker-compose.yml`, `.env` and `.env.example`.
