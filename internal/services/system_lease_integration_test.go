// This file holds an OPT-IN integration check of the ownership lease of the
// hardware monitored systems (system_lease.go): who may talk to an agent, and when
// another node is allowed to take it over.
//
// It is skipped unless UP_SCRATCH_DSN points at a MariaDB/MySQL database. The rows
// it writes are removed at the end (systems, leases and nodes by their test
// identifiers), so it leaves nothing behind. The usual way to run it:
//
//	docker run --rm -d --name up-lease-verify -p 127.0.0.1:3307:3306 \
//	  -e MARIADB_ROOT_PASSWORD=verify -e MARIADB_DATABASE=up \
//	  -e MARIADB_USER=up -e MARIADB_PASSWORD=verify mariadb:11
//	UP_SCRATCH_DSN='up:verify@tcp(127.0.0.1:3307)/up?charset=utf8mb4&parseTime=true&loc=UTC' \
//	  go test ./internal/services/ -run TestSystemLeaseLive -v
//	docker rm -f up-lease-verify
//
// Why it needs a real engine: the claim is a SELECT ... FOR UPDATE followed by an
// INSERT or an UPDATE on a unique primary key. What the check is about is exactly
// what two transactions do to that row, which no in-memory fake answers.
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

// leaseTestService builds a SystemService on a scratch database, without the sync
// emitter or the hub (the claim must work without either).
func leaseTestService(t *testing.T, db *gorm.DB, nodeID string) *SystemService {
	t.Helper()
	return NewSystemService(db, &config.Config{NodeID: nodeID}, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

// liveNode registers a node with a fresh heartbeat.
func liveNode(t *testing.T, db *gorm.DB, nodeID string) {
	t.Helper()
	now := time.Now().UTC()
	node := models.Node{
		NodeID:        nodeID,
		Name:          nodeID,
		Status:        models.NodeStatusOnline,
		LastHeartbeat: &now,
	}
	if err := db.Where("node_id = ?", nodeID).Delete(&models.Node{}).Error; err != nil {
		t.Fatalf("cleaning node %s: %v", nodeID, err)
	}
	if err := db.Create(&node).Error; err != nil {
		t.Fatalf("creating node %s: %v", nodeID, err)
	}
}

// deadNode registers a node whose last heartbeat is past the grace period.
func deadNode(t *testing.T, db *gorm.DB, nodeID string) {
	t.Helper()
	stale := time.Now().UTC().Add(-(models.NodeOfflineSeconds + 60) * time.Second)
	node := models.Node{
		NodeID:        nodeID,
		Name:          nodeID,
		Status:        models.NodeStatusOffline,
		LastHeartbeat: &stale,
	}
	if err := db.Where("node_id = ?", nodeID).Delete(&models.Node{}).Error; err != nil {
		t.Fatalf("cleaning node %s: %v", nodeID, err)
	}
	if err := db.Create(&node).Error; err != nil {
		t.Fatalf("creating node %s: %v", nodeID, err)
	}
}

// TestSystemLeaseLive checks the three cases the ownership rules exist for: one
// node holds it, an expired lease is taken over, and a lease whose node stopped
// reporting heartbeats is taken over as well.
func TestSystemLeaseLive(t *testing.T) {
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
	ctx := context.Background()

	system := models.System{
		UUID:         models.NewSystemUUID(),
		OriginNodeID: "up-lease-node-1",
		Revision:     1,
		Name:         "lease-test-" + time.Now().UTC().Format("150405.000000"),
		Enabled:      true,
	}
	if err := db.Create(&system).Error; err != nil {
		t.Fatalf("creating the system: %v", err)
	}
	t.Cleanup(func() {
		db.Where("system_id = ?", system.ID).Delete(&models.SystemLease{})
		db.Delete(&models.System{}, system.ID)
		db.Where("node_id IN ?", []string{"up-lease-node-1", "up-lease-node-2"}).Delete(&models.Node{})
	})

	liveNode(t, db, "up-lease-node-1")
	liveNode(t, db, "up-lease-node-2")
	first := leaseTestService(t, db, "up-lease-node-1")
	second := leaseTestService(t, db, "up-lease-node-2")

	// 1. The first node takes it, and the second one is refused while the first is
	//    alive and renewing.
	if ok, err := first.ClaimSystem(ctx, system.ID); err != nil || !ok {
		t.Fatalf("first claim = %v, %v; want true", ok, err)
	}
	if ok, err := second.ClaimSystem(ctx, system.ID); err != nil || ok {
		t.Fatalf("second claim = %v, %v; want false (the lease is held)", ok, err)
	}
	// 2. Renewing keeps it, and only the owner can renew it.
	if ok, err := first.RenewSystem(ctx, system.ID); err != nil || !ok {
		t.Fatalf("renew by the owner = %v, %v", ok, err)
	}
	if ok, err := second.RenewSystem(ctx, system.ID); err != nil || ok {
		t.Fatalf("renew by a stranger = %v, %v; want false", ok, err)
	}
	// 3. An expired lease is taken over, and the previous owner learns it.
	if err := db.Model(&models.SystemLease{}).Where("system_id = ?", system.ID).
		Update("expires_at", time.Now().UTC().Add(-time.Minute)).Error; err != nil {
		t.Fatalf("expiring the lease: %v", err)
	}
	if ok, err := second.ClaimSystem(ctx, system.ID); err != nil || !ok {
		t.Fatalf("claim of an expired lease = %v, %v; want true", ok, err)
	}
	if ok, err := first.RenewSystem(ctx, system.ID); err != nil || ok {
		t.Fatalf("renew after a takeover = %v, %v; want false", ok, err)
	}
	if ok, err := first.ClaimSystem(ctx, system.ID); err != nil || ok {
		t.Fatalf("claim while a live node holds it = %v, %v; want false", ok, err)
	}
	// 4. A lease whose node died is taken over even before it expires.
	deadNode(t, db, "up-lease-node-2")
	if ok, err := first.ClaimSystem(ctx, system.ID); err != nil || !ok {
		t.Fatalf("claim from a dead owner = %v, %v; want true", ok, err)
	}
	// 5. Releasing drops it, and the other node can take it.
	if err := first.ReleaseSystem(ctx, system.ID); err != nil {
		t.Fatalf("release: %v", err)
	}
	liveNode(t, db, "up-lease-node-2")
	if ok, err := second.ClaimSystem(ctx, system.ID); err != nil || !ok {
		t.Fatalf("claim after a release = %v, %v; want true", ok, err)
	}
}
