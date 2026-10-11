# Plan - Probe IP family (`alternate` default) and removal of the expiry family pin

> **Status: in progress.** Task given on 2026-09-26: (1) remove the failed IPv6 workarounds for
> WHOIS/expiry dialing, (2) document that users must run the container with the Docker network type
> `host` to reach IPv6-only registries, (3) add a monitor option that **alternates between IPv4 and
> IPv6 on each test**, default for monitors and templates, overridable per monitor.
>
> Folder tracked in git (the `/plans/` exclusion was removed on 2026-10-11), excluded
> from the Docker build context (`.dockerignore`). This file is the live checklist: if
> the work is interrupted, read it first.

## 1. Goal

| # | Deliverable |
|---|---|
| 1 | `expiry_ip_version` (the admin pin that made a container without an IPv6 route fail fast) is **gone**: model, setting, API, UI, locales, tests, docs |
| 2 | The IPv6-enabled ULA network is **gone** from both compose files; the README/`.env.example` say the supported way to reach an IPv6-only registry is `network_mode: host` |
| 3 | New `config.ip_family` on monitors and templates: `alternate` (default) \| `auto` \| `ipv4` \| `ipv6` |

## 2. Decisions taken

- **Soft rotation, hard pin.** `alternate` flips IPv4/IPv6 on every execution but only *pins* the
  family when the target really has an address in it; otherwise the probe keeps Go's happy eyeballs.
  A single-stack target must never be reported down by the rotation. An explicit `ipv4`/`ipv6` is a
  hard pin (it fails when the family is unavailable) - that is what an override is for. `auto` stays
  as the opt-out.
- **One execution, one family.** The retries inside a single test keep the family of the turn, so a
  retry re-tests the path that just failed instead of hiding it behind the other family.
- **The rotation lives in the worker** (`worker.family`, seeded from `id%2` so a fleet splits over
  both paths after a restart), because the worker is already the per-monitor state holder. A restart
  re-seeds; documented limitation.
- **In-memory only, no schema change.** The heartbeat keeps its columns: Go's dial error already
  names `tcp4`/`tcp6`, so the family is visible in the failure message without a migration, an API
  field or a UI column.
- **The expiry diagnostics stay.** `registryDialError`/`describeDialAttempts` are not the pin: they
  only run after a failed `auto` dial and are what made the original incident readable. Only the
  "enable IPv6 for that network" hint changes, and it now points at `network_mode: host`.
- **`ip_family` is a field of every monitor type** (like `ignore_tls`/`host`), so `PruneToType`,
  `CONFIG_FIELDS` and the UI list it for all five types. It is stored in `MonitorConfig`, so both the
  monitor and the template carry it with no extra plumbing.

## 3. File map

| Part | Files |
|---|---|
| A - model | `internal/models/domain.go`, `internal/models/security.go`, `internal/models/domain_test.go` |
| A - service | `internal/services/settings.go` |
| A - dialing | `internal/expiry/netdial.go`, `whois.go`, `rdap.go`, `resolver.go`, `netdial_test.go`, `rdap_lookup_test.go`, `whois_live_test.go` |
| A - frontend | `web/src/lib/types-platform.ts`, `web/src/views/admin/AdminExpiryView.vue`, `expiry.ipVersion*` in all 7 locales |
| A/B - docs | `docs/api.md`, `docs/monitors.md`, `README.md`, `docker/.env.example` |
| B - docker | `docker/docker-compose.yml`, `docker/docker-compose-bundle.yml` |
| C - model | `internal/models/ip_family.go` (new), `internal/models/monitor_config.go`, `internal/models/monitor_validate.go` |
| C - runtime | `internal/scheduler/worker.go`, `execute.go`, `internal/checkers/checker.go` + `ipfamily.go` (new), `http.go`, `tcp.go`, `tls.go`, `dns.go` |
| C - frontend | `web/src/lib/types.ts`, `web/src/lib/monitor-config.ts`, `web/src/components/monitors/MonitorConfigFields.vue`, `MonitorForm.vue`, `web/src/views/admin/AdminMonitorTemplatesView.vue`, `monitor.ipFamily*` in all 7 locales |
| C - docs | `docs/monitors.md`, `docs/api.md`, `docs/architecture.md`, `docs/database.md` (`config` JSON column), `docs/public-api.md` (example) |
| C - tests | `internal/models/ip_family_test.go` (new), `internal/checkers/ipfamily_test.go` (new), `internal/scheduler/worker_test.go` (new), `internal/models/monitor_config_test.go` |

