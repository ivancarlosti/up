package services

import (
	"testing"
	"time"

	"github.com/ivancarlosti/up/internal/models"
)

// TestSnapshotChangeDescribesTombstones pins the rule that lets a full resync
// repair a MISSED DELETION.
//
// A snapshot built from the live rows alone could not mention a row that no longer
// exists, so a node that never received the deletion would keep it for ever: the
// checksums would disagree on every pass and no pass could fix it.
func TestSnapshotChangeDescribesTombstones(t *testing.T) {
	deletedAt := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	row := models.SyncObject{
		UUID:         "11111111-1111-4111-8111-111111111111",
		Entity:       models.EntityMonitor,
		OriginNodeID: "up-node-2",
		Revision:     3,
		DeletedAt:    &deletedAt,
		UpdatedAt:    deletedAt,
	}

	change, ok := SnapshotChange(row)
	if !ok {
		t.Fatal("a tombstone must be describable, or a missed deletion can never be repaired")
	}
	if change.Action != models.ActionDelete {
		t.Fatalf("Action = %q, want %q", change.Action, models.ActionDelete)
	}
	if change.ID != 0 {
		t.Fatalf("ID = %d: a snapshot position is not an outbox position and must not move a cursor", change.ID)
	}
	if !change.UpdatedAt.Equal(row.UpdatedAt) || change.OriginNodeID != "up-node-2" || change.Revision != 3 {
		t.Fatalf("the tombstone lost part of its identity: %+v", change)
	}
}

// TestSnapshotChangeCarriesTheStoredPayload pins that a live identity is described
// by the payload stored beside it, which is what makes the snapshot possible for
// the entities that have no row of their own to render from (a relation).
func TestSnapshotChangeCarriesTheStoredPayload(t *testing.T) {
	row := models.SyncObject{
		UUID:         "u",
		Entity:       models.EntityMonitorGroupMember,
		OriginNodeID: "up-node-1",
		Revision:     2,
		Payload:      `{"monitor_uuid":"m","group_uuid":"g"}`,
		UpdatedAt:    time.Now().UTC(),
	}

	change, ok := SnapshotChange(row)
	if !ok {
		t.Fatal("a live identity with a payload must be describable")
	}
	if change.Action != models.ActionUpsert {
		t.Fatalf("Action = %q, want %q", change.Action, models.ActionUpsert)
	}
	if string(change.Payload) != row.Payload {
		t.Fatalf("Payload = %s, want %s", change.Payload, row.Payload)
	}
}

// TestSnapshotChangeRefusesToGuessADeletion is the safety rule: an identity without
// a payload is reported as undescribable, never as a deletion. A deletion is
// destructive, so guessing one from a missing field would destroy data on the
// receiver.
func TestSnapshotChangeRefusesToGuessADeletion(t *testing.T) {
	row := models.SyncObject{UUID: "u", Entity: models.EntityMonitor, Revision: 1}
	if change, ok := SnapshotChange(row); ok {
		t.Fatalf("a live identity with no payload must not be sent, got %+v", change)
	}
}
