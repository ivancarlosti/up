// This file holds an OPT-IN integration check of the template CLONE
// (MonitorTemplateService.Clone, see monitor_template.go and docs/monitors.md):
// the copy of a template keeps the definition and the two optional attachments,
// gets a **fresh identity** (uuid, revision, origin), the followers of the source
// stay with the source, the name collision is resolved with a counter, and the
// clone reaches the synchronisation outbox like any other create — without a
// second code path for the peers.
//
// It is skipped unless UP_SCRATCH_DSN points at a MariaDB/MySQL database. Every
// row it writes lives inside a transaction that is rolled back at the end. The
// database must already be migrated (the application creates the schema on boot).
// The usual way to run it:
//
//	docker run --rm -d --name up-clone-verify -p 127.0.0.1:3309:3306 \
//	  -e MARIADB_ROOT_PASSWORD=verify -e MARIADB_DATABASE=up \
//	  -e MARIADB_USER=up -e MARIADB_PASSWORD=verify mariadb:11
//	UP_SCRATCH_DSN='up:verify@tcp(127.0.0.1:3309)/up?charset=utf8mb4&parseTime=true&loc=UTC' \
//	  go test ./internal/services/ -run TestTemplateCloneLive -v
//	docker rm -f up-clone-verify
//
// Why it needs a real engine: the name of the copy is settled by a
// `LOWER(name) = LOWER(?)` query over the live table, the identity is stamped by
// the GORM hooks of a real insert, and "the copy follows no monitor" is a
// statement about the rows of another table — none of which a pure helper can
// show.
package services

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"

	"github.com/ivancarlosti/up/internal/config"
	"github.com/ivancarlosti/up/internal/models"
)

// countRows counts the rows a query selects inside the scratch transaction.
func countRows(t *testing.T, tx *gorm.DB, model any, query string, args ...any) int64 {
	t.Helper()
	var count int64
	if err := tx.Model(model).Where(query, args...).Count(&count).Error; err != nil {
		t.Fatalf("counting rows (%s): %v", query, err)
	}
	return count
}

