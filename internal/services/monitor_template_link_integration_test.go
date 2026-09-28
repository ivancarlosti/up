// This file holds an OPT-IN integration check of the SCOPE of a link run
// (MonitorTemplateService.LinkAll, see monitor_template_link.go and
// docs/monitors.md): a run that selects groups replaces the scope of the
// template, so the monitors that follow it from outside the selection stop
// following it, while the monitors of the selection keep their own row, their
// address and their groups.
//
// It is skipped unless UP_SCRATCH_DSN points at a MariaDB/MySQL database. Every
// row it writes lives inside a transaction that is rolled back at the end, so it
// leaves nothing behind even when it points at a copy that is not disposable.
// The database must already be migrated (the application creates the schema on
// boot). The usual way to run it:
//
//	docker run --rm -d --name up-scope-verify -p 127.0.0.1:3309:3306 \
//	  -e MARIADB_ROOT_PASSWORD=verify -e MARIADB_DATABASE=up \
//	  -e MARIADB_USER=up -e MARIADB_PASSWORD=verify mariadb:11
//	UP_SCRATCH_DSN='up:verify@tcp(127.0.0.1:3309)/up?charset=utf8mb4&parseTime=true&loc=UTC' \
//	  go test ./internal/services/ -run TestLinkAllRescopesLive -v
//	docker rm -f up-scope-verify
//
// Why it needs a real engine: the scope is resolved by a join
// (monitors -> monitor_group_members) and a detach is a real monitor edit that
// runs through the whole write path (validation, revision bump, outbox). A unit
// test cannot tell a query that resolves the members of the selected groups from
// one that returns every monitor, nor can it see whether the column was really
// cleared while the rest of the row survived.
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

