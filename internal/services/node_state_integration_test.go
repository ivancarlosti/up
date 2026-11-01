// This file holds an OPT-IN integration check of monitor_node_states, the derived
// table the cluster voting reads instead of grouping the heartbeat history (see
// internal/services/node_state.go and models.MonitorNodeState).
//
// It is skipped unless UP_SCRATCH_DSN points at a THROWAWAY MariaDB/MySQL
// database: it creates monitors and heartbeats, and it deletes the rows it
// created at the end (plus a few thousand heartbeats of its own). The usual way
// to run it is a disposable container:
//
//	docker run --rm -d --name up-node-state -p 127.0.0.1:3307:3306 \
//	  -e MARIADB_ROOT_PASSWORD=verify -e MARIADB_DATABASE=up \
//	  -e MARIADB_USER=up -e MARIADB_PASSWORD=verify mariadb:11
//	UP_SCRATCH_DSN='up:verify@tcp(127.0.0.1:3307)/up?charset=utf8mb4&parseTime=true&loc=UTC' \
//	  go test ./internal/services/ -run TestNodeStateLive -v
//	docker rm -f up-node-state
//
// Why it needs a real engine: the whole point of the table is the SQL around it.
// The upsert decides insert-or-replace and has to keep an out of order heartbeat
// from moving a row backwards, the boot seed is an INSERT ... SELECT with a
// FORCE INDEX hint and a VALUES() upsert, and the deletion is a chunked DELETE
// whose LIMIT only GORM's MySQL dialector emits. None of that is exercised by a
// unit test, and all of it fails at runtime - on the probe path or at boot.
package services

import (
	"context"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"github.com/ivancarlosti/up/internal/config"
	"github.com/ivancarlosti/up/internal/models"
)

// nodeStateOf reads the stored state of one (monitor, node) pair.
func nodeStateOf(t *testing.T, db *gorm.DB, monitorID uint, nodeID string) (models.MonitorNodeState, bool) {
	t.Helper()
	var state models.MonitorNodeState
	err := db.Where("monitor_id = ? AND node_id = ?", monitorID, nodeID).Take(&state).Error
	if err == gorm.ErrRecordNotFound {
		return models.MonitorNodeState{}, false
	}
	if err != nil {
		t.Fatalf("reading the state of (%d, %s): %v", monitorID, nodeID, err)
	}
	return state, true
}

