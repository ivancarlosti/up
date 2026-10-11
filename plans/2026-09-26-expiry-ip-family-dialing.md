# Plan - Address-family-aware registry dialing (WHOIS/RDAP `ip_version`)

> **Archived plan** — task: *container-scoped WHOIS timeouts*
> (`dial tcp 52.37.99.5:43: i/o timeout`), worked and shipped on **2026-09-26** (staged, not yet
> committed when archived). Kept in `plans/` for future review because §3 holds the raw
> evidence (the container-vs-host dials and the live registry sweep) that the shipped code
> cannot reproduce. Folder tracked in git (the `/plans/` exclusion was removed on
> 2026-10-11), excluded from the Docker build context (`.dockerignore`).

## 1. Goal

Stop the daily expiry job from losing a whole registry to a ten-second timeout, and make the
failure explain itself:

1. An admin setting for the address family of every registry lookup
   (`auto` | `ipv4` | `ipv6`), stored as `expiry_ip_version`.
2. A pinned family must fail **immediately and explicitly** instead of hiding the other one.
3. `auto` must stay as fast as before and, when the lookup fails, report **every** resolved
   address with its own error, plus a hint when an entire family has no route.
4. Documentation: port-43 egress, the IPv6-in-Docker trap, the sidecar debugging recipe.

## 2. Decisions taken

- **One dial path for WHOIS and RDAP.** `internal/expiry/netdial.go` owns the network choice;
  RDAP reaches it by cloning `http.DefaultTransport` and replacing `DialContext`, so proxies,
  TLS and HTTP/2 defaults survive.
- **`auto` keeps Go's happy-eyeballs fast path.** The diagnostic pass only runs *after* a
  failure, under a 5 s budget (2 s per address), because `timeout_seconds` is the real budget
  of the daily job.
- **The hint is derived, not guessed:** "no IPv6 route" appears only when *every* IPv6 attempt
  failed with ENETUNREACH/EHOSTUNREACH; an address that connects on the retry produces
  "answered on a retry: probably rate limited" instead.
- **Normalise both ways.** `NormalizeExpiryIPVersion` accepts `4`/`v4`/`tcp4`/`6`/`v6`/`tcp6`
  and falls back to `auto`; `ValidExpiryIPVersion` accepts the empty string so an older
  frontend keeps working.
- **Not cluster-synced.** The setting describes the network of one machine, so it stays out of
  the sync whitelist in `models/security.go` like the other `expiry_*` keys.
- **Validated before dialing:** `ExpirySettings.Validate` returns
  `ip_version must be auto, ipv4 or ipv6` (HTTP 400).

## 3. The reported failure and the evidence

Symptom: `dial tcp 52.37.99.5:43: i/o timeout` (`whois.nic.io`) only when the lookup ran inside
the container; the same lookup from the host worked. `ip -6 route` is empty in the `upswarm`
container while the host has IPv6, so the container was left with the IPv4 path alone.

Same static test binary executed in both namespaces (so the code under test is identical):

| namespace | family | result |
|---|---|---|
| host | ipv6 | `ipv6 reached whois.nic.io:43 (4926 bytes)` |
| host | ipv4 | reached, 4926 bytes |
| container | ipv6 | `dial tcp6 [2600:1f13:101:c200:9b9b:7011:8bd8:a4af]:43: connect: network is unreachable`, **0.00 s** |
| container | ipv4 | reached, 4926 bytes |

Two conclusions: a pinned family turns a 10 s silence into an instant, explicit error, and the
original outage was intermittent (IPv4:43 answered again during the re-test) - which is exactly
why the new diagnostics matter more than the pin itself.

Registry sweep `TestLiveWhoisDefaults` (64 TLDs): **63 PASS / 1 FAIL**, and the failure is
registry-side, not ours - `.mz` returned
`nic.mz: querying whois.nic.mz: dial whois.nic.mz failed: dial tcp4 196.3.103.51:43: i/o timeout`,
`whois.nic.mz` has a single A record (no AAAA) and `196.3.103.51:43` is unreachable from the
host too. It also doubles as proof that the diagnostic pass runs on a real failure.

### 3.1 How the two namespaces were exercised

```bash
# host run
go test -c -o /tmp/expiry.test ./internal/expiry/
UP_LIVE_WHOIS=1 UP_LIVE_WHOIS_PIN=ipv6 /tmp/expiry.test -test.run TestLiveWhoisPinnedFamily -test.v

# same binary inside the namespace of the running app (alpine sidecar)
CGO_ENABLED=0 go test -c -o /tmp/expiry_static.test ./internal/expiry/
docker run --rm --network container:upswarm -v /tmp/expiry_static.test:/expiry.test:ro \
  -e UP_LIVE_WHOIS=1 -e UP_LIVE_WHOIS_PIN=ipv4 --entrypoint /expiry.test alpine:3.24 \
  -test.run TestLiveWhoisPinnedFamily -test.v
```

`--network container:upswarm` shares the network namespace of the running app, so the result is
what the app itself sees. The sidecar is musl-based, so the binary must be built with
`CGO_ENABLED=0`: the default `go test -c` links against glibc and alpine answers
`exec /expiry.test: no such file or directory`.

## 4. Implementation (as built)

