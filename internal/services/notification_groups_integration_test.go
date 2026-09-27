// This file holds an OPT-IN integration check of the group lookup that feeds the
// notification payloads (NotificationService.groupNames, see
// notification_dispatch.go and docs/notifications.md).
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
//	  go test ./internal/services/ -run TestNotificationGroupNamesLive -v
//	docker rm -f up-groups-verify
//
// Why it needs a real engine: the statement crosses three tables
// (monitors -> monitor_group_members -> monitor_groups) and its ORDER BY is the
// only thing that keeps the groups in the order the operator sees in
// Admin > Groups. A unit test cannot tell a query that orders them from one that
// returns them in whatever order the engine picks.
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

// TestNotificationGroupNamesLive pins the group lookup of a monitor: the names
// come back in sort order (not in the order of the membership rows, and not
// alphabetically) and a monitor in no group yields an empty list rather than nil.
func TestNotificationGroupNamesLive(t *testing.T) {
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

	// The sort orders deliberately contradict the alphabetical order of the
	// names: a lookup that sorted by name, or did not sort at all, fails below.
	groups := []*models.MonitorGroup{
		{Name: "zz-verify-c-" + suffix, SortOrder: 1},
		{Name: "zz-verify-a-" + suffix, SortOrder: 2},
		{Name: "zz-verify-b-" + suffix, SortOrder: 3},
	}
	for _, group := range groups {
		if err := tx.Create(group).Error; err != nil {
			t.Fatalf("creating the group %s: %v", group.Name, err)
		}
	}
	for i := len(groups) - 1; i >= 0; i-- {
		member := &models.MonitorGroupMember{GroupID: groups[i].ID, MonitorID: monitor.ID}
		if err := tx.Create(member).Error; err != nil {
			t.Fatalf("linking the group %s: %v", groups[i].Name, err)
		}
	}

	service := NewNotificationService(tx, &config.Config{NodeID: "verify"},
		slog.New(slog.NewTextHandler(io.Discard, nil)), nil)

	want := []string{groups[0].Name, groups[1].Name, groups[2].Name}
	got := service.groupNames(context.Background(), monitor.ID)
	if len(got) != len(want) {
		t.Fatalf("groupNames() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("groupNames() = %v, want %v (the groups must follow sort_order)", got, want)
		}
	}

	// A monitor in no group must yield an empty list, never nil: NewMessage
	// renders nil as null in the webhook body, [] is what a consumer expects.
	bare := &models.Monitor{Name: "zz-verify-bare-" + suffix, Type: models.MonitorTypeHTTP}
	if err := tx.Create(bare).Error; err != nil {
		t.Fatalf("creating the monitor without groups: %v", err)
	}
	if got := service.groupNames(context.Background(), bare.ID); got == nil || len(got) != 0 {
		t.Errorf("groupNames(monitor in no group) = %#v, want an empty list", got)
	}
}
