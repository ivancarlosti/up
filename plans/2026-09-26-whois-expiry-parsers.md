# Plan - WHOIS expiry parsers (ISO 8601, built-in seed set, CRUD, reset, own registry server)

> **Archived plan** — task: *WHOIS expiry parsers (ISO 8601, built-in seed set, CRUD, reset,
> own registry server)*, worked and shipped on **2026-09-26** (commit `170404e3`).
> Kept in `plans/` for future review because §3 and §7.2 carry the live port-43 probe results
> (observed line, regex, Go layout, probe domain per TLD) that the shipped code no longer
> contains. Folder tracked in git (the `/plans/` exclusion was removed on 2026-10-11) and
> excluded from the Docker build context (`.dockerignore`).

This document records the full plan and the verified research data so the context is not lost
between sessions.

## 1. Goal

1. Treat **ISO 8601** as the documented/recommended value of a parser's `date_layouts`.
2. **Pre-fill** `whois_parsers` with the built-in rules of the popular TLDs that have no RDAP.
3. Keep full CRUD over parsers in Admin > TLD/SSL expiration (add / edit / remove already exist).
4. Add a **Reset WHOIS parsers** button with a confirmation alert that erases the operator's
   customizations and restores the built-in list.
5. Prefer **each TLD's own WHOIS server** (from the IANA referral) over always asking
   `whois.iana.org`.
6. Document everything and cover it with tests.

## 2. Decisions taken

- **Verified-only seed set.** Only TLDs whose registry WHOIS actually returns an expiration date
  are seeded. Registries that hide the date are *not* seeded, because a seeded rule that can never
  match would report `error` for those monitors forever.
- **One source of truth.** The curated registry table lives in `internal/models/whois_defaults.go`
  and feeds both the seed data (`database.Seed`) and the server lookup (`models.WhoisServerFor`),
  which `expiry.WhoisClient.Server` consults before falling back to `whois.iana.org`.
- **Seed only when the table is empty**, so an operator's edits/deletions survive restarts and are
  only undone by the explicit Reset button.
- **No database migration.** `whois_parsers` already has `server`, `date_layouts` and `note`.
- Reset is destructive by design: a `ConfirmDialog` warns that every customization is erased.

## 3. Research data (verified by live port-43 lookups)

### 3.1 Pool size (IANA root zone vs RDAP bootstrap)

| metric | value |
|---|---|
| TLDs in the IANA root zone | 1438 (1287 ASCII, 151 IDN) |
| TLDs covered by the RDAP bootstrap (`data.iana.org/rdap/dns.json`) | 1107 ASCII |
| **ASCII TLDs with no RDAP at all** | **180** |
| IDN TLDs with no RDAP | 56 |

So "the 50 most used TLDs without RDAP" is a subset of those 180.

### 3.2 Registries confirmed to publish an expiry (seeded)