// TestTemplateCloneLive pins the clone of a template end to end.
func TestTemplateCloneLive(t *testing.T) {
	tx := scratchTx(t)
	ctx := context.Background()
	suffix := time.Now().UTC().Format("20060102150405.000000")
	// Federated on purpose: the copy must reach the outbox like any other
	// create, so the peers learn about it without a second code path.
	cfg := &config.Config{NodeID: "verify", ClusterMode: config.ClusterModeFederated}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	templates := NewMonitorTemplateService(tx, cfg, log)
	templates.SetSyncEmitter(NewSyncEmitter(tx, cfg, log))
	monitors := NewMonitorService(tx, cfg, log, nil)

	channel := &models.Notification{Name: "zz-clone-channel-" + suffix, Type: models.NotificationWebhook}
	if err := tx.Create(channel).Error; err != nil {
		t.Fatalf("creating the channel: %v", err)
	}

	// The source: a probe, its defaults, a channel and a remembered tag scope.
	source := &models.MonitorTemplate{
		Name:        "zz-clone-" + suffix,
		Description: "the usual probe",
		Type:        models.MonitorTypeHTTP,
		Propagate:   true,
		Config:      models.MonitorConfig{Method: "HEAD", AcceptedStatusCodes: "200-299"},
		Defaults: models.TemplateDefaults{
			IntervalSeconds: 120,
			TimeoutSeconds:  20,
			NotificationIDs: []uint{channel.ID},
		},
		LinkScope: models.TemplateLinkScope{Kind: models.TemplateScopeTags, Tags: []string{"zz-scope-" + suffix}},
	}
	if err := templates.Create(ctx, source); err != nil {
		t.Fatalf("creating the source: %v", err)
	}

	// A monitor that follows the source: the clone must never steal it.
	follower := &models.Monitor{
		Name:            "zz-clone-follower-" + suffix,
		Type:            models.MonitorTypeHTTP,
		Active:          false,
		IntervalSeconds: 120,
		TimeoutSeconds:  20,
		TemplateUUID:    source.UUID,
		Config:          models.MonitorConfig{URL: "http://127.0.0.1:9/", Method: "HEAD"},
	}
	if err := monitors.Create(ctx, follower, nil, nil); err != nil {
		t.Fatalf("creating the follower: %v", err)
	}

	// --- the full copy -------------------------------------------------------
	clone, err := templates.Clone(ctx, source.ID, MonitorTemplateCloneOptions{CopyChannels: true, CopyScope: true})
	if err != nil {
		t.Fatalf("cloning the template: %v", err)
	}
	if clone.ID == source.ID || clone.UUID == "" || clone.UUID == source.UUID {
		t.Fatalf("the copy must carry its own identity: clone=%d/%s source=%d/%s",
			clone.ID, clone.UUID, source.ID, source.UUID)
	}
	if clone.Revision != 1 || clone.OriginNodeID != "verify" {
		t.Fatalf("the copy must be stamped like a fresh create: revision=%d origin=%q",
			clone.Revision, clone.OriginNodeID)
	}
	if clone.Name != source.Name+" (copy)" {
		t.Fatalf("the copy must be named after the source, got %q", clone.Name)
	}
	if clone.Type != source.Type || clone.Description != source.Description ||
		clone.Config.Method != "HEAD" || clone.Config.AcceptedStatusCodes != "200-299" ||
		clone.Defaults.IntervalSeconds != 120 || clone.Defaults.TimeoutSeconds != 20 ||
		!clone.Propagate {
		t.Fatalf("the definition must travel with the copy: %+v", clone)
	}
	if len(clone.Defaults.NotificationIDs) != 1 || clone.Defaults.NotificationIDs[0] != channel.ID {
		t.Fatalf("copy_channels=true must keep the channels: %v", clone.Defaults.NotificationIDs)
	}
	if clone.LinkScope.Kind != models.TemplateScopeTags || len(clone.LinkScope.Tags) != 1 {
		t.Fatalf("copy_scope=true must keep the scope: %+v", clone.LinkScope)
	}

	// Nothing was repointed: the follower of the source is still the follower of
	// the source, and the copy has none of its own.
	if following := countRows(t, tx, &models.Monitor{}, "template_uuid = ?", source.UUID); following != 1 {
		t.Fatalf("the source must keep its follower, got %d", following)
	}
	if followsCopy := countRows(t, tx, &models.Monitor{}, "template_uuid = ?", clone.UUID); followsCopy != 0 {
		t.Fatalf("the copy must start with no follower, got %d", followsCopy)
	}

	// The copy is published to the peers, inside the create it went through.
	if outbox := countRows(t, tx, &models.SyncOutbox{}, "entity = ? AND uuid = ?", models.EntityMonitorTemplate, clone.UUID); outbox != 1 {
		t.Fatalf("the copy must reach the outbox once, got %d", outbox)
	}

	// --- the name collision is resolved with a counter ------------------------
	second, err := templates.Clone(ctx, source.ID, MonitorTemplateCloneOptions{})
	if err != nil {
		t.Fatalf("cloning twice: %v", err)
	}
	if want := source.Name + " (copy) (2)"; second.Name != want {
		t.Fatalf("the second copy must get the counter, got %q want %q", second.Name, want)
	}
	// A name that only differs in case is taken too: the requested name is upper
	// case and the stored one is not, so the LOWER() comparison is what keeps the
	// table free of a duplicate name.
	upper, err := templates.Clone(ctx, source.ID, MonitorTemplateCloneOptions{Name: strings.ToUpper(source.Name)})
	if err != nil {
		t.Fatalf("cloning with a colliding name: %v", err)
	}
	if want := strings.ToUpper(source.Name) + " (2)"; upper.Name != want {
		t.Fatalf("a name that only differs in case must be treated as taken, got %q want %q", upper.Name, want)
	}

	// --- the two switches off -------------------------------------------------
	bare, err := templates.Clone(ctx, source.ID, MonitorTemplateCloneOptions{Name: "zz-clone-bare-" + suffix})
	if err != nil {
		t.Fatalf("cloning without the attachments: %v", err)
	}
	if len(bare.Defaults.NotificationIDs) != 0 {
		t.Fatalf("copy_channels=false must leave the copy without channels: %v", bare.Defaults.NotificationIDs)
	}
	if bare.LinkScope.Kind != models.TemplateScopeType {
		t.Fatalf("copy_scope=false must start the copy on the type scope, got %+v", bare.LinkScope)
	}

	// --- an unknown source is a not found -------------------------------------
	if _, err := templates.Clone(ctx, 0, MonitorTemplateCloneOptions{}); err == nil {
		t.Fatal("cloning a template that does not exist must fail")
	}
}
