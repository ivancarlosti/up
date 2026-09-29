package boot

import (
	"testing"
	"time"

	"github.com/ivancarlosti/up/internal/i18n"
)

// TestSnapshotStatusAndDatabase pins the two fields the container health check
// and the SPA gate read. They are a contract (docs/api.md, section 2), so the
// transitions are tested explicitly.
func TestSnapshotStatusAndDatabase(t *testing.T) {
	state := New("up-node-1")

	// A boot that has not contacted the database yet: no failure, no answer.
	start := state.Snapshot()
	if start.Status != StatusStarting || start.Ready {
		t.Fatalf("a fresh state must be %q and not ready: %+v", StatusStarting, start)
	}
	if start.Database != "pending" {
		t.Fatalf("database = %q, want pending", start.Database)
	}
	if start.NodeID != "up-node-1" || start.Phase != PhaseStarting {
		t.Fatalf("unexpected identity/phase: %+v", start)
	}

	// A connection that is being retried: the process is degraded (it cannot
	// serve the API) and the reason is classified.
	state.Retry(3, i18n.CodeDatabaseCredentials, "Access denied for user 'up'", 4*time.Second)
	retrying := state.Snapshot()
	if retrying.Status != StatusDegraded {
		t.Fatalf("status = %q, want %q", retrying.Status, StatusDegraded)
	}
	if retrying.Database != "unauthorized" || retrying.Code != i18n.CodeDatabaseCredentials {
		t.Fatalf("unexpected classification: %+v", retrying)
	}
	if retrying.Attempts != 3 || retrying.NextRetryMS != 4000 {
		t.Fatalf("the retry counters must reach the client: %+v", retrying)
	}

	// Making progress again clears the failure: on a slow but healthy database
	// the page must go back to a progress message.
	state.SetPhase(PhaseMigrating)
	progressing := state.Snapshot()
	if progressing.Status != StatusStarting || progressing.Code != "" || progressing.Database != "pending" {
		t.Fatalf("a new phase must clear the failure: %+v", progressing)
	}

	// A step that will never succeed on its own.
	state.Fail(i18n.CodeDatabaseMissing, "Unknown database 'up'")
	failed := state.Snapshot()
	if failed.Status != StatusDegraded || failed.Database != "missing" || failed.Attempts != 0 {
		t.Fatalf("unexpected failure snapshot: %+v", failed)
	}

	// Ready: the DB answers, the API serves.
	state.Ready()
	ready := state.Snapshot()
	if ready.Status != StatusOK || !ready.Ready || ready.Phase != PhaseReady {
		t.Fatalf("unexpected ready snapshot: %+v", ready)
	}
	if ready.Database != "ok" || ready.Code != "" {
		t.Fatalf("a ready process must report a healthy database: %+v", ready)
	}
	if ready.ElapsedMS < 0 || ready.Since.IsZero() {
		t.Fatalf("the elapsed time must be reported: %+v", ready)
	}
}

// TestSnapshotBackgroundFailure checks the one case that is NOT a database
// problem: the deferred rollup rebuild failing after the API is serving. The
// process stays ready (the UI must keep working) but the health check turns
// degraded, and the database field stays honest.
func TestSnapshotBackgroundFailure(t *testing.T) {
	state := New("up-node-1")
	state.Ready()
	state.SetMaintenance(MaintenanceRollups)

	working := state.Snapshot()
	if !working.Ready || working.Status != StatusOK || working.Maintenance != MaintenanceRollups {
		t.Fatalf("unexpected snapshot during maintenance: %+v", working)
	}

	state.Fail(i18n.CodeDatabaseMigration, "statement timeout")
	broken := state.Snapshot()
	if !broken.Ready {
		t.Fatalf("the process must keep serving: %+v", broken)
	}
	if broken.Status != StatusDegraded {
		t.Fatalf("status = %q, want %q", broken.Status, StatusDegraded)
	}
	if broken.Database != "ok" {
		t.Fatalf("the database itself is fine; only the background pass failed: %+v", broken)
	}
}

// TestNilStateTolerated documents the nil receiver contract: a caller that does
// not track the progress must not have to build a State.
func TestNilStateTolerated(t *testing.T) {
	var state *State
	state.SetPhase(PhaseMigrating)
	state.Retry(1, i18n.CodeDatabaseUnreachable, "boom", time.Second)
	state.Fail(i18n.CodeBootFailed, "boom")
	state.Ready()
	state.SetMaintenance(MaintenanceRollups)
	state.ClearMaintenance()
	if snap := state.Snapshot(); snap.Status != StatusOK {
		t.Fatalf("a nil state reads as a healthy one: %+v", snap)
	}
}