Probed with a real registered domain per TLD (registry-owned domain or the TLD's own NS name).

| TLD | own WHOIS server | observed line | capture | Go layout(s) |
|---|---|---|---|---|
| io | whois.nic.io | `Registry Expiry Date: 2026-11-22T01:38:41Z` | `^\s*Registry Expiry Date:\s*(\S+)` | ISO |
| us | whois.nic.us | `Registry Expiry Date: ...Z` | same | ISO |
| me | whois.nic.me | `Registry Expiry Date: 2027-04-29T17:53:03Z` | same | ISO |
| ie | (IANA referral) | `Registry Expiry Date: 2027-01-01T14:59:49Z` | same | ISO |
| my | whois.mynic.my | `Registry Expiry Date: 2033-01-08T16:00:00.000Z` | same | ISO w/ millis |
| hr | whois.dns.hr | `Registrar Registration Expiration Date: 2050-11-15T23:00:00Z` | `Expiration Date:\s*(\S+)` | ISO |
| ru | whois.tcinet.ru | `paid-till: 2027-07-06T21:00:00Z` | `^paid-till:\s*(\S+)` | ISO |
| ug | whois.co.ug | `Expires On: 2030-01-02` (also `Privacy Expiry:`) | `^(?:Privacy Expiry|Expires On):\s*(\S+)` | ISO |
| cn | whois.cnnic.cn | `Expiration Time: 2028-03-10 19:06:34` | `^Expiration Time:\s*(.+)` | `2006-01-02 15:04:05` |
| cl | whois.nic.cl | `Expiration date: 2030-02-08 21:00:00 CLST` | `^Expiration date:\s*(\S+ \S+ \S+)` | `2006-01-02 15:04:05 MST` |
| pt | whois.dns.pt | `Expiration Date: 05/03/2027 16:23:00` | `^Expiration Date:\s*(\S+ \S+)` | `02/01/2006 15:04:05` |
| rs | whois.rnids.rs | `Expiration date: 10.03.2108 12:00:00` | `^Expiration date:\s*(.+)` | `02.01.2006 15:04:05` |
| hk | whois.hkirc.hk | `Expiry Date: 16-11-2035` | `^Expiry Date:\s*(\S+)` | `02-01-2006` |
| tr | whois.trabis.gov.tr | `Expires on..............: 2026-Aug-25.` | `^Expires on\.*:\s*([^.]+)` | `2006-Jan-02` (year first!) |
| it | whois.nic.it | `Expire Date: 2026-10-10` | `^Expire Date:\s*(\S+)` | `2006-01-02` |
| se | whois.iis.se | `expires: 2031-03-09` | `^expires:\s*(\S+)` | `2006-01-02` |
| dk | whois.punktum.dk | `Expires: 2027-03-31` | `^Expires:\s*(\S+)` | `2006-01-02` |
| lt | whois.domreg.lt | `Expires: 2027-01-08` | `^Expires:\s*(\S+)` | `2006-01-02` |
| ee | whois.tld.ee | `expire: 2030-02-05` | `^expire:\s*(\S+)` | `2006-01-02` |
| mx | whois.mx | `Expiration Date: 2027-01-14` | `^Expiration Date:\s*(\S+)` | `2006-01-02` |
| pk | whois.pknic.net.pk | `Expiry Date: 2026-12-29` | `^Expiry Date:\s*(\S+)` | `2006-01-02` |
| by | whois.cctld.by | `Expiration Date: 2028-06-18` | `^Expiration Date:\s*(\S+)` | `2006-01-02` |
| am | whois.amnic.net | `Expires: 2026-10-19` | `^Expires:\s*(\S+)` | `2006-01-02` |

### 3.3 Registries verified NOT to publish an expiry (excluded on purpose)

`de` (denic.de -> `Status: connect`, no date), `ch` (switch.ch), `jp` (jprs.co.jp), `be`,
`at`, `nz`, `hu`, `ro`, `il`, `eu`, `sk`, `bg`, `lv`, `lu`, `kz`, `es`. Also unreachable from a
plain container (server never answers on 43): `vn`, `za`, `gr`, `ba`, `mt`, `cy`, `al`, `ke`,
`ng`, `az`, `ge`, `md`, `ws`, `bd`, `lk`, `ph`, `eg`.

### 3.4 Discovery probe (step 2 of the implementation)

A throwaway Python probe (`/tmp/probe_whois.py`) walks every ASCII TLD without RDAP:

1. ask `whois.iana.org` for the TLD -> `whois:` server + `nserver:` list;
2. build a candidate domain per TLD from the TLD's own NS names (`a.ns.<tld>` is by definition a
   registered domain of that TLD) and from `nic.<tld>` / `registry.<tld>` / `dns.<tld>`;
3. query the registry server and keep the TLD only if a line matches
   `(?i)^[^:]*(expir|paid-till|expire)[^:]*[:.]\s*(.+)$`.

Output is a TSV `tld / server / domain / line`; the table of section 3.2 is extended with the
lines it found, and the permanent counterpart is `internal/models/whois_defaults_live_test.go`
(`UP_LIVE_WHOIS=1`), so "verified" is reproducible instead of a one-off claim.

## 4. Implementation phases

### Phase 1 - ISO 8601 in `date_layouts`

