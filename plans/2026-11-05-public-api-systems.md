# Public API for the hardware monitoring (systems)

## Context

Hardware monitoring (the Beszel agent hub) is complete on the dashboard side but
not on the API side. `internal/handlers/router_admin.go:83-103` registers
`/api/systems*` inside the group protected by `middleware.RequireSession`
(`router_admin.go:12-15`), and the token group `/api/v1` (`router.go:112-128`)
carries monitors, status pages and the on-demand analysis but **no** systems
route. A `read` scoped token can therefore list monitors and cannot list
machines, which is the gap reported as "how do I get a list of systems like I get
monitors?".

The service layer needs nothing new: `SystemService.List` (`system_write.go:20`),
`Get` (`:47`), `Latest` (`system_stats.go:159`), `History`
(`system_history.go:21`, cluster aware: it reads the samples from the owning
peer in federated mode), `Series` (`system_stats.go:34`, downsampled to 240
points), `Containers` (`:83`) and `Smart` (`:122`) already produce the payloads
the dashboard renders. The window is already clamped by
`models.NormalizeSystemChartWindow` to `{1, 6, 24, 168, 720}` hours
(`system_maintenance.go:37-57`), so "bounded hours" is a documentation fact
rather than new code.

The documentation is the other half of the bug: `docs/api.md` never mentions
`/api/systems`, `docs/public-api.md` never says that hardware data is
dashboard-only, and the four `ERR_SYSTEM_*` codes are missing from the error
catalogue of `docs/api.md` §11.

## Goal

Expose the read half of the hardware monitoring through the bearer-token API,
with the same payloads the dashboard consumes, without leaking the operator
tuning (alert rules and their latches) or the one-time secrets (agent token,
install line, agent KEY), and document it next to `GET /api/v1/monitors`.

| Method | Path | Answer |
|---|---|---|
| GET | `/api/v1/systems` | `[]SystemView` sorted by `name`, alerts and one-time secrets stripped |
| GET | `/api/v1/systems/overview` | `[]SystemMetric`, the last sample of every machine |
| GET | `/api/v1/systems/:id` | `SystemView` |
| GET | `/api/v1/systems/:id/history?hours=24` | `SystemHistory` (row + charts + containers + SMART) |
| GET | `/api/v1/systems/:id/metrics?hours=24` | `SystemPoint[]` (at most 240 downsampled points) |
| GET | `/api/v1/systems/:id/containers` | `SystemContainer[]` |
| GET | `/api/v1/systems/:id/smart` | `{smart, smart_complete, smart_at}` |

All seven require the `read` scope. The write half (create, update, delete,
rotate token, alert rules, SMART refresh, the global containers/SMART listings)
stays dashboard-only, and the documentation says so.

## Steps

1. `internal/handlers/publicapi_systems.go` (new): `publicSystem` strips
   `Alerts`, `Token`, `InstallCommand` and `AgentKey` from a `SystemView`
   (the same idea as `redactMonitor` in `publicapi.go:19`); the seven handlers
   reuse `requireSystems` (`systems.go:14`) so a process without the hardware
   service answers the same `503` as the dashboard; `metrics`, `containers` and
   `smart` are projections of one `SystemService.History` call, so they are
   cluster aware and can never disagree with the page.
2. `internal/handlers/router.go`: register the seven routes in the `v1` group
   with `middleware.RequireToken(h.Tokens, models.ScopeRead)`.
3. `internal/handlers/systems_routes_test.go`: `TestPublicSystemRoutesRegistered`
   registers the whole tree through `Container.Register` (a gin wildcard clash
   panics at registration time, which a build alone does not catch).
4. `internal/handlers/publicapi_systems_test.go` (new, no database): the
   redaction contract and the `503` of an unwired service.
5. `docs/public-api.md`: `read` scope row, a `### Systems` section with the
   endpoint table, a sample payload, the window rule, the redaction rule, the
   federated `remote` note and a recipe.
6. `docs/api.md`: the `/api/v1/systems*` rows in §1 and §10, plus the four
   `ERR_SYSTEM_*` codes that §11 was missing.
7. `docs/hardware-monitoring.md` §6: mark which system routes are also on the
   public API, so the two references do not diverge again.

## Risks

- **Payload size.** `/metrics` is bounded by the 240-point downsample and the
  five accepted windows; the listing is one row per machine and the responses
  are gzip-compressed by the middleware. No new knob.
- **Federated mode.** `Series`, `Containers` and `Smart` read local rows only;
  routing the three projections through `History` keeps a non-owner node
  answering with the owner's samples instead of an empty chart.
  `GET /overview` stays local, exactly like the dashboard cards, and the
  documentation says it.
- **Information exposure.** `last_error`, `token_prefix` and `fingerprint` stay
  in the payload on purpose (diagnostics, not credentials); the alert rules
  (thresholds and latches) and the one-time `token`/`install_command`/`agent_key`
  are removed. Tightening further later does not break the contract.
- **The `503` code.** `requireSystems` answers `503` with `ERR_VALIDATION`
  today; reusing it keeps the dashboard behaviour untouched and avoids a new
  code in seven locale files for a wording nit. The documentation spells the
  answer out verbatim.
- **No write scope**, so no CSRF or idempotency concern and no scheduler or
  agent interaction is involved.
