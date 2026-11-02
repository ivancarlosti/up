// This file holds an OPT-IN integration check of the status page CLONE
// (StatusPageService.Clone, see statuspage_clone.go and docs/status-pages.md):
// every setting of the source travels — switches included, so a private page is not
// copied public — the two selections travel with their order and their overrides,
// the copy carries a **fresh identity** (uuid, revision 1, origin this node), the
// subscribers of the source stay with the source, subscriptions start OFF while the
// channel and the delay travel inert, and the whole clone reaches the
// synchronisation outbox as ONE row: the peers never see a half-built page.
//
// It is skipped unless UP_SCRATCH_DSN points at a MariaDB/MySQL database. Every row
// it writes lives inside a transaction that is rolled back at the end. The database
// must already be migrated (the application creates the schema on boot). The usual
// way to run it:
//
//	docker run --rm -d --name up-page-clone-verify -p 127.0.0.1:3309:3306 \
//	  -e MARIADB_ROOT_PASSWORD=verify -e MARIADB_DATABASE=up \
//	  -e MARIADB_USER=up -e MARIADB_PASSWORD=verify mariadb:11
//	UP_SCRATCH_DSN='up:verify@tcp(127.0.0.1:3309)/up?charset=utf8mb4&parseTime=true&loc=UTC' \
//	  go test ./internal/services/ -run TestStatusPageCloneLive -v
//	docker rm -f up-page-clone-verify
//
// Why it needs a real engine: the slug collision is settled by a live query, the
// identity is stamped by the GORM hooks of a real insert, the three `default:true`
// columns are filled by the ENGINE when the struct carries false (the clone has to
// correct them), and "the copy has no subscriber" is a statement about the rows of
// another table — none of which a pure helper can show.
package services

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/ivancarlosti/up/internal/config"
	"github.com/ivancarlosti/up/internal/models"
)

