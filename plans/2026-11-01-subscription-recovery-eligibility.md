# Plan - Recovery mails only for subscribers who were told about the outage

> **Shipped plan** — task: *"the backup notification should only be delivered if
> the down period is longer than the notification delay; sub-10-minute outages are
> sending a bare 'back up' e-mail to everybody"*. Implemented on **2026-11-01**.
>
> Owner decision taken before coding: the recovery mail is only sent to the
> subscribers that received the outage mail, and a down mail that was already due
> when the monitor recovered is still delivered first — the recovery follows it
> instead of replacing it (variant **C**; the two alternatives were "literal time
> rule" and "up only after a delivered down", which would have silently dropped
> the owed down mail).

## 1. Goal

Make the `subscription_delay_minutes` of a status page mean what it says: an
outage shorter than the delay produces **no mail at all**, and a recovery ("back
up") mail never reaches a subscriber who was never told the monitor was down.

## 2. Root cause

`observePageTransition` queued the down mail with the gate that matters — it is
scheduled at `opened_at + delay` and cancelled if the incident closes first — and
then queued the `up` mail for **every active subscriber of the page** the moment
the incident closed (`createSubscriptionNotices(..., SubscriptionNoticeUp, at)`).
The `ClosedAt != nil` guard existed only on the down side, as the comment
"recovered before the delay elapsed" shows. So:

- a 2 minute blip produced a lone "back up" mail — and only that mail, which is
  precisely what makes it unreadable;
- the delay was effectively a filter on the down mail only, never on the pair;
- a subscriber who confirmed *during* an outage also received the recovery.

## 3. What the fix does

| Where | Change |
|---|---|
| `createRecoveryNotices` | **new**: queues the `up` mail only for the subscribers whose down mail was sent (or in flight), plus the ones whose down mail was still queued when the incident closed **but whose delay had already elapsed** (`scheduled_at <= closed_at`) |
| `createSubscriptionNotices` | takes an optional `subscriberIDs` filter (`nil` = every active subscriber, as before); the `(incident, subscriber, kind)` key keeps it idempotent |
| `outageMailStillOwed(closedAt, scheduledAt)` | **new, pure**: the down mail is delivered unless the incident closed before it was due. Replaces the blanket `ClosedAt != nil` skip |
| `recoveryMailActionFor(status)` | **new, pure**: `sent` → deliver, `pending`/`sending` → wait, anything else (or no row) → give up |
| `postponeRecoveryNotice` | **new**: keeps the `up` notice pending (not an attempt, no retry consumed) with `next_attempt_at = max(now+1m, down.scheduled_at, down.next_attempt_at)` |
| delivery gate | a `down` notice now passes when its delay had elapsed before the closure; an `up` notice consults its sibling `down` and delivers, waits or gives up |

Ordering is free: `RunDue` sorts by `scheduled_at ASC, id ASC` and the down mail is
always scheduled earlier, so an owed down mail leaves in the same pass as (or
before) its recovery. No schema change and no migration: the eligibility rule is
derived from the rows that already exist, so notices queued before the fix are
also handled (a stale `up` with a `skipped`/missing down is skipped with an
explicit reason).

A close that is **not** `up` (maintenance, pending, unknown) keeps its old
behaviour of cancelling the queued down mail outright: no recovery mail follows
those, and a late "down" with no resolution would be worse than silence.

## 4. Tests

| File | What it pins |
|---|---|
| `internal/services/statuspage_subscription_test.go` | `TestOutageMailStillOwed` (open incident, before due, at due, after due) and `TestRecoveryMailAction` (each sibling state) — the two decisions, without a database |
| `internal/services/statuspage_subscription_integration_test.go` | **new, opt-in** (`UP_SCRATCH_DSN`) `TestStatusPageSubscriptionRecoveryLive`: (1) a 2 minute blip queues no `up` and the down is skipped at delivery, (2) an outage past the delay queues the `up` at the recovery, (3) a subscriber that confirmed during the outage gets nothing, (4) an owed down mail keeps the `up` pending behind it and, when it dies, the `up` is skipped instead of delivered alone |

## 5. Validation

- `gofmt -l ./internal`, `go vet ./...`, `go build ./...`, `go test ./...` — clean.
- Live, against a throwaway MariaDB 11.4 (`UP_SCRATCH_DSN=…:3308`):
  `go test ./internal/services -run TestStatusPageSubscriptionRecoveryLive -count=2 -v` — pass.
- `cd web && npm run check:i18n && npm run typecheck` — clean (7 126 messages,
  864 used keys checked in every locale).

## 6. Documentation and copy

`docs/status-pages.md` §6 stated the old behaviour ("the down mail is cancelled and
only the recovery mail is sent") and is rewritten; the
`subscription_delay_minutes` help text in all seven locales now says that a
shorter outage sends no e-mail at all. The invariant is documented on
`models.SubscriptionIncident` / `models.SubscriptionNotice`.