| phase | content |
|---|---|
| 1 - dialer | `internal/expiry/netdial.go` + `netdial_test.go` (new): `registryDialer`, `registryNetwork`, `familyNetwork`, `dialRegistry`, `registryDialError`, `describeDialAttempts`, `unreachableNetwork`, budgets `registryDialProbeBudget`/`registryDialProbeTimeout` |
| 1 - plumbing | `WhoisClient.IPVersion` (`whois.go`), `newRDAPClient(timeout, ipVersion)` cloning `http.DefaultTransport` (`rdap.go`), `rebuildLocked` (`resolver.go`), test call sites (`rdap_lookup_test.go`) |
| 2a - backend | `models/domain.go`: `ExpiryIPVersionAuto/IPV4/IPV6`, `DefaultExpiryIPVersion`, `ExpiryIPVersions`, `NormalizeExpiryIPVersion`, `ValidExpiryIPVersion`, field `IPVersion json:"ip_version"` + default + Normalize + Validate; `models/security.go`: `SettingExpiryIPVersion = "expiry_ip_version"`; `services/settings.go`: read/write |
| 2a - frontend | `web/src/lib/types-platform.ts`; `web/src/views/admin/AdminExpiryView.vue` (Select + `setIPVersion`); `ipVersion`, `ipVersionHelp`, `ipVersionAuto`, `ipVersionIPV4`, `ipVersionIPV6` in all 7 locales |
| 3 - docs | `README.md` ("Network egress": TCP 43 / 443 / 53, the two traps, the IPv6 options, the verified sidecar recipe), `docker/.env.example`, `docs/api.md` |
| tests | `netdial_test.go` (loopback stub proving a pinned family is used *and* named in the error, the message/hints, `unreachableNetwork`), `whois_live_test.go` (`UP_LIVE_WHOIS_PIN`), `models/domain_test.go` |

## 5. Verification actually run

| command | result |
|---|---|
| `go build ./...` | ok |
| `go vet ./...` | ok |
| `gofmt -l internal` | empty |
| `go test -count=1 ./internal/expiry/... ./internal/models/... ./internal/services/...` | all ok |
| `go test ./...` | all packages ok |
| `cd web && npm run typecheck` | ok (vue-tsc, no errors) |
| `cd web && npm run check:i18n` | ok, 737 messages/locale, 616 used keys, 5159 compiled |
| `UP_LIVE_WHOIS=1 go test ./internal/expiry/ -run TestLiveWhoisDefaults -v` | 63/64 PASS, 83 s, 1 registry-side failure (`.mz`, see §3) |
| container-namespace sidecar (`docker run --network container:upswarm alpine:3.24` + `nc`) | see §3 table |
| `cd web && npm run build` | **cannot run here**: `node -v` 18.19.1 vs `engines.node >= 22.12.0` (vite 8 / rolldown needs `node:util.styleText`). Pre-existing environment issue, unrelated to the change |
| `cd web && npm run e2e:domain-expiry` | not run, same Node constraint |

## 6. Deviations, limitations, follow-ups

- **No `docker/docker-compose.ipv6.yml`.** `enable_ipv6` plus a ULA subnet was believed not to
  give the container IPv6 *transit* (Docker does not masquerade ULA traffic without daemon-level
  `experimental`/`ip6tables`), so an override file would have looked like a fix while still
  timing out. **Re-tested live on 2026-09-27 with Docker 29.8.1 and the claim is wrong for
  current engines:** a network created with `enable_ipv6: true` and a ULA subnet sets
  `net.ipv6.conf.all.forwarding=1` by itself, hands the container a global address plus a default
  IPv6 route, and masquerades it to the host's IPv6. In the real `up` container started from
  `docker/docker-compose.yml` the app's own `WhoisClient` (family `auto`) answered
  `whois.dns.pt` in 1.27 s (`dns.pt -> 2026-12-31T23:59:00Z`) with **no** `daemon.json` change.
  Both shipped compose files therefore declare the network inline (up, plus `mariadb` in the
  bundle) instead of shipping a separate override. The three routes stay documented in the
  README - daemon-level `ipv6`/`fixed-cidr-v6`/`ip6tables` (now only needed to give *other*
  containers and the default bridge IPv6), an IPv6-enabled compose network, and
  `network_mode: host`.
- **README wording corrected after the live re-test.** It first asserted that `whois.nic.io`
  answers "only over IPv6"; IPv4:43 worked again, so the text now states the verified fact
  (Docker networks are IPv4-only by default) and describes the trap generically.
- **Live tests stay opt-in** (`UP_LIVE_WHOIS`, `UP_LIVE_WHOIS_PIN`), so `go test ./...` never
  touches the network.
- **Follow-ups worth considering:** show the last dial error per registry in the admin page,
  allow a per-TLD family override, and probe IPv6 egress once at startup to warn early.

---

## 7. Reverted (2026-09-26, later the same day)

Everything of §2a/§2b above (the `expiry_ip_version` setting, the `tcp4`/`tcp6` pin in
`internal/expiry/netdial.go`, the "IP family" control of *Admin > TLD/SSL expiration*, the
`expiry.ipVersion*` strings of the seven locales) was **removed**, together with the
`enable_ipv6: true` network of both compose files. See
`plans/2026-09-26-monitor-ip-family-alternation.md` for the change that supersedes this one.

Why: pinning a family never made an unreachable registry reachable - it only replaced a timeout
with `connect: network is unreachable` - and it could not work from a container anyway, because the
*network* (not the lookup) is what has no IPv6 route. The supported answer is `network_mode: host`,
now the documented recipe in `README.md` / `docker/.env.example`. The diagnostics of §2c
(`registryDialError`, `describeDialAttempts`, `unreachableNetwork`) stay; only the hint text now
points at `network_mode: host`. The orphan `expiry_ip_version` row of an existing installation is
simply ignored (no migration).

The probe side went the other way: `config.ip_family` on a monitor rotates IPv4/IPv6 between
executions only when it is set to `alternate` (the opt-in, see
`plans/2026-09-26-monitor-ip-family-alternation.md`; the default is `auto`, the dialer's own happy
eyeballs), where a real choice exists.
