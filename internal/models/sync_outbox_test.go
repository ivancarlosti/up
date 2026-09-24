package models

import (
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