// TestStatusPageCloneLive pins the clone of a status page end to end.
func TestStatusPageCloneLive(t *testing.T) {
	tx := scratchTx(t)
	ctx := context.Background()
	stamp := time.Now().UTC().Format("20060102150405.000000")
	slugStamp := strings.ReplaceAll(stamp, ".", "-")
	// Federated on purpose: the copy must reach the outbox like any other create.
	cfg := &config.Config{NodeID: "verify", ClusterMode: config.ClusterModeFederated}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	pages := NewStatusPageService(tx, cfg, log)
	pages.SetSyncEmitter(NewSyncEmitter(tx, cfg, log))
	monitors := NewMonitorService(tx, cfg, log, nil)
	groups := NewMonitorGroupService(tx, cfg, log)

	// Real rows for everything the payload joins: a dangling id is silently dropped
	// from the published selection, so a fake one would hide a broken copy.
	first := &models.Monitor{
		Name: "zz-clone-page-a-" + stamp, Type: models.MonitorTypeHTTP,
		IntervalSeconds: 120, TimeoutSeconds: 20,
		Config: models.MonitorConfig{URL: "http://127.0.0.1:9/a", Method: "HEAD"},
	}
	second := &models.Monitor{
		Name: "zz-clone-page-b-" + stamp, Type: models.MonitorTypeHTTP,
		IntervalSeconds: 120, TimeoutSeconds: 20,
		Config: models.MonitorConfig{URL: "http://127.0.0.1:9/b", Method: "HEAD"},
	}
	for _, monitor := range []*models.Monitor{first, second} {
		if err := monitors.Create(ctx, monitor, nil, nil); err != nil {
			t.Fatalf("creating monitor %q: %v", monitor.Name, err)
		}
	}
	group := &models.MonitorGroup{Name: "zz-clone-page-group-" + stamp, Description: "the cloned group"}
	if err := groups.Create(ctx, group, []uint{second.ID}); err != nil {
		t.Fatalf("creating the group: %v", err)
	}
	channel := &models.Notification{Name: "zz-clone-page-smtp-" + stamp, Type: models.NotificationSMTP}
	if err := tx.Create(channel).Error; err != nil {
		t.Fatalf("creating the smtp channel: %v", err)
	}

	// The source: every setting away from its default, subscriptions ON, a selection
	// with overrides and a linked group.
	source := &models.StatusPage{
		Slug:                     "zz-clone-page-" + slugStamp,
		Title:                    "ZZ clone page " + stamp,
		Description:              "the page we copy",
		FooterText:               "all times are UTC",
		Theme:                    "dark",
		IsPublic:                 false,
		ShowUptime:               false,
		ShowCharts:               true,
		ShowTags:                 true,
		ShowCertExpiry:           true,
		ShowDomainExpiry:         false,
		UptimeWindowHours:        168,
		CustomCSS:                ".brand { color: red }",
		SubscriptionEnabled:      true,
		SubscriptionSMTPUUID:     channel.UUID,
		SubscriptionDelayMinutes: 30,
	}
	if err := pages.Create(ctx, source, []uint{second.ID, first.ID}); err != nil {
		t.Fatalf("creating the source page: %v", err)
	}
	if err := pages.SetGroups(ctx, source.ID,
		[]models.StatusPageGroupLink{{GroupID: group.ID, DisplayName: "Edge"}}); err != nil {
		t.Fatalf("linking the group: %v", err)
	}
	// The overrides the copy must keep: a display name, a section and both switches
	// off on the first monitor.
	if err := tx.Model(&models.StatusPageMonitor{}).
		Where("status_page_id = ? AND monitor_id = ?", source.ID, first.ID).
		UpdateColumns(map[string]any{
			"display_name": "Primary", "group_name": "edge", "show_uptime": false, "show_chart": false,
		}).Error; err != nil {
		t.Fatalf("overriding the first row: %v", err)
	}
	// A subscriber of the source: the copy must never inherit it.
	subscriber := &models.StatusPageSubscriber{
		StatusPageID: source.ID,
		Email:        "zz-clone-" + slugStamp + "@example.com",
		Locale:       "en-US",
	}
	if err := tx.Create(subscriber).Error; err != nil {
		t.Fatalf("creating the subscriber: %v", err)
	}

	// --- the full copy -------------------------------------------------------
	clone, err := pages.Clone(ctx, source.ID, StatusPageCloneOptions{CopyMonitors: true, CopyGroups: true})
	if err != nil {
		t.Fatalf("cloning the page: %v", err)
	}
	if clone.ID == source.ID || clone.UUID == "" || clone.UUID == source.UUID {
		t.Fatalf("the copy must carry its own identity: clone=%d/%s source=%d/%s",
			clone.ID, clone.UUID, source.ID, source.UUID)
	}
	if clone.Revision != 1 || clone.OriginNodeID != "verify" {
		t.Fatalf("the copy must be stamped like a fresh create: revision=%d origin=%q",
			clone.Revision, clone.OriginNodeID)
	}
	if clone.Title != source.Title+" (copy)" || clone.Slug != source.Slug+statusPageCloneSlugSuffix {
		t.Fatalf("the copy must be named after the source, got %q / %q", clone.Title, clone.Slug)
	}
	if clone.Description != source.Description || clone.FooterText != source.FooterText ||
		clone.Theme != source.Theme || clone.CustomCSS != source.CustomCSS ||
		clone.UptimeWindowHours != 168 {
		t.Fatalf("the settings did not travel: %+v", clone)
	}
	// The three `default:true` columns: the insert must not turn the false of the
	// source into the default of the column.
	if clone.IsPublic || clone.ShowUptime || !clone.ShowCharts || !clone.ShowTags ||
		!clone.ShowCertExpiry || clone.ShowDomainExpiry {
		t.Fatalf("the switches did not travel: %+v", clone)
	}
	if clone.SubscriptionEnabled {
		t.Fatal("the copy must start with subscriptions off")
	}
	if clone.SubscriptionSMTPUUID != channel.UUID || clone.SubscriptionDelayMinutes != 30 {
		t.Fatalf("the subscription configuration must travel inert: %q / %d",
			clone.SubscriptionSMTPUUID, clone.SubscriptionDelayMinutes)
	}

	// --- the explicit selection, order and overrides included -----------------
	sourceItems, err := pages.Items(ctx, source.ID)
	if err != nil {
		t.Fatalf("reading the source selection: %v", err)
	}
	cloneItems, err := pages.Items(ctx, clone.ID)
	if err != nil {
		t.Fatalf("reading the copy selection: %v", err)
	}
	if len(sourceItems) != 2 || len(cloneItems) != len(sourceItems) {
		t.Fatalf("the copy must keep both monitors, got %d of %d", len(cloneItems), len(sourceItems))
	}
	for i, want := range sourceItems {
		got := cloneItems[i]
		if got.MonitorID != want.MonitorID || got.DisplayName != want.DisplayName ||
			got.GroupName != want.GroupName || got.ShowUptime != want.ShowUptime ||
			got.ShowChart != want.ShowChart {
			t.Fatalf("row %d did not travel: got %+v want %+v", i, got, want)
		}
		if got.ID == want.ID || got.StatusPageID != clone.ID {
			t.Fatalf("row %d must be a new row of the copy: %+v", i, got)
		}
	}
	// The overrides were really written: the switch that is off stayed off.
	if cloneItems[1].DisplayName != "Primary" || cloneItems[1].ShowUptime || cloneItems[1].ShowChart {
		t.Fatalf("the overrides of the source were not copied: %+v", cloneItems[1])
	}

	// --- the linked groups ---------------------------------------------------
	cloneGroups, err := pages.Groups(ctx, clone.ID)
	if err != nil {
		t.Fatalf("reading the copy groups: %v", err)
	}
	if len(cloneGroups) != 1 || cloneGroups[0].GroupID != group.ID ||
		cloneGroups[0].DisplayName != "Edge" || cloneGroups[0].GroupName != group.Name {
		t.Fatalf("the group link did not travel: %+v", cloneGroups)
	}

	// --- what stays behind ----------------------------------------------------
	if got := countRows(t, tx, &models.StatusPageSubscriber{}, "status_page_id = ?", clone.ID); got != 0 {
		t.Fatalf("the copy must inherit no subscriber, got %d", got)
	}
	if got := countRows(t, tx, &models.StatusPageSubscriber{}, "status_page_id = ?", source.ID); got != 1 {
		t.Fatalf("the source must keep its subscriber, got %d", got)
	}
	if got := countRows(t, tx, &models.StatusPageMonitor{}, "status_page_id = ?", source.ID); got != 2 {
		t.Fatalf("the selection of the source must not be repointed, got %d rows", got)
	}

	// The copy is published to the peers ONCE, inside the create it went through.
	if got := countRows(t, tx, &models.SyncOutbox{}, "entity = ? AND uuid = ?",
		models.EntityStatusPage, clone.UUID); got != 1 {
		t.Fatalf("the copy must reach the outbox once, got %d", got)
	}

	// --- the slug collision is resolved with a counter ------------------------
	bare, err := pages.Clone(ctx, source.ID, StatusPageCloneOptions{})
	if err != nil {
		t.Fatalf("cloning twice: %v", err)
	}
	if want := source.Slug + "-copy-2"; bare.Slug != want {
		t.Fatalf("the second copy must get the counter, got %q want %q", bare.Slug, want)
	}
	if bare.Title != source.Title+" (copy)" {
		t.Fatalf("the title carries no counter (titles are not unique), got %q", bare.Title)
	}
	// Both switches off: the copy is a page with the same settings and no selection.
	if items, err := pages.Items(ctx, bare.ID); err != nil {
		t.Fatalf("reading the bare copy selection: %v", err)
	} else if len(items) != 0 {
		t.Fatalf("copy_monitors=false must leave the copy without monitors, got %d", len(items))
	}
	if links, err := pages.Groups(ctx, bare.ID); err != nil {
		t.Fatalf("reading the bare copy groups: %v", err)
	} else if len(links) != 0 {
		t.Fatalf("copy_groups=false must leave the copy without groups, got %d", len(links))
	}

	// --- an unknown source is a not found -------------------------------------
	if _, err := pages.Clone(ctx, 0, StatusPageCloneOptions{}); err == nil {
		t.Fatal("cloning a page that does not exist must fail")
	}
}