## 4. Progress

- [x] Reconnaissance (see §3 and the archived `2026-09-26-expiry-ip-family-dialing.md`)
- [x] A - remove the expiry family pin (backend + frontend + locales + docs)
- [x] B - docker/README: no IPv6 network, `network_mode: host`
- [x] C1/C2 - `models.IPFamily` + `MonitorConfig` + validation + pruning
- [x] C3/C4 - worker rotation + checkers dial plan
- [x] C5 - UI + locales
- [x] C6/C7 - docs + tests
- [x] Verification (§5)

## 5. Verification

| command | result |
|---|---|
| `gofmt -l internal` | empty |
| `go build ./...` | ok |
| `go vet ./...` | ok |
| `go test -count=1 ./...` | all packages ok (new tests: `ip_family_test.go`, `ipfamily_test.go`, `ipfamily_live_test.go`, `worker_test.go`, plus cases added to `monitor_config_test.go` / `monitor_validate_test.go`) |
| `UP_LIVE_NET=1 go test ./internal/checkers/ -run TestLiveIPFamilyDialPlan -v` | PASS: `ipv4 reached 1.1.1.1:443`, `ipv6 reached [2606:4700:4700::1001]:443` (the plan really dials the family it names) |
| live stack: `go build ./cmd/server` + `mariadb:11` in Docker + the real API | see the "Live API check" block below |
| `UP_LIVE_WHOIS=1 go test ./internal/expiry/ -run TestLiveWhois` | ok, 62.5 s: the 64 built-in registry rules still answer through the plain happy-eyeballs dial. `UP_LIVE_WHOIS=1 go test -v ./internal/expiry/ -run TestLiveWhoisDefaults/pt` -> `.pt dns.pt -> 2026-12-31T23:59:00Z` in 0.89 s (the IPv6-only registry that started this workstream) |
| `cd docker && docker compose -f docker-compose.yml config` / `-f docker-compose-bundle.yml config` | both parse; every service lands on the implicit `default` network (`mariadb` and `up` share it) |
| `cd web && npm run typecheck` | ok (vue-tsc, no errors) |
| `cd web && npm run check:i18n` | ok: 738 messages/locale, 617 keys used, 5166 compiled |
| `git grep -n "ip_version\|enable_ipv6"` | empty |
| `cd web && npm run build` | **cannot run here**: node v18.19.1 vs `engines.node >= 22.12.0`; vite 8.3.0 (rolldown) needs `node:util.styleText`. Pre-existing environment constraint, not caused by this change (no build config was touched) |

### Live API check (real server + MariaDB in Docker, 2026-09-27)

`go build ./cmd/server`, a throwaway `mariadb:11` container, then the real API:

