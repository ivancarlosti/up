// This file holds an OPT-IN integration check of the group lookup that feeds the
// notification payloads (NotificationService.groupRef, see notification_dispatch.go
// and docs/notifications.md).
//
// It is skipped unless UP_SCRATCH_DSN points at a MariaDB/MySQL database. Every
// row it writes lives inside a transaction that is rolled back at the end, so it
// leaves nothing behind even when it points at a copy that is not disposable.
// The database must already be migrated (the application creates the schema on
// boot). The usual way to run it:
//
//	docker run --rm -d --name up-groups-verify -p 127.0.0.1:3307:3306 \
//	  -e MARIADB_ROOT_PASSWORD=verify -e MARIADB_DATABASE=up \
//	  -e MARIADB_USER=up -e MARIADB_PASSWORD=verify mariadb:11
//	UP_SCRATCH_DSN='up:verify@tcp(127.0.0.1:3307)/up?charset=utf8mb4&parseTime=true&loc=UTC' \
//	  go test ./internal/services/ -run TestNotificationGroupRefLive -v
//	docker rm -f up-groups-verify
//
// Why it needs a real engine: the statement crosses three tables
// (monitors -> monitor_group_members -> monitor_groups) and it has to PICK one row
// out of them. A monitor belongs to one group now, but a database that predates the
// single group rule still holds a duplicate until the boot consolidation runs, so
// the query picks by an explicit order instead of returning whatever the engine
// hands back first. Only a real engine can tell a query that orders from one that
// does not.
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

// TestNotificationGroupRefLive pins the group lookup of a monitor.
//
// Two rules are what it protects: the group that comes FIRST in the group list the
// operator sees is the one a notification names (never the position inside a group,
// which orders members and not groups), and a monitor in no group yields nil rather
// than a zero value that would render as an empty group in the payload.
func TestNotificationGroupRefLive(t *testing.T) {
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
	monitor := &models.Monitor{Name: "zz-verify-" + suffix, Type: models.MonitorTypeHTTP, Tags: "web,api"}
	if err := tx.Create(monitor).Error; err != nil {
		t.Fatalf("creating the monitor: %v", err)
	}

	// The group order deliberately contradicts the alphabetical order of the names,
	// the positions of the memberships and the insertion order: a lookup that
	// returned the rows in whatever order the engine picked, that sorted by name or
	// that read the position inside a group fails below.
	groups := []*models.MonitorGroup{
		{Name: "zz-verify-c-" + suffix, SortOrder: 1},
		{Name: "zz-verify-a-" + suffix, SortOrder: 2},
		{Name: "zz-verify-b-" + suffix, SortOrder: 3},
		// Two groups that share a position, to pin the tie-break on the id.
		{Name: "zz-verify-d-" + suffix, SortOrder: 7},
		{Name: "zz-verify-e-" + suffix, SortOrder: 7},
	}
	for _, group := range groups {
		if err := tx.Create(group).Error; err != nil {
			t.Fatalf("creating the group %s: %v", group.Name, err)
		}
	}

	// The transitional state the boot consolidation removes (see
	// monitor_group_single.go): the monitor is linked to several groups, in reverse,
	// with positions inside each group that point at the LAST group of the list.
	link := func(monitorID, groupID uint, order int) {
		t.Helper()
		row := &models.MonitorGroupMember{GroupID: groupID, MonitorID: monitorID, SortOrder: order}
		if err := tx.Create(row).Error; err != nil {
			t.Fatalf("linking the group %d at position %d: %v", groupID, order, err)
		}
	}
	link(monitor.ID, groups[2].ID, 1)
	link(monitor.ID, groups[1].ID, 2)
	link(monitor.ID, groups[0].ID, 3)

	// A second monitor ties on the group position, so the id is what breaks the
	// tie: the lookup can never return an arbitrary row.
	tied := &models.Monitor{Name: "zz-verify-tied-" + suffix, Type: models.MonitorTypeHTTP}
	if err := tx.Create(tied).Error; err != nil {
		t.Fatalf("creating the second monitor: %v", err)
	}
	link(tied.ID, groups[3].ID, 1)
	link(tied.ID, groups[4].ID, 1)

	service := NewNotificationService(tx, &config.Config{NodeID: "verify"},
		slog.New(slog.NewTextHandler(io.Discard, nil)), nil)

	want := &models.MonitorGroupRef{ID: groups[0].ID, Name: groups[0].Name}
	if got := service.groupRef(context.Background(), monitor.ID); got == nil || *got != *want {
		t.Fatalf("groupRef() = %v, want %v (the group order decides, not the position inside a group)", got, want)
	}
	want = &models.MonitorGroupRef{ID: groups[3].ID, Name: groups[3].Name}
	if got := service.groupRef(context.Background(), tied.ID); got == nil || *got != *want {
		t.Fatalf("groupRef() = %v, want %v (the id breaks the tie)", got, want)
	}

	// A monitor in no group must yield nil: the caller renders that as no group at
	// all, while a zero valued ref would name an empty group.
	bare := &models.Monitor{Name: "zz-verify-bare-" + suffix, Type: models.MonitorTypeHTTP}
	if err := tx.Create(bare).Error; err != nil {
		t.Fatalf("creating the monitor without groups: %v", err)
	}
	if got := service.groupRef(context.Background(), bare.ID); got != nil {
		t.Errorf("groupRef(monitor in no group) = %v, want nil", got)
	}
}
