<div align="center">

<img src="web/public/logo.svg" alt="Up" width="96" height="96" />

# Up

**Minimalist, cluster-ready uptime monitoring.**

One Go binary - hosts its own Vue 3 dashboard, writes to your external
MariaDB/MySQL, watches HTTP(s), Keyword, TCP, DNS and SSL targets, keeps an eye on
the expiration of the certificates and domains behind them, and runs as a cluster
of nodes that vote on the real status.

<!-- buttons -->
[![Stars](https://img.shields.io/github/stars/ivancarlosti/up?label=⭐%20Stars&color=gold&style=flat)](https://github.com/ivancarlosti/up/stargazers)
[![Watchers](https://img.shields.io/github/watchers/ivancarlosti/up?label=Watchers&style=flat&color=red)](https://github.com/sponsors/ivancarlosti)
[![Forks](https://img.shields.io/github/forks/ivancarlosti/up?label=Forks&style=flat&color=ff69b4)](https://github.com/sponsors/ivancarlosti)
[![Downloads](https://img.shields.io/github/downloads/ivancarlosti/up/total?label=Downloads&color=success)](https://github.com/ivancarlosti/up/releases)
[![GitHub commit activity](https://img.shields.io/github/commit-activity/m/ivancarlosti/up?label=Activity)](https://github.com/ivancarlosti/up/pulse)
[![GitHub Issues](https://img.shields.io/github/issues/ivancarlosti/up?label=Issues&color=orange)](https://github.com/ivancarlosti/up/issues)  
[![License](https://img.shields.io/github/license/ivancarlosti/up?label=License)](LICENSE)
[![GitHub last commit](https://img.shields.io/github/last-commit/ivancarlosti/up?label=Last%20Commit)](https://github.com/ivancarlosti/up/commits)
[![Security](https://img.shields.io/badge/Security-View%20Here-purple)](https://github.com/ivancarlosti/up/security)
[![Code of Conduct](https://img.shields.io/badge/Code%20of%20Conduct-2.1-4baaaa)](https://github.com/ivancarlosti/up?tab=coc-ov-file)
<!-- endbuttons -->

</div>

## Features

| Feature | Description |
|---|---|
| **Monitors** | HTTP(s) (method, encoding, body, headers, basic/bearer auth, redirects, cache buster, accepted status codes, ignore TLS), HTTP(s) Keyword (invert, case sensitive), TCP (send/expect), DNS (A/AAAA/CNAME/MX/TXT/NS/SOA through a chosen resolver, invert check) |
| **Scheduling** | one worker per monitor, per-monitor interval, timeout, retries with a `pending` phase and a re-notification interval |
| **Dashboard** | actionable summary boxes (status counters plus *certificates expiring in 7 days* and *domains expiring in 30 days or expired*) that filter the **shared sortable monitors table** (name, type, group, status, interval, uptime over a selectable period, certificate/domain expiry and a bucketed heartbeat sparkline), live status via WebSocket, per-node breakdown and a monitor detail with statistics and an event log |
| **Uptime window & history** | one global period (24 h / 7 d / 14 d / 30 d) drives the dashboard, the monitors table, the detail page and the heartbeat sparkline, with a per status page override; heartbeat history is pruned by a configurable retention (**180 days by default**, `0` = never) with a preview and a *purge now* button in Admin > Settings |
| **Groups & clones** | named groups of monitors (filter, shallow/deep clone), monitor clone, groups drive the status pages |
| **Templates & bulk** | reusable monitor templates of **every type** (`http`, `keyword`, `tcp`, `dns`, `ssl`), add monitors by pasting `name,target[,type,...]` (per row report, duplicates skipped, a row can override the type of its template), a bulk edit with a diff preview and monitors that **follow** a template (editing it pushes the defaults to every linked monitor; groups and tags stay untouched) |
| **Certificates** | a `ssl` type plus certificate watching on any https monitor: validity badge on the dashboard/detail/admin list, free thresholds (`7,6,5,30`) and daily `cert_expiring`/`cert_expired` reminders |
| **Domain expiration** | registry watching for the monitor's domain: RDAP first, per-TLD WHOIS parsers (Admin > TLD/SSL expiration) and a manual date for the TLDs that publish none (one date per registrable domain, applied the moment it is saved and independent of the watch switch); free thresholds and daily `domain_expiring`/`domain_expired` reminders |
| **Expiry scheduling** | one daily, configurable-time check for certificates and domains, **deduplicated by target** (many monitors on the same host/domain = one lookup) with an admin rate limit per registry, a target list and a per-target *check now* |
| **Notifications** | SMTP, Webhook, Slack, Discord and Telegram through the [shoutrrr](https://github.com/nicholas-fedor/shoutrrr) engine, custom webhook body template, delivery history, test button |
| **Status pages** | public pages with groups, per-page toggles (uptime, charts, tags) and an **opt-in expiry badge**, plus a shields.io style badge |
| **Authentication** | `none`, single `account` (with optional reCAPTCHA) or `keycloak` OIDC (Authorization Code + PKCE) with an e-mail/domain allow list |
| **Cluster** | two modes. **`shared`** (default): several nodes on the same external database, join with a private key, node liveness (offline after 2 min), `ANY_NODE_FAILS` / `ALL_NODES_FAIL` / `QUORUM` voting and `PRIMARY_ONLY` / `ANY_WITH_LOCK` notification sender. **`federated`**: one database per node, the configuration, the votes and the notification ownership synchronised over the signed peer API (`CLUSTER_PEER_API`), with a derived leader, a notification election (`leader`/`hash`/`origin`), `run_on=some`, opt-in channel/settings/session-secret sync and push. See [clustering.md](docs/clustering.md) and [clustering-modes.md](docs/clustering-modes.md) |
| **Public status pages** | per-slug pages with theme, monitor selection, uptime/charts and a README badge |
| **Public REST API** | scoped bearer tokens (`read`/`write`), IP allow/deny rules, rate limiting |
| **i18n & theme** | en-US, pt-BR, es-MX, fr-FR, zh-CN, hi-IN, ar-SA (with RTL), light/dark/system and a 12/24-hour clock preference, with configurable defaults for new visitors |

## Quick start (Docker + external MariaDB)

```bash
cp docker/.env.example docker/.env     # set APP_URL and the DB credentials

docker compose -f docker/docker-compose.yml up -d
docker compose -f docker/docker-compose.yml logs -f up
```

The compose file uses `ghcr.io/ivancarlosti/up:latest` and reaches your database
through `host.docker.internal` (`extra_hosts: host.docker.internal:host-gateway`).
**This file declares no database service**: Up connects to an external
MariaDB/MySQL (the bundle described below is the self-contained alternative).

The published port follows `APP_PORT` in `docker/.env`: `APP_PORT=3000` publishes
`3000:3000` (default) and `APP_PORT=8080` publishes `8080:8080`. To keep the
container on 3000 but publish another host port, set the optional `HOST_PORT`
(for example `HOST_PORT=80` -> `80:3000`). See
[docs/development.md](docs/development.md#changing-the-published-port).

Database prerequisites:

```sql
CREATE DATABASE up CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;
CREATE USER 'up'@'%' IDENTIFIED BY 'secret';
GRANT ALL PRIVILEGES ON up.* TO 'up'@'%';
```

Open `http://<host>:3000`, sign in with `ACCOUNT_LOGIN` / `ACCOUNT_PASSWORD` and
create your first monitor.

## Quick start (Docker bundle with MariaDB included)

`docker/docker-compose-bundle.yml` is the same application plus a MariaDB
container, for a machine that has no database server yet. Inside a compose
network a container is reached by its **service name**, so `DB_HOST` must be the
name of the database service declared there (`mariadb`):

```bash
cp docker/.env.example docker/.env
sed -i 's/^DB_HOST=.*/DB_HOST=mariadb/' docker/.env     # the bundled DB service

docker compose -f docker/docker-compose-bundle.yml up -d
docker compose -f docker/docker-compose-bundle.yml logs -f up
```

The database files live in the `mariadb_data` volume, so they survive restarts;
`docker compose -f docker/docker-compose-bundle.yml down -v` deletes them along
with every monitor, heartbeat and setting. Both compose files are alternatives:
they read the same `docker/.env` and use the same container name (`up`).

## Quick start (from source)

```bash
# Go 1.27.1 and Node 24 are required (see docs/development.md for installs)
export PATH="$HOME/.local/go/bin:$HOME/.local/node-v24.21.0-linux-x64/bin:$PATH"

cp docker/.env.example .env
sed -i 's/host.docker.internal/127.0.0.1/' .env

(cd web && npm ci && npm run build)    # once: fills web/dist (embedded)
go run ./cmd/server                    # http://localhost:3000
```

Frontend development with hot reload:

```bash
cd web && npm run dev                  # :5173, proxies /api and the WS to :3000
```

## Configuration

Everything is environment driven; `docker/.env.example` is the complete
reference and the server refuses to start with an incomplete or invalid setup
(printing every problem at once).

The essentials:

```env
APP_URL=https://up.example.com         # canonical public URL
APP_TRUST_PROXY=true                   # behind Traefik/Nginx/Caddy/Cloudflare
APP_PORT=3000                          # inside the container AND published on the host
# HOST_PORT=80                         # optional: publish a different host port

DB_HOST=host.docker.internal           # external database, never in compose
# DB_HOST=mariadb                      # bundled database (docker-compose-bundle.yml)
DB_PORT=3306
DB_DATABASE=up
DB_USERNAME=up
DB_PASSWORD=secret
DB_SSL=false

AUTH_METHOD=account                    # none | account | keycloak
ACCOUNT_LOGIN=admin@example.com
ACCOUNT_PASSWORD=admin123

DEFAULT_LOCALE=en-US                   # en-US | pt-BR | es-MX | fr-FR | zh-CN | hi-IN | ar-SA
DEFAULT_THEME=system                   # system | light | dark

CLUSTER_ENABLED=false
CLUSTER_MODE=shared                    # shared (same database) | federated (one database per node)
CLUSTER_PEER_API=false                 # signed node to node API + ping loop (required by federated)
NODE_ID=up-node-1
NODE_NAME=Primary Node
CLUSTER_PRIVATE_KEY=                   # generated on the first boot

# retention (Admin > Settings holds the live values)
# HEARTBEAT_RETENTION_DAYS=30          # fallback when the setting is absent; the setting defaults to 180 days, 0 = never
# NOTIFICATION_LOG_RETENTION_DAYS=90   # prune the delivery history (environment only, disabled by default)
```

## Public API and status pages

```bash
# create a read-only token (Admin > Security, or the API)
TOKEN=$(curl -s -b cookies.txt -X POST https://up.example.com/api/tokens \
  -H 'Content-Type: application/json' \
  -d '{"name":"ci","scopes":["read"],"expires_in_days":90}' | jq -r .token)

curl -H "Authorization: Bearer $TOKEN" https://up.example.com/api/v1/status
```

```markdown
![status](https://up.example.com/api/public/status/main/badge.svg)
```

## Documentation

| Document | Content |
|---|---|
| [architecture.md](docs/architecture.md) | components, boot sequence, heartbeat lifecycle, concurrency, where to change what |
| [database.md](docs/database.md) | connection, complete DDL, queries, indexes, migrations, retention |
| [monitors.md](docs/monitors.md) | every monitor type and option, retries, upside down mode |
| [notifications.md](docs/notifications.md) | SMTP, Webhook, Slack, Discord, Telegram, template variables, delivery log, troubleshooting |
| [authentication.md](docs/authentication.md) | `none`/`account`/`keycloak`, sessions, allow list, reCAPTCHA |
| [api.md](docs/api.md) | every endpoint with examples and the error code catalogue |
| [public-api.md](docs/public-api.md) | tokens, scopes and recipes for the `/api/v1` surface |
| [status-pages.md](docs/status-pages.md) | public status pages and badges |
| [security.md](docs/security.md) | IP rules, rate limiting, tokens, secrets inventory |
| [clustering.md](docs/clustering.md) | topology, join flow, liveness, voting and sender strategies |
| [clustering-modes.md](docs/clustering-modes.md) | cluster modes: `shared` vs `federated` (one database per node), sync protocol, identity, voting, notification election |
| [reverse-proxy.md](docs/reverse-proxy.md) | Traefik, Nginx, Caddy, Cloudflare Tunnel, WebSocket notes |
| [i18n.md](docs/i18n.md) | languages, resolution order, adding a language |
| [development.md](docs/development.md) | toolchain, local setup, tests, image build, sinks for testing |

## Screenshots

| ![dashboard.png](docs/screenshots/dashboard.png) | 
|:--:| 
| *Main dashboard panel* |

| ![monitor.png](docs/screenshots/monitor.png) | 
|:--:| 
| *Monitoring panel* |

| ![singlemonitor.png](docs/screenshots/singlemonitor.png) | 
|:--:| 
| *Single monitor status details* |

| ![status.png](docs/screenshots/status.png) | 
|:--:| 
| *Public Status page* |


## Tests

```bash
go test ./...        # checkers, cluster aggregation, IP rules, notification URLs
go vet ./...
cd web && npm run typecheck
```

## License

MIT - see [LICENSE](LICENSE).

<!-- footer -->
---

## 🧑‍💻 Consulting and technical support
* For personal support and queries, please submit a new issue to have it addressed.
* For commercial related questions, please [**contact me**][ivancarlos] for consulting costs.

[cc]: https://docs.github.com/en/communities/setting-up-your-project-for-healthy-contributions/adding-a-code-of-conduct-to-your-project
[contributing]: https://docs.github.com/en/articles/setting-guidelines-for-repository-contributors
[security]: https://docs.github.com/en/code-security/getting-started/adding-a-security-policy-to-your-repository
[support]: https://docs.github.com/en/articles/adding-support-resources-to-your-project
[it]: https://docs.github.com/en/communities/using-templates-to-encourage-useful-issues-and-pull-requests/configuring-issue-templates-for-your-repository#configuring-the-template-chooser
[prt]: https://docs.github.com/en/communities/using-templates-to-encourage-useful-issues-and-pull-requests/creating-a-pull-request-template-for-your-repository
[funding]: https://docs.github.com/en/articles/displaying-a-sponsor-button-in-your-repository
[ivancarlos]: https://ivancarlos.me
