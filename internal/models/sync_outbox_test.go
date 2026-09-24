package models

import (
	"encoding/json"
	"testing"
	"time"
)

// TestOutboxToChangeCarriesTheVersionTimestamp pins the timestamp the merge
// compares.
//
// The insert time of the outbox row is always slightly LATER than the update it
// describes. A node that published the insert time would compare its own row's
// updated_at against a peer's outbox insert time — two different quantities for
// the same change — so BOTH nodes would conclude that the other is newer and each
// would adopt the other's change. Measured in the lab before this fix: the two
// nodes ended a concurrent edit with different identities and opposite conflict
// verdicts.
func TestOutboxToChangeCarriesTheVersionTimestamp(t *testing.T) {
	inserted := time.Date(2026, 1, 1, 12, 0, 0, 500_000_000, time.UTC)
	version := inserted.Add(-2 * time.Millisecond)

	change := OutboxToChange(SyncOutbox{
		ID:           7,
		Entity:       EntityMonitor,
		UUID:         "11111111-1111-4111-8111-111111111111",
		Action:       ActionUpsert,
		OriginNodeID: "up-node-2",
		Revision:     4,
		Payload:      `{"uuid":"11111111-1111-4111-8111-111111111111"}`,
		PayloadHash:  "abc",
		CreatedAt:    inserted,
		UpdatedAt:    version,
	})

	if !change.UpdatedAt.Equal(version) {
		t.Fatalf("UpdatedAt = %s, want the version timestamp %s", change.UpdatedAt, version)
	}
	if change.ID != 7 || change.Revision != 4 || change.Entity != EntityMonitor {
		t.Fatalf("the change lost part of its identity: %+v", change)
	}
	if change.OriginNodeID != "up-node-2" {
		t.Fatalf("OriginNodeID = %q, want the editor", change.OriginNodeID)
	}
	if string(change.Payload) != `{"uuid":"11111111-1111-4111-8111-111111111111"}` || change.PayloadHash != "abc" {
		t.Fatalf("the change lost its payload: %+v", change)
	}
}

// TestOutboxToChangeFallsBackToInsertTime keeps the rows written before the version
// column existed usable: they are still delivered, they just carry the only
// timestamp they have.
func TestOutboxToChangeFallsBackToInsertTime(t *testing.T) {
	inserted := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	change := OutboxToChange(SyncOutbox{
		ID: 1, Entity: EntityMonitor, UUID: "u", Action: ActionUpsert, CreatedAt: inserted,
	})
	if !change.UpdatedAt.Equal(inserted) {
		t.Fatalf("UpdatedAt = %s, want the insert time %s", change.UpdatedAt, inserted)
	}
}

// TestOutboxToChangeDropsAnInvalidPayload is the sender-side half of the poison-pill
// guard.
//
// A json.RawMessage that cannot be marshalled fails the WHOLE response, so one corrupt
// outbox row would make the node unservable to every peer. Handing it over as absent
// instead keeps the batch flowing, and the receiver contains that single change as a
// dead letter.
func TestOutboxToChangeDropsAnInvalidPayload(t *testing.T) {
	change := OutboxToChange(SyncOutbox{
		ID: 3, Entity: EntityMonitor, UUID: "u", Action: ActionUpsert,
		Payload: `{"uuid": `, CreatedAt: time.Now().UTC(),
	})
	if change.Payload != nil {
		t.Fatalf("an invalid payload must not be forwarded, got %s", change.Payload)
	}
	// The whole batch has to stay marshallable, which is the point of the guard.
	if _, err := json.Marshal([]SyncChangePayload{change}); err != nil {
		t.Fatalf("a batch carrying the change must still marshal: %v", err)
	}
}

// TestOutboxToChangeKeepsAValidPayload is its counterpart: the guard must not throw
// away well-formed payloads.
func TestOutboxToChangeKeepsAValidPayload(t *testing.T) {
	body := `{"uuid":"u","name":"api"}`
	change := OutboxToChange(SyncOutbox{
		ID: 4, Entity: EntityMonitor, UUID: "u", Action: ActionUpsert,
		Payload: body, CreatedAt: time.Now().UTC(),
	})
	if string(change.Payload) != body {
		t.Fatalf("Payload = %s, want %s", change.Payload, body)
	}
}

// TestOutboxToChangeDeleteCarriesNoPayload pins that a tombstone hands the apply
// path nothing but its identity.
func TestOutboxToChangeDeleteCarriesNoPayload(t *testing.T) {
	change := OutboxToChange(SyncOutbox{
		ID: 9, Entity: EntityMonitor, UUID: "u", Action: ActionDelete,
		Revision: 2, CreatedAt: time.Now().UTC(),
	})
	if change.Payload != nil {
		t.Fatalf("expected no payload on a delete, got %s", change.Payload)
	}
	if change.Action != ActionDelete {
		t.Fatalf("Action = %q, want %q", change.Action, ActionDelete)
	}
}
