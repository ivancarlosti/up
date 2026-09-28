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
	"errors"
	"io"
	"log/slog"
	"net/http"
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

// scratchTx opens the shared scratch connection and starts the transaction the
// live checks of this file roll back, so a run leaves nothing behind even when
// UP_SCRATCH_DSN points at a copy that is not disposable. It skips the test when
// the variable is not set.
func scratchTx(t *testing.T) *gorm.DB {
	t.Helper()
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
	t.Cleanup(func() {
		if err := tx.Rollback().Error; err != nil {
			t.Errorf("rolling back: %v", err)
		}
	})
	return tx
}

// templateOf re-reads a template inside the scratch transaction: exactly what the
// link dialog is seeded with when it reopens.
func templateOf(t *testing.T, tx *gorm.DB, id uint) models.MonitorTemplate {
	t.Helper()
	var row models.MonitorTemplate
	if err := tx.First(&row, id).Error; err != nil {
		t.Fatalf("reading template %d: %v", id, err)
	}
	return row
}

// TestLinkScopeLive pins what the link dialog depends on: the scope of a run is
// STORED on the template (so re-opening the dialog shows the decision instead of
// "every monitor of this type"), a dry run stores nothing, a tag scope matches
// whole tags only, the deprecated group_ids payload is translated to uuids, an
// edit that does not carry a scope keeps the stored one, and a scope naming a
// group that no longer exists is pruned or refused.
func TestLinkScopeLive(t *testing.T) {
	tx := scratchTx(t)
	ctx := context.Background()
	suffix := time.Now().UTC().Format("20060102150405.000000")
	cfg := &config.Config{NodeID: "verify"}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	monitors := NewMonitorService(tx, cfg, log, nil)
	templates := NewMonitorTemplateService(tx, cfg, log)
	templates.SetMonitorService(monitors)

	template := &models.MonitorTemplate{
		Name:     "zz-scope-tpl-" + suffix,
		Type:     models.MonitorTypeHTTP,
		Config:   models.MonitorConfig{Method: "GET"},
		Defaults: models.TemplateDefaults{IntervalSeconds: 120, TimeoutSeconds: 20},
	}
	if err := tx.Create(template).Error; err != nil {
		t.Fatalf("creating the template: %v", err)
	}
	// The tag is unique to this run, so a shared scratch database cannot make the
	// tag scope look wider than it is.
	tag := "zz-tag-" + suffix
	group := &models.MonitorGroup{Name: "zz-scope-group-" + suffix}
	if err := tx.Create(group).Error; err != nil {
		t.Fatalf("creating the group: %v", err)
	}

	makeMonitor := func(label, tags string) *models.Monitor {
		monitor := &models.Monitor{
			Name:            "zz-scope-" + label + "-" + suffix,
			Type:            models.MonitorTypeHTTP,
			Active:          false,
			IntervalSeconds: 300,
			TimeoutSeconds:  10,
			TemplateUUID:    template.UUID,
			Tags:            tags,
			Config:          models.MonitorConfig{URL: "http://127.0.0.1:9/" + label, Method: "GET"},
		}
		if err := monitors.Create(ctx, monitor, nil, nil); err != nil {
			t.Fatalf("creating the monitor %s: %v", label, err)
		}
		return monitor
	}
	// Every monitor follows the template and every one of them is a candidate for
	// a detach: the scope of each run below decides who stays.
	alpha := makeMonitor("alpha", tag)   // tagged: the only one in a tag scope
	beta := makeMonitor("beta", tag+"x") // "zz-tag-...x" only LOOKS like the tag
	gamma := makeMonitor("gamma", "")    // untagged, member of the group
	if err := tx.Create(&models.MonitorGroupMember{GroupID: group.ID, MonitorID: gamma.ID}).Error; err != nil {
		t.Fatalf("linking the group member: %v", err)
	}

	// --- a tag scope, previewed ------------------------------------------------
	tagScope := models.TemplateLinkScope{Kind: models.TemplateScopeTags, Tags: []string{tag}}
	preview, err := templates.LinkAll(ctx, LinkOptions{TemplateID: template.ID, Scope: tagScope, DryRun: true})
	if err != nil {
		t.Fatalf("tag dry run: %v", err)
	}
	if preview.Monitors != 1 || preview.Updated != 1 || preview.Unlinked != 2 || preview.Linked != 0 {
		t.Fatalf("tag dry run = %+v, want only the tagged monitor in scope and the two others released", preview)
	}
	if preview.Scope.Kind != models.TemplateScopeTags || len(preview.Scope.Tags) != 1 {
		t.Fatalf("the preview did not report the scope it would store: %+v", preview.Scope)
	}
	if got := rowOf(t, tx, alpha.ID); got.IntervalSeconds != 300 {
		t.Errorf("the dry run applied the defaults: %+v", got)
	}
	if stored := templateOf(t, tx, template.ID); !stored.LinkScope.IsZero() {
		t.Errorf("the dry run stored a scope: %+v", stored.LinkScope)
	}

	// --- a tag scope, applied -------------------------------------------------
	result, err := templates.LinkAll(ctx, LinkOptions{TemplateID: template.ID, Scope: tagScope})
	if err != nil {
		t.Fatalf("tag run: %v", err)
	}
	if result.Monitors != 1 || result.Updated != 1 || result.Unlinked != 2 {
		t.Fatalf("tag run = %+v, want the same counts as the preview", result)
	}
	if got := rowOf(t, tx, alpha.ID); got.TemplateUUID != template.UUID || got.IntervalSeconds != 120 {
		t.Errorf("the tagged monitor did not take the defaults: %+v", got)
	}
	// The detach keeps everything but the link: the tags of a released monitor are
	// what the next tag scope reads, so losing them would cascade.
	for _, released := range []*models.Monitor{beta, gamma} {
		got := rowOf(t, tx, released.ID)
		if got.TemplateUUID != "" {
			t.Errorf("monitor %d still follows the template", got.ID)
		}
		if got.Tags != models.CleanTags(released.Tags) || got.Name != released.Name || got.Config.URL != released.Config.URL {
			t.Errorf("monitor %d lost more than the link: %+v", got.ID, got)
		}
	}
	stored := templateOf(t, tx, template.ID)
	if stored.LinkScope.Kind != models.TemplateScopeTags || len(stored.LinkScope.Tags) != 1 || stored.LinkScope.Tags[0] != tag {
		t.Fatalf("the tag scope was not stored: %+v", stored.LinkScope)
	}

	// --- the deprecated group_ids payload -------------------------------------
	// An older client sends ids and no scope: the run must narrow to what it
	// asked for (never to the whole type) and remember the group by uuid.
	legacy, err := templates.LinkAll(ctx, LinkOptions{TemplateID: template.ID, GroupIDs: []uint{group.ID}, DryRun: true})
	if err != nil {
		t.Fatalf("legacy dry run: %v", err)
	}
	if legacy.Scope.Kind != models.TemplateScopeGroups || len(legacy.Scope.GroupUUIDs) != 1 ||
		legacy.Scope.GroupUUIDs[0] != group.UUID {
		t.Fatalf("group_ids was not translated to the uuid of the group: %+v", legacy.Scope)
	}
	if legacy.Monitors != 1 || legacy.Linked != 1 || legacy.Unlinked != 1 {
		t.Fatalf("legacy dry run = %+v, want only the member of the group in scope", legacy)
	}

	applied, err := templates.LinkAll(ctx, LinkOptions{TemplateID: template.ID, GroupIDs: []uint{group.ID}})
	if err != nil {
		t.Fatalf("legacy run: %v", err)
	}
	if applied.Monitors != 1 || applied.Linked != 1 || applied.Unlinked != 1 {
		t.Fatalf("legacy run = %+v, want the member linked and the tagged follower released", applied)
	}
	if got := rowOf(t, tx, gamma.ID); got.TemplateUUID != template.UUID || got.IntervalSeconds != 120 {
		t.Errorf("the group member did not follow the template: %+v", got)
	}
	stored = templateOf(t, tx, template.ID)
	if stored.LinkScope.Kind != models.TemplateScopeGroups || len(stored.LinkScope.GroupUUIDs) != 1 ||
		stored.LinkScope.GroupUUIDs[0] != group.UUID {
		t.Fatalf("the group scope was not stored: %+v", stored.LinkScope)
	}

	// --- re-opening the dialog, and an edit that carries no scope -------------
	// What the admin view reads back is what it seeds the picker with.
	reopened, err := templates.Get(ctx, template.ID)
	if err != nil {
		t.Fatalf("re-reading the template: %v", err)
	}
	if reopened.LinkScope.Kind != models.TemplateScopeGroups || len(reopened.LinkScope.GroupUUIDs) != 1 {
		t.Fatalf("the stored scope did not come back with the template: %+v", reopened.LinkScope)
	}
	// The template dialog does not know the field: an edit that carries no scope
	// must not send the template back to "every monitor of this type".
	edited := *reopened
	edited.LinkScope = models.TemplateLinkScope{}
	edited.Description = "edited by the scope live check"
	if err := templates.Update(ctx, &edited); err != nil {
		t.Fatalf("updating the template without a scope: %v", err)
	}
	if stored = templateOf(t, tx, template.ID); stored.LinkScope.Kind != models.TemplateScopeGroups ||
		len(stored.LinkScope.GroupUUIDs) != 1 || stored.LinkScope.GroupUUIDs[0] != group.UUID {
		t.Fatalf("the edit wiped the stored scope: %+v", stored.LinkScope)
	}

	// --- a group deleted elsewhere --------------------------------------------
	// The scope is pruned of the references that are gone, and a scope that names
	// nothing that exists is refused instead of silently covering every monitor.
	partial, err := templates.LinkAll(ctx, LinkOptions{TemplateID: template.ID, DryRun: true,
		Scope: models.TemplateLinkScope{Kind: models.TemplateScopeGroups,
			GroupUUIDs: []string{"zz-missing-" + suffix, group.UUID}}})
	if err != nil {
		t.Fatalf("dry run with a deleted group: %v", err)
	}
	if len(partial.Scope.GroupUUIDs) != 1 || partial.Scope.GroupUUIDs[0] != group.UUID {
		t.Fatalf("the deleted group was not pruned: %+v", partial.Scope)
	}
	_, err = templates.LinkAll(ctx, LinkOptions{TemplateID: template.ID, DryRun: true,
		Scope: models.TemplateLinkScope{Kind: models.TemplateScopeGroups,
			GroupUUIDs: []string{"zz-missing-" + suffix}}})
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusBadRequest {
		t.Fatalf("a scope naming no existing group = %v, want a 400", err)
	}
	if stored = templateOf(t, tx, template.ID); stored.LinkScope.Kind != models.TemplateScopeGroups ||
		len(stored.LinkScope.GroupUUIDs) != 1 {
		t.Fatalf("the refused run changed the stored scope: %+v", stored.LinkScope)
	}
}
