# Plan - Domain expiry is `null` (not `0001-01-01`) when no date was read

> Task: the API returned `0001-01-01T00:00:00Z` for the domain expiration of
> domains whose lookup read no date (e.g. `saranszky.com`, `saranszky.com.br`,
> `iga.com.you`). Unknown/expired-without-a-date must expose `null`.

## 1. Goal

The decorated `domain` object of a monitor (and every other `DomainInfo`
payload) exposes `expires_at: null` when the last lookup obtained no date
(`status` = `not_found` | `unsupported` | `error`), instead of Go's zero time
rendered as `"0001-01-01T00:00:00Z"`.

## 2. Root cause

`models.DomainInfo.ExpiresAt` is a non-pointer `time.Time` with
`json:"expires_at"`. `encoding/json` renders the zero value as
`"0001-01-01T00:00:00Z"`, and `omitempty` cannot help (a struct is never
"empty" for the encoder). Every non-ok observation (`rdap.go` not-found/error,
`resolver.go` unsupported/not-found) leaves the field at zero, so the bogus date
reached the UI, which painted it as a real date.

## 3. Decisions

- Fix only the wire shape, not the internal type: `DomainInfo` keeps
  `ExpiresAt time.Time`, so `HasExpiry`, `Expired`, the equality comparisons and
  the alert planner are untouched. This mirrors the existing
  `Heartbeat.MarshalJSON` / `HeartbeatSummary.MarshalJSON` precedent (internal
  value in, readable value out).
- Expose `null` (not an omitted key): the field is always present today, so the
  TS interface becomes `string | null` and no consumer breaks.
- `null` is emitted only for a genuinely zero date; a registry that returns a
  past (expired) date keeps it.

## 4. Changes

| file | change |
|---|---|
| `internal/models/domain.go` | `DomainInfo.MarshalJSON` renders a zero `ExpiresAt` as `null` (UTC RFC3339 otherwise); doc comment updated; `encoding/json` import added |
| `internal/models/domain_test.go` | `TestDomainInfoMarshalJSON`: not_found -> `"expires_at":null`, ok -> RFC3339 date, decorated `Monitor` does not leak `0001-01-01` |
| `web/src/lib/types.ts` | `DomainInfo.expires_at` becomes `string \| null` (`formatDateTime` already renders `—`) |
| `docs/api.md` | documents the `null` for `not_found`/`unsupported`/`error` |

## 5. Verification

| command | result |
|---|---|
| `gofmt -l internal/models/domain.go internal/models/domain_test.go` | empty |
| `go build ./...` | ok |
| `go vet ./internal/models/...` | ok |
| `go test ./internal/...` | all packages ok |
| `npm run typecheck` (web, node 22) | ok |

Go is not installed in the work container; the commands ran inside
`golang:1.27.1-alpine` with a persistent module/build cache.

## 6. Out of scope

- `MonitorDomain.ExpiresAt` also stays a `time.Time`: the row is never
  serialized directly (only through `Info()`), so no change is needed.
- `days_left` for a non-ok status stays `0`; the UI already ignores it there.