| request | answer |
|---|---|
| `GET /api/admin/expiry` | `{check_time, check_timezone, rdap_enabled, whois_enabled, rate_limit_ms, timeout_seconds}` - no `ip_version` |
| `PUT /api/admin/expiry` **with** a legacy `ip_version` | `200`, and the field is not echoed back (an old client keeps working, nothing is stored) |
| `POST /api/monitors` `config.ip_family = "ipv7"` | `400 ERR_MONITOR_CONFIG_INVALID config.ip_family must be one of alternate, auto, ipv4, ipv6` |
| `POST /api/monitors` `ip_family` `ipv6` / `ipv4` / `auto` | `201`, stored verbatim |
| `POST /api/monitors` **without** the field | `201`, stored as `alternate` |
| `POST /api/monitors/:id/check`, `ip_family=ipv6`, target `127.0.0.1:3111` | down: `tcp connect failed: dial tcp6: address 127.0.0.1: no suitable address found` |
| `POST /api/monitors/:id/check`, `ip_family=ipv4`, target `127.0.0.1` | up: `tcp connection established` |
| same target, `ip_family=ipv4`, port `1` | down: `tcp connect failed: connection refused (tcp4)` - the family survives the shortened message |
| same target, `ip_family=ipv6`, port `1` | down: `tcp connect failed: dial tcp6: address 127.0.0.1: no suitable address found` |
| `alternate`, single stack target, both turns | up on both turns (the soft rotation never reports a family the target does not have) |

## 6. Deviations / follow-ups

- **`alternate` also rotates a DNS monitor's resolver.** `dns.Client.Net` gets `udp4`/`udp6`
  when the plan pins a family, so a resolver given as an IPv4 literal (the default `1.1.1.1`) is
  simply never pinned by the soft rotation (the plan falls back). A hostname resolver that
  publishes both families really rotates. An explicit `ipv4`/`ipv6` pins the resolver connection.
- **`probeTarget` resolves a name once when a family is pinned** (the address is then dialed as a
  literal, so one execution tests exactly one path). That is one extra lookup per probe for
  `alternate` and for the explicit pins; `auto` is untouched.
- **No migration, no new column, no new API field.** The rotated family shows up in the failure
  message (Go names `tcp4`/`tcp6`), which is why the heartbeat schema stays as it is. The orphan
  `expiry_ip_version` settings row of an existing installation is ignored (nothing reads it).
- **`alternate` is a real behavior change for dual stack targets:** a host whose IPv6 (or IPv4)
  path is dead is now reported down on the turns that test it. That is the point of the default;
  `auto` is the documented opt-out.
- **The old IPv6-enabled compose network is gone**, so a `.pt` style registry needs
  `network_mode: host` (Linux) or a daemon-level IPv6 configuration. Documented in the README,
  `docker/.env.example` and `docs/api.md`.
- **`describeRequestError` now qualifies a shortened message with the family** the dial pinned
  (`connection refused (tcp6)`, `timeout exceeded (tcp6)`). Without it an alternating monitor
  would report the same text for the two paths, and the operator could not tell which one broke.
  Only messages that name a family are qualified, so nothing changes for `auto`.
- **`NormalizeIPFamily` does not swallow a typo.** It canonicalizes what it knows and passes
  anything else through (lowered), so `MonitorConfig.Normalize` + `Validate` reject the write with
  `400`. An empty value still means the default rotation, which is what a monitor stored before
  the field existed carries.
- **REVERSED on 2026-09-27 (operator request): the default is now `auto`, not `alternate`.** §1/§2
  above describe the original decision; the implemented behavior is the reverse: `auto` (the
  dialer's happy eyeballs, unchanged from before the field existed, IPv6 tried first when the name
  has an AAAA and the host has a usable IPv6 route, IPv4 as a silent fallback) is `DefaultIPFamily`,
  the first entry of `models.IPFamilies` and the first option of the UI select, while `alternate`
  became the opt-in that reports a dual stack host whose second path is dead. Consequences to keep
  in mind: (a) `auto` cannot detect a dead family by design - that is documented in
  `docs/monitors.md`, `docs/api.md`, `docs/database.md` and the `ipFamilyHelp` of the 7 locales;
  (b) a monitor or template stored with an empty `ip_family` (i.e. created before the field
  existed) now behaves as `auto` instead of rotating, because `NormalizeIPFamily("")` returns the
  default - no migration/backfill was added, this is the documented behaviour change; (c) the
  `400` message of an unknown value now lists `auto, alternate, ipv4, ipv6`.