// TestLinkAllRescopesLive pins the re-scope: the monitors of the selected group
// follow the template, the followers outside it are detached (and keep
// everything else), and the "every monitor of this type" scope detaches nobody.
func TestLinkAllRescopesLive(t *testing.T) {
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

	ctx := context.Background()
	suffix := time.Now().UTC().Format("20060102150405.000000")
	cfg := &config.Config{NodeID: "verify"}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	monitors := NewMonitorService(tx, cfg, log, nil)
	templates := NewMonitorTemplateService(tx, cfg, log)
	templates.SetMonitorService(monitors)

	template := &models.MonitorTemplate{
		Name:     "zz-verify-tpl-" + suffix,
		Type:     models.MonitorTypeHTTP,
		Config:   models.MonitorConfig{Method: "GET"},
		Defaults: models.TemplateDefaults{IntervalSeconds: 120, TimeoutSeconds: 20},
	}
	if err := tx.Create(template).Error; err != nil {
		t.Fatalf("creating the template: %v", err)
	}

	// Two groups: the scoped one holds a follower and a monitor that does not
	// follow the template yet, the other one holds a follower the run must leave
	// behind.
	scopedGroup := &models.MonitorGroup{Name: "zz-verify-scoped-" + suffix}
	otherGroup := &models.MonitorGroup{Name: "zz-verify-other-" + suffix}
	for _, group := range []*models.MonitorGroup{scopedGroup, otherGroup} {
		if err := tx.Create(group).Error; err != nil {
			t.Fatalf("creating the group %s: %v", group.Name, err)
		}
	}

	// Every monitor is created through the monitor service, so the rows are the
	// ones the API would have written.
	makeMonitor := func(label, templateUUID string) *models.Monitor {
		monitor := &models.Monitor{
			Name:            "zz-verify-" + label + "-" + suffix,
			Type:            models.MonitorTypeHTTP,
			Active:          false,
			IntervalSeconds: 300,
			TimeoutSeconds:  10,
			TemplateUUID:    templateUUID,
			Config:          models.MonitorConfig{URL: "http://127.0.0.1:9/" + label, Method: "GET"},
		}
		if err := monitors.Create(ctx, monitor, nil, nil); err != nil {
			t.Fatalf("creating the monitor %s: %v", label, err)
		}
		return monitor
	}
	kept := makeMonitor("kept", template.UUID)         // follower, member of the scoped group
	linked := makeMonitor("linked", "")                // not a follower, member of the scoped group
	detached := makeMonitor("detached", template.UUID) // follower, member of the other group
	loose := makeMonitor("loose", template.UUID)       // follower, member of no group

	for _, member := range []*models.MonitorGroupMember{
		{GroupID: scopedGroup.ID, MonitorID: kept.ID},
		{GroupID: scopedGroup.ID, MonitorID: linked.ID},
		{GroupID: otherGroup.ID, MonitorID: detached.ID},
	} {
		if err := tx.Create(member).Error; err != nil {
			t.Fatalf("linking the group members: %v", err)
		}
	}
	detachedRevision := rowOf(t, tx, detached.ID).Revision

	// --- the preview ---------------------------------------------------------
	preview, err := templates.LinkAll(ctx,
		LinkOptions{TemplateID: template.ID, GroupIDs: []uint{scopedGroup.ID}, DryRun: true})
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if preview.Monitors != 2 || preview.Linked != 1 || preview.Updated != 2 || preview.Unlinked != 2 {
		t.Fatalf("dry run = %+v, want the 2 monitors of the group (1 of them linked) and the 2 followers left behind", preview)
	}
	// A preview writes nothing: the dialog would be a lie otherwise.
	if got := rowOf(t, tx, linked.ID); got.TemplateUUID != "" || got.IntervalSeconds != 300 {
		t.Fatalf("the dry run wrote to a monitor: %+v", got)
	}
	if got := rowOf(t, tx, kept.ID); got.IntervalSeconds != 300 {
		t.Fatalf("the dry run applied the defaults: %+v", got)
	}
	if got := rowOf(t, tx, detached.ID); got.TemplateUUID != template.UUID {
		t.Fatalf("the dry run detached a follower: %+v", got)
	}

	// --- the run -------------------------------------------------------------
	result, err := templates.LinkAll(ctx, LinkOptions{TemplateID: template.ID, GroupIDs: []uint{scopedGroup.ID}})
	if err != nil {
		t.Fatalf("link run: %v", err)
	}
	if result.Monitors != 2 || result.Linked != 1 || result.Updated != 2 || result.Unlinked != 2 {
		t.Fatalf("link run = %+v, want the same counts as the preview", result)
	}

	// The members of the group follow the template and took its defaults.
	for _, scoped := range []*models.Monitor{kept, linked} {
		row := rowOf(t, tx, scoped.ID)
		if row.TemplateUUID != template.UUID {
			t.Errorf("monitor %d does not follow the template: %q", row.ID, row.TemplateUUID)
		}
		if row.IntervalSeconds != 120 || row.TimeoutSeconds != 20 {
			t.Errorf("monitor %d did not take the defaults: %+v", row.ID, row)
		}
	}

	// The followers outside the scope stopped following it, and the detach kept
	// everything else: the row, its address and its groups.
	for _, released := range []*models.Monitor{detached, loose} {
		row := rowOf(t, tx, released.ID)
		if row.TemplateUUID != "" {
			t.Errorf("monitor %d still follows the template: %q", row.ID, row.TemplateUUID)
		}
		if row.Name != released.Name || row.IntervalSeconds != 300 || row.Config.URL != released.Config.URL {
			t.Errorf("monitor %d lost more than the link: %+v", row.ID, row)
		}
	}
	if !memberOf(t, tx, otherGroup.ID, detached.ID) {
		t.Error("the detach dropped the groups of the monitor")
	}
	if !memberOf(t, tx, scopedGroup.ID, kept.ID) || !memberOf(t, tx, scopedGroup.ID, linked.ID) {
		t.Error("the link dropped the groups of the monitors in scope")
	}
	// The detach is an edit (the revision is the merge order of the cluster), not
	// a silent UPDATE the peers would never hear about.
	if got := rowOf(t, tx, detached.ID).Revision; got <= detachedRevision {
		t.Errorf("the detach did not advance the revision: %d -> %d", detachedRevision, got)
	}

	// The type scope has no outside: it never detaches, and it would simply link
	// everything of the type again (the released followers included). The run is
	// a dry one so the shared rows of the scratch database stay untouched.
	typeWide, err := templates.LinkAll(ctx, LinkOptions{TemplateID: template.ID, DryRun: true})
	if err != nil {
		t.Fatalf("type wide dry run: %v", err)
	}
	if typeWide.Unlinked != 0 {
		t.Fatalf("the type scope detached %d monitors, want none", typeWide.Unlinked)
	}
	if typeWide.Linked < 2 {
		t.Fatalf("the type scope did not report the released followers as linking again: %+v", typeWide)
	}
}

// rowOf re-reads a monitor inside the scratch transaction.
func rowOf(t *testing.T, tx *gorm.DB, id uint) models.Monitor {
	t.Helper()
	var row models.Monitor
	if err := tx.First(&row, id).Error; err != nil {
		t.Fatalf("reading monitor %d: %v", id, err)
	}
	return row
}

// memberOf reports whether a monitor belongs to a group.
func memberOf(t *testing.T, tx *gorm.DB, groupID, monitorID uint) bool {
	t.Helper()
	var count int64
	if err := tx.Model(&models.MonitorGroupMember{}).
		Where("group_id = ? AND monitor_id = ?", groupID, monitorID).
		Count(&count).Error; err != nil {
		t.Fatalf("counting the members of group %d: %v", groupID, err)
	}
	return count == 1
}