// TestNodeStateLive pins the four promises of the per node state table: the boot
// seed copies the newest verdict inside the vote window and nothing else, a
// heartbeat write replaces the row, an out of order heartbeat does not, and the
// deletion of a monitor takes its states with it while the history is removed in
// chunks.
func TestNodeStateLive(t *testing.T) {
	dsn := os.Getenv("UP_SCRATCH_DSN")
	if dsn == "" {
		t.Skip("UP_SCRATCH_DSN is not set")
	}
	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{
		NowFunc:                func() time.Time { return time.Now().UTC() },
		SkipDefaultTransaction: true,
		Logger:                 gormlogger.Default.LogMode(gormlogger.Silent),
	})
	if err != nil {
		t.Fatalf("connecting: %v", err)
	}
	// The tables the check touches, taken from the very lists the deletion walks:
	// a model moved from one list to the other is then covered either way.
	toMigrate := []any{&models.Monitor{}}
	toMigrate = append(toMigrate, monitorLinkModels...)
	toMigrate = append(toMigrate, monitorHistoryModels...)
	if err := db.AutoMigrate(toMigrate...); err != nil {
		t.Fatalf("migrating the tables the check needs: %v", err)
	}

	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := &config.Config{NodeID: "node-a"}
	heartbeats := NewHeartbeatService(db, cfg, log)
	stats := NewStatsService(db)
	monitors := NewMonitorService(db, cfg, log, stats)

	suffix := time.Now().UTC().Format("20060102150405.000000")
	monitor := &models.Monitor{Name: "zz-node-state-" + suffix, Type: models.MonitorTypeHTTP, IntervalSeconds: 60, Active: true}
	other := &models.Monitor{Name: "zz-node-state-other-" + suffix, Type: models.MonitorTypeHTTP, IntervalSeconds: 60, Active: true}
	for _, m := range []*models.Monitor{monitor, other} {
		if err := db.Create(m).Error; err != nil {
			t.Fatalf("creating the monitor %s: %v", m.Name, err)
		}
	}
	t.Cleanup(func() {
		for _, m := range []*models.Monitor{monitor, other} {
			for _, model := range []any{&models.Heartbeat{}, &models.HeartbeatRollup{},
				&models.MonitorNodeState{}, &models.LighthouseRun{}} {
				if err := db.Where("monitor_id = ?", m.ID).Delete(model).Error; err != nil {
					t.Errorf("cleaning up %T of monitor %d: %v", model, m.ID, err)
				}
			}
			if err := db.Delete(&models.Monitor{}, m.ID).Error; err != nil {
				t.Errorf("cleaning up the monitor %d: %v", m.ID, err)
			}
		}
	})

	now := time.Now().UTC()
	history := []models.Heartbeat{
		// Older than the two minute vote window of a 60 s monitor: the seed must not
		// copy either of them.
		{MonitorID: monitor.ID, NodeID: "node-b", Status: models.StatusUp, LatencyMS: 21, CreatedAt: now.Add(-3 * time.Hour)},
		{MonitorID: other.ID, NodeID: "node-a", Status: models.StatusUp, LatencyMS: 22, CreatedAt: now.Add(-3 * time.Hour)},
		// Inside the window, in the order they were run: the last row of each node is
		// the verdict the seed has to keep.
		{MonitorID: monitor.ID, NodeID: "node-a", Status: models.StatusUp, LatencyMS: 12, CreatedAt: now.Add(-90 * time.Second)},
		{MonitorID: monitor.ID, NodeID: "node-a", Status: models.StatusDown, LatencyMS: 30, CreatedAt: now.Add(-30 * time.Second)},
		{MonitorID: monitor.ID, NodeID: "node-b", Status: models.StatusUp, LatencyMS: 20, CreatedAt: now.Add(-40 * time.Second)},
	}
	for i := range history {
		if err := db.Create(&history[i]).Error; err != nil {
			t.Fatalf("storing the heartbeat %d: %v", i, err)
		}
	}

	if err := SeedNodeStates(ctx, db, log); err != nil {
		t.Fatalf("seeding the per node states: %v", err)
	}
	stateA, ok := nodeStateOf(t, db, monitor.ID, "node-a")
	if !ok {
		t.Fatal("the seed did not copy the verdict of (monitor, node-a)")
	}
	if stateA.Status != models.StatusDown || stateA.LatencyMS != 30 {
		t.Fatalf("the seed kept the wrong verdict of node-a: %+v", stateA)
	}
	if stateB, ok := nodeStateOf(t, db, monitor.ID, "node-b"); !ok || stateB.LatencyMS != 20 {
		t.Fatalf("the seed kept the wrong verdict of node-b: %+v (present %v)", stateB, ok)
	}
	if _, ok := nodeStateOf(t, db, other.ID, "node-a"); ok {
		t.Fatal("the seed copied a verdict older than the vote window")
	}

	// Idempotent: a second boot must not move a row (the guard of the upsert keeps
	// the one that is already there, which is the newer one).
	if err := SeedNodeStates(ctx, db, log); err != nil {
		t.Fatalf("seeding twice: %v", err)
	}
	if again, _ := nodeStateOf(t, db, monitor.ID, "node-a"); !again.CheckedAt.Equal(stateA.CheckedAt) {
		t.Fatalf("the second seed moved the verdict from %s to %s", stateA.CheckedAt, again.CheckedAt)
	}

	// The write path: Record stores the heartbeat AND the verdict.
	if err := heartbeats.Record(ctx, &models.Heartbeat{
		MonitorID: monitor.ID, NodeID: "node-a", Status: models.StatusUp, LatencyMS: 15, CreatedAt: now,
	}); err != nil {
		t.Fatalf("recording a heartbeat: %v", err)
	}
	// An out of order heartbeat (a retry landing late, a manual "check now") must
	// not replace a newer verdict.
	if err := heartbeats.Record(ctx, &models.Heartbeat{
		MonitorID: monitor.ID, NodeID: "node-a", Status: models.StatusDown, LatencyMS: 99,
		CreatedAt: now.Add(-5 * time.Minute),
	}); err != nil {
		t.Fatalf("recording an older heartbeat: %v", err)
	}
	if state, _ := nodeStateOf(t, db, monitor.ID, "node-a"); state.Status != models.StatusUp || state.LatencyMS != 15 {
		t.Fatalf("an out of order heartbeat replaced the newer verdict: %+v", state)
	}

	// What the voting reads: the fresh rows of the batch, and nothing when the
	// bound is in the future.
	states, err := stats.LatestPerNode(ctx, []uint{monitor.ID, other.ID}, now.Add(-2*time.Minute))
	if err != nil {
		t.Fatalf("reading the per node states: %v", err)
	}
	if len(states) != 2 {
		t.Fatalf("LatestPerNode returned %d rows, want the 2 fresh ones: %+v", len(states), states)
	}
	if none, err := stats.LatestPerNode(ctx, []uint{monitor.ID}, now.Add(time.Hour)); err != nil || len(none) != 0 {
		t.Fatalf("LatestPerNode with a future bound = %d rows (err %v), want none", len(none), err)
	}

	// Phase one of a deletion: the monitor and the bounded rows, in one transaction.
	if err := db.Transaction(func(tx *gorm.DB) error { return deleteMonitorRow(tx, monitor.ID) }); err != nil {
		t.Fatalf("deleting the monitor row: %v", err)
	}
	if _, ok := nodeStateOf(t, db, monitor.ID, "node-a"); ok {
		t.Fatal("the deletion left a per node state behind")
	}

	// A probe that was still running when the monitor was deleted can write its
	// verdict afterwards. The row is invisible, and the boot reaps it.
	if err := db.Create(&models.MonitorNodeState{
		MonitorID: monitor.ID, NodeID: "node-a", Status: models.StatusUp, CheckedAt: now,
	}).Error; err != nil {
		t.Fatalf("recreating a verdict of the deleted monitor: %v", err)
	}
	if err := SeedNodeStates(ctx, db, log); err != nil {
		t.Fatalf("seeding after the deletion: %v", err)
	}
	if _, ok := nodeStateOf(t, db, monitor.ID, "node-a"); ok {
		t.Fatal("the boot seed kept the verdict of a monitor that no longer exists")
	}

	// Phase two: the history, in chunks. More rows than a single chunk, so the loop
	// has to run more than once.
	rows := make([]models.Heartbeat, 0, monitorHistoryChunk+1000)
	for i := 0; i < monitorHistoryChunk+1000; i++ {
		rows = append(rows, models.Heartbeat{
			MonitorID: monitor.ID, NodeID: "node-a", Status: models.StatusUp,
			LatencyMS: int64(i % 100), CreatedAt: now.Add(-time.Duration(i) * time.Minute),
		})
	}
	if err := db.CreateInBatches(rows, 1000).Error; err != nil {
		t.Fatalf("storing %d heartbeats: %v", len(rows), err)
	}
	monitors.purgeMonitorHistory(ctx, monitor.ID)
	if left := countRows(t, db, &models.Heartbeat{}, "monitor_id = ?", monitor.ID); left != 0 {
		t.Fatalf("%d heartbeats survived the chunked purge", left)
	}
	if left := countRows(t, db, &models.HeartbeatRollup{}, "monitor_id = ?", monitor.ID); left != 0 {
		t.Fatalf("%d rollups survived the chunked purge", left)
	}
	// The purge is scoped to the monitor it was asked about.
	if left := countRows(t, db, &models.Heartbeat{}, "monitor_id = ?", other.ID); left != 1 {
		t.Fatalf("the purge of one monitor left %d heartbeats of another, want 1", left)
	}
}