- `internal/models/whois_defaults.go`: `DefaultWhoisDateLayouts = "2006-01-02T15:04:05Z07:00;2006-01-02"`.
- `internal/expiry/whois.go`: `builtinDateLayouts` keeps ISO 8601 first and gains
  `2006-01-02T15:04:05.000Z07:00` (`.my`), `2006-01-02 15:04:05 MST` (`.cl`),
  `02.01.2006 15:04:05` (`.rs`), `02-01-2006` (`.hk`).
- `AdminExpiryView.vue`: `blankParser()` prefills `date_layouts` with the ISO constant and the
  input placeholder becomes `2006-01-02T15:04:05Z07:00`.
- `expiry.layoutsHelp` (7 locales) explains ISO 8601 and shows the reference layout mapping.

### Phase 2 - Built-in parsers

- `internal/models/whois_defaults.go`: the curated table (`whoisRegistryDefaults`),
  `DefaultWhoisParsers()` (fresh copy) and `WhoisServerFor(tld)`.
- `internal/database/seed.go`: insert the defaults when `whois_parsers` is empty.

### Phase 3 - Prefer the TLD's own WHOIS server

- `internal/expiry/whois.go`: `Server()` -> `models.WhoisServerFor(tldOf(domain))` first, IANA
  referral only as fallback.
- `internal/expiry/resolver.go`: unchanged (it already prefers `parser.Server`, which the seed now
  fills), but the short-circuit avoids one IANA round trip per TLD per run.
- `expiry.serverHelp` (7 locales) documents the fallback order.

### Phase 4 - Reset button

- `internal/services/whois.go`: `Reset(ctx)` in one transaction (delete all, insert defaults).
- `internal/handlers/expiry.go`: `resetWhoisParsers` handler.
- `internal/handlers/router_admin.go`: `POST /api/admin/expiry/whois-parsers/reset`.
- `web/src/lib/api-admin.ts`: `resetWhoisParsers()`.
- `AdminExpiryView.vue`: a `Reset WHOIS parsers` button in the parsers card header, a second
  `ConfirmDialog` warning that all customizations are erased, and a parsers search filter.
- 7 locales: `expiry.resetParsers`, `resetParsersTitle`, `resetParsersWarning`,
  `resetParsersConfirm`, `resetParsersDone`, `parserSearchPlaceholder`, `parsersCount`.

### Phase 5 - Tests, docs

- `internal/models/whois_defaults_test.go`: unique TLDs, every rule valid, every server set and
  never `whois.iana.org`, ISO 8601 first in the layouts, `WhoisServerFor` round-trip.
- `internal/models/whois_defaults_live_test.go`: `UP_LIVE_WHOIS=1` only; asserts every seeded rule
  extracts a date from the live registry.
- `internal/expiry/whois_test.go`: referral parsing, ISO 8601 layouts, built-in layout table.
- `internal/handlers/expiry_routes_test.go`: the reset route is registered.
- Docs: `docs/api.md`, `docs/monitors.md`, `docs/database.md`, `README.md`.

## 5. Verification commands

```bash
go build ./... && go vet ./... && go test ./...
cd web && npm run check:i18n && npm run typecheck && npm run build
```

## 6. Risks / notes

- A registry may stop publishing the date after a rule is seeded: the monitor then reports
  "no date" (parse error). The operator can disable/delete the row or hit Reset.
- `whois_parsers` is documented as local (no cluster sync entity), so the seed runs per node and
  the empty-table guard keeps it idempotent.
- The seed count is whatever verification supports; it is not padded to reach a round 50.

## 7. As-built record (what actually shipped)

### 7.1 Phase status

| phase | state | artifacts |
|---|---|---|
| 1 - ISO 8601 | done | `models.DefaultWhoisDateLayouts`, `expiry.builtinDateLayouts`, `blankParser()` + input placeholder, `expiry.layoutsHelp` in 7 locales |
| 2 - built-in parsers | done | `internal/models/whois_defaults.go` (64 registries), seed in `internal/database/seed.go` (empty table guard) |
| 3 - own WHOIS server | done | `WhoisClient.Server` consults `models.WhoisServerFor` first, `expiry.serverHelp` in 7 locales |
| 4 - CRUD + reset | done | `WhoisParserService.Reset`, `POST /api/admin/expiry/whois-parsers/reset`, `adminApi.resetWhoisParsers()`, destructive button + second `ConfirmDialog` + search box + note column, 8 new i18n keys x 7 locales |
| 5 - tests + docs | done | `models/whois_defaults_test.go`, `expiry/whois_test.go`, `expiry/whois_live_test.go`, reset route assertion, `docs/{api,monitors,database}.md`, `README.md` |

