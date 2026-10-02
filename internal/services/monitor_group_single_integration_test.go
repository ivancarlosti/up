// This file holds an OPT-IN integration check of the single group rule: the boot
// consolidation that collapses the memberships a monitor used to be able to have
// (ConsolidateSingleGroupMembership, see monitor_group_single.go) and the write path
// that replaces them (setMonitorGroup, replaceMonitorGroupMembers).
//
// It is skipped unless UP_SCRATCH_DSN points at a MariaDB/MySQL database. Every row
// it writes lives inside a transaction that is rolled back at the end, so it leaves
// nothing behind even when it points at a copy that is not disposable. The database
// must already be migrated (the application creates the schema on boot). The usual
// way to run it:
//
//	docker run --rm -d --name up-single-verify -p 127.0.0.1:3307:3306 \
//	  -e MARIADB_ROOT_PASSWORD=verify -e MARIADB_DATABASE=up \
//	  -e MARIADB_USER=up -e MARIADB_PASSWORD=verify mariadb:11
//	UP_SCRATCH_DSN='up:verify@tcp(127.0.0.1:3307)/up?charset=utf8mb4&parseTime=true&loc=UTC' \
//	  go test ./internal/services/ -run TestSingleGroupLive -v
//	docker rm -f up-single-verify
//
// Why it needs a real engine: the consolidation decides WHICH membership survives with
// an ORDER BY over a join (the group order, never the position inside a group) and the
// write path relies on the composite primary key of monitor_group_members. Neither can
// be exercised without the engine that resolves them.
package services

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"github.com/ivancarlosti/up/internal/config"
	"github.com/ivancarlosti/up/internal/i18n"
	"github.com/ivancarlosti/up/internal/models"
)

// groupIDsOfMonitor reads the groups a monitor is in (the boot consolidation must
// leave exactly one row).
func groupIDsOfMonitor(t *testing.T, tx *gorm.DB, monitorID uint) []uint {
	t.Helper()
	ids := []uint{}
	if err := tx.Model(&models.MonitorGroupMember{}).
		Where("monitor_id = ?", monitorID).
		Order("group_id ASC").
		Pluck("group_id", &ids).Error; err != nil {
		t.Fatalf("reading the memberships of monitor %d: %v", monitorID, err)
	}
	return ids
}

// TestSingleGroupLive pins the two halves of the single group rule: one group per
// monitor after the consolidation, and one group per monitor after every write.
func TestSingleGroupLive(t *testing.T) {
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

	tx := db.Begin()
	if tx.Error != nil {
		t.Fatalf("starting the transaction: %v", tx.Error)
	}
	defer func() {
		if err := tx.Rollback().Error; err != nil {
			t.Errorf("rolling back: %v", err)
		}
	}()

	suffix := time.Now().UTC().Format("20060102150405.000000")
	groups := []*models.MonitorGroup{
		{Name: "zz-single-first-" + suffix, SortOrder: 1},
		{Name: "zz-single-second-" + suffix, SortOrder: 2},
		{Name: "zz-single-third-" + suffix, SortOrder: 3},
	}
	for _, group := range groups {
		if err := tx.Create(group).Error; err != nil {
			t.Fatalf("creating the group %s: %v", group.Name, err)
		}
	}
	monitor := &models.Monitor{Name: "zz-single-" + suffix, Type: models.MonitorTypeHTTP}
	if err := tx.Create(monitor).Error; err != nil {
		t.Fatalf("creating the monitor: %v", err)
	}

	// The legacy state: the monitor is in all three groups, with the positions
	// inside each group pointing at the LAST one (a position orders the members of a
	// group, so it must not decide which group the monitor keeps).
	for index, group := range []*models.MonitorGroup{groups[2], groups[1], groups[0]} {
		member := &models.MonitorGroupMember{GroupID: group.ID, MonitorID: monitor.ID, SortOrder: index + 1}
		if err := tx.Create(member).Error; err != nil {
			t.Fatalf("linking the group %s: %v", group.Name, err)
		}
	}

	ctx := context.Background()
	cfg := &config.Config{NodeID: "verify"}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	if err := ConsolidateSingleGroupMembership(ctx, tx, cfg, log); err != nil {
		t.Fatalf("consolidating: %v", err)
	}
	if got := groupIDsOfMonitor(t, tx, monitor.ID); len(got) != 1 || got[0] != groups[0].ID {
		t.Fatalf("after the consolidation the monitor is in %v, want only the first group %d",
			got, groups[0].ID)
	}
	// Idempotent: a second boot must change nothing at all.
	if err := ConsolidateSingleGroupMembership(ctx, tx, cfg, log); err != nil {
		t.Fatalf("consolidating twice: %v", err)
	}
	if got := groupIDsOfMonitor(t, tx, monitor.ID); len(got) != 1 || got[0] != groups[0].ID {
		t.Fatalf("the second consolidation changed the memberships: %v", got)
	}

	// The write path: the pointer carries the whole contract — an id joins the
	// group, nil keeps the current one and 0 leaves the monitor without a group.
	second := groups[1].ID
	if err := setMonitorGroup(tx, monitor.ID, &second); err != nil {
		t.Fatalf("setMonitorGroup(second): %v", err)
	}
	if got := groupIDsOfMonitor(t, tx, monitor.ID); len(got) != 1 || got[0] != groups[1].ID {
		t.Fatalf("setMonitorGroup(second) = %v, want only the second group", got)
	}
	// nil keeps the current group...
	if err := setMonitorGroup(tx, monitor.ID, nil); err != nil {
		t.Fatalf("setMonitorGroup(nil): %v", err)
	}
	if got := groupIDsOfMonitor(t, tx, monitor.ID); len(got) != 1 || got[0] != groups[1].ID {
		t.Fatalf("setMonitorGroup(nil) = %v, want the current group kept", got)
	}
	// ...and zero clears it.
	none := uint(0)
	if err := setMonitorGroup(tx, monitor.ID, &none); err != nil {
		t.Fatalf("setMonitorGroup(0): %v", err)
	}
	if got := groupIDsOfMonitor(t, tx, monitor.ID); len(got) != 0 {
		t.Fatalf("setMonitorGroup(0) = %v, want no group", got)
	}
	// An unknown group is a 400, and the memberships are left alone.
	unknown := uint(999999)
	err = setMonitorGroup(tx, monitor.ID, &unknown)
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Code != i18n.CodeMonitorGroupInvalid {
		t.Fatalf("setMonitorGroup(an unknown group) = %v, want %s", err, i18n.CodeMonitorGroupInvalid)
	}
	// The group editor MOVES a monitor: adding it to another group takes it out of
	// the one it was in.
	first := groups[0].ID
	if err := setMonitorGroup(tx, monitor.ID, &first); err != nil {
		t.Fatalf("setMonitorGroup(first): %v", err)
	}
	if err := tx.Transaction(func(inner *gorm.DB) error {
		return replaceMonitorGroupMembers(inner, groups[2].ID, []uint{monitor.ID})
	}); err != nil {
		t.Fatalf("adding the monitor to the third group: %v", err)
	}
	if got := groupIDsOfMonitor(t, tx, monitor.ID); len(got) != 1 || got[0] != groups[2].ID {
		t.Fatalf("after the group editor the monitor is in %v, want only the third group (a move)", got)
	}
}
