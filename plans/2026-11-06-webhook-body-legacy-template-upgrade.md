# Plan - Heal the stored webhook body default and make its refusal actionable

> Task: a webhook channel keeps failing with `422 Failed to parse request body`
> because the body template it stores is the *default of an older release*, which
> interpolates a check detail raw inside a quoted string. Repair the stored
> default, refuse a broken template when it is saved, and make the delivery-log
> error name the value at fault.

## 1. Context

A keyword monitor renders its detail as Go quoted text (`keyword "hello"
matched`, `internal/checkers/http.go:205`). The default block of a release older
than `7906caf` wrote that detail into the payload as `"message": "{{.Message}}"`,
so the rendered body stops being JSON as soon as the value carries a quote, a
backslash or a newline, and a target that parses the body drops the alert.

The guard added in `7906caf` (`checkWebhookBody`) is correct and stays. What was
missing is on both sides of it:

- **On disk.** `Notification.Normalize()` (`internal/models/notification.go`)
  fills `DefaultWebhookBodyTemplate` only when the field is EMPTY, and nothing
  applies it on the read path, so a channel created before the fix keeps the old
  block forever - the operator has to know to clear the field. Six historical
  spellings of the block exist between `f2a7b85` and `3fa6062`; the three oldest
  still name `{{.MonitorGroups}}`, a field that no longer exists, so they do not
  merely break on a quoted value: they fail to render at all.
- **In the log.** The refusal said *the rendered webhook body is not valid JSON*
  and nothing else. A rendered body is one long line with sixteen values, so the
  operator is left to bisect the template by hand.

## 2. Goal

- A channel that stored a **shipped** default delivers again after the upgrade
  with no action from the operator (read path + save path + boot pass).
- A template the operator **wrote** is never rewritten: Up cannot tell which of
  its lines were meant as JSON, so guessing would be worse than refusing.
- A broken hand-written template is refused **when it is saved** with a 400 that
  names the reason, and - if it reaches the send path anyway (a channel applied
  from a peer, a row edited in the database) - the delivery log names the byte
## 3. Steps

1. `internal/models/notification.go`
   - `legacyWebhookBodyTemplates`: the six historical snapshots, each commented
     with the commit it shipped in.
   - `UpgradeWebhookBodyTemplate`: returns `DefaultWebhookBodyTemplate` when the
     input is one of them (compared after `canonicalBodyTemplate`, which strips
     CRLF, indentation, blank lines and trailing spaces), else the input verbatim.
     An empty template is returned untouched: `Normalize` owns that case.
   - `Normalize` upgrades **before** the empty check (write path).
   - `AfterFind` upgrades on every read, mirroring `MonitorTemplate.AfterFind`, so
     the UI, the *Test* button, the dispatch and the sync payload heal before the
     backfill has rewritten the row.
2. `internal/database/notification_body.go` - `BackfillWebhookBodyTemplates`,
   modelled on `template_auth.go`: read `notifications WHERE type = 'webhook'`
   raw (the model hook would heal the value in memory and the pass would compare
   it with itself), replace only a stored default, never advance the revision.
   Wired in `cmd/server/main.go` after the `template_auth` step, reusing
   `boot.PhaseBackfilling`.
3. `internal/notify/body_template.go` - `BodyTemplateProblem(contentType,
   template)`: renders the template with `webhookProbeMessage` (every field set,
   every free-text one carrying a quote, a backslash, a tab and a newline) and
   runs `checkWebhookBody` on the result. The probe lives in `notify` because it
   needs the template helpers, and `notify` already imports `models`.
4. `internal/services/notification.go` - `validateChannel` = `Validate` + the
   probe, used by `Create` and `Update`. The sync apply path is deliberately left
   out: a peer's channel is stored as it was sent.
5. `internal/notify/engine.go` - `checkWebhookBody` reports the `json.SyntaxError`
   offset and a bounded, single-line excerpt (`describeBodyError`,
   `bodyExcerpt`).
6. Tests: `internal/models/notification_test.go` (upgrade table over the
   snapshots, CRLF/no-op cases, `AfterFind`, `Normalize`),
   `internal/database/notification_body_test.go` (upgrade, idempotency, custom
   template, other type, empty and corrupt column),
   `internal/notify/notify_test.go` (`BodyTemplateProblem` table, the enriched
   error).
7. `docs/notifications.md` - §2 *Escaping* (what heals itself, what is refused,
   when), §6 delivery-log row + the save-time 400 row.

## 4. Risks

- **A hand-written template that only resembles the default is not upgraded**,
  and is refused on the next save. Mitigation: the 400 names the byte and the
  fix, the dialog prefills the current default, and clearing the field restores
  it; the delivery log explains the same failure for a channel that is never
  saved again.
- **The save-time probe is stricter than reality**: it refuses a template that
  happens to be valid JSON only for the values of one event (e.g. a value that
  never carries a quote is written raw). That is the same rule the send path
  applies, and a quote is the normal case for a keyword check, so the strictness
  is the point.
- **The probe exercises the hardware fields too**, so a template that renders
  them raw is refused even by an operator who only watches monitors - correct,
  because one channel serves both kinds of alert.
- **The backfill rewrites the whole `config` column**: a malformed column aborts
  the boot step loudly (as `BackfillTemplateAuth` does) instead of skipping the
  row silently. The pass is idempotent and does not advance the revision, so a
  cluster converges without a change nobody made.
- **An operator who deliberately wants a non-JSON body keeps it**: the probe is
  only applied to a JSON content type, exactly like the send path.

## 5. Verification

```bash
gofmt -l internal/
go vet ./...
go build ./...
go test ./...     # full suite, EXIT=0
go test ./internal/models/ -run 'WebhookBody' -v
go test ./internal/notify/ -run 'BodyTemplateProblem|CheckWebhookBody|DefaultWebhookTemplate' -v
go test ./internal/database/ -run 'UpgradeWebhookBody' -v
```

Manual check: a webhook channel whose stored template is the old block renders
the escaped payload after a reload; saving a template with
`"message": "{{.Message}}"` answers 400 and names the byte.

  and shows a short excerpt of the body.
- Nothing changes for a `content_type` that is not JSON, and nothing changes for
  a channel whose template is already correct.