### 7.2 The final table: 64 registries (all verified live)

| group | layout used | TLDs |
|---|---|---|
| ISO 8601 `Registry Expiry Date` / `paid-till` | `2006-01-02T15:04:05Z07:00` | io, us, me, ie, ru, hr, ug, ac, sh, su, sx, vc, af, ag, ci, do, gn, ki, kn, mn, mr, mz, pr, sb, sc, so, td, tl |
| ISO 8601 with fractional seconds | `2006-01-02T15:04:05.000Z07:00` | my, bf, bh, bi, bj, co, dm, gh, gl, la |
| date and time | `2006-01-02 15:04:05` / `... MST` / `02/01/2006 15:04:05` / `02.01.2006 15:04:05` | cn, cl, pt, rs, im |
| short month names | `2006-Jan-02`, `02-Jan-2006`, `02-Jan-2006 15:04:05` | tr, edu, bn |
| day first, numeric separators | `02-01-2006`, `02.01.2006` | hk, ax, ls, mk, ve |
| plain dates | `2006-01-02` | it, se, dk, lt, mx, by, am, pk, ee, mc, st, tg |

Excluded on purpose: every registry that does not publish the date (`de`, `ch`, `jp`, `be`, `at`, `nz`, `hu`, `ro`, `il`, `eu`, `sk`, `bg`, `lv`, `lu`, `kz`, `es`, `dz` - `.dz` answers `Expiry Date: N/A`), and the servers that never answer
from this network (`vn`, `za`, `gr`, `ba`, `mt`, `cy`, `al`, `ke`, `ng`, `az`, `ge`, `md`, `ws`, `bd`, `lk`, `ph`, `eg`).

Two rules were corrected during the live verification of the table:

- `.ug` answers `Expires On: 2030-01-02` (the older sample had `Privacy Expiry:`), so the
  expression accepts both spellings;
- `.tr` writes `2026-Aug-25`, i.e. **year first**: the layout is `2006-Jan-02`, not
  `02-Jan-2006` (the probe's generic `02-Jan-2006` reading would have been wrong).

### 7.3 Verification actually run

| command | result |
|---|---|
| `go build ./...` | ok |
| `go vet ./internal/...` | ok |
| `gofmt -l internal` | empty |
| `go test ./internal/...` | all packages ok |
| `UP_LIVE_WHOIS=1 go test ./internal/expiry/ -run TestLiveWhoisDefaults -v` | **64/64 sub-tests PASS**, 77 s, 0 failures |
| `cd web && npm run check:i18n` | ok, 5124 messages compiled, 611 used keys x 7 locales |
| `cd web && npm run typecheck` | ok (vue-tsc, no errors) |
| `cd web && npm run build` | **cannot run in this container**: `node -v` is 18.19.1 while `web/package.json` declares `engines.node >= 22.12.0` (vite 8 needs `node:util.styleText`). Pre-existing environment issue, unrelated to these changes. |

### 7.4 Deviations from the original sketch

- The live test lives in `internal/expiry/whois_live_test.go` (not `internal/models/`): a test in
  package `models` cannot import `expiry` (import cycle) and the probe domains belong to the lookup
  path. It uses `UP_LIVE_WHOIS=1` and keeps the probe domain of every rule in `probeDomains`.
- The seed is **64 rows**, more than the "~50" target: every extra row is a registry that was
  verified the same way, so the coverage is larger without inventing unverified rules.
- `expiry.parsersCount`, `noParsersMatch`, `parserSearchPlaceholder` and the five `resetParsers*`
  keys were added to all 7 locales (the parsers table can now be filtered and the note of a row -
  "built-in default" vs a custom description - is visible under the TLD).
