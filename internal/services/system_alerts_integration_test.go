// This file holds an OPT-IN integration check of the hardware alert rules: the
// default rules a machine starts with, the save that must keep a rule switched
// OFF, the latch that exactly one caller wins, and the delivery of the four
// events to the channels that opted into them.
//
// It is skipped unless UP_SCRATCH_DSN points at a throwaway MariaDB/MySQL
// database: it creates the tables it needs and rolls its rows back, but it does
// write to the database. The usual way to run it:
//
//	docker run --rm -d --name up-alerts-verify -p 127.0.0.1:3310:3306 \
//	  -e MARIADB_ROOT_PASSWORD=verify -e MARIADB_DATABASE=up \
//	  -e MARIADB_USER=up -e MARIADB_PASSWORD=verify mariadb:11
//	UP_SCRATCH_DSN='up:verify@tcp(127.0.0.1:3310)/up?charset=utf8mb4&parseTime=true&loc=UTC' \
//	  go test ./internal/services/ -run TestSystemAlertsLive -v
//	docker rm -f up-alerts-verify
package services

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"github.com/ivancarlosti/up/internal/config"
	"github.com/ivancarlosti/up/internal/models"
	"github.com/ivancarlosti/up/internal/notify"
)

// TestSystemAlertsLive drives the alert rules, the latch and the dispatch of one
// machine against a real database.
func TestSystemAlertsLive(t *testing.T) {
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
	if err := db.AutoMigrate(
		&models.System{}, &models.SystemMetric{}, &models.SystemAlert{}, &models.SystemNotification{},
		&models.Notification{}, &models.NotificationLog{},
	); err != nil {
		t.Fatalf("migrating the scratch database: %v", err)
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
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	suffix := time.Now().UTC().Format("20060102150405.000000")
	cfg := &config.Config{NodeID: "verify", AppURL: "https://up.example"}

	systemSvc := NewSystemService(tx, cfg, log)
	notifications := NewNotificationService(tx, cfg, log, notify.NewEngine(log, 5*time.Second))
	alerts := NewSystemAlertService(tx, cfg, log)
	alerts.SetSystemService(systemSvc)
	alerts.SetNotificationService(notifications)
	systemSvc.SetAlertService(alerts)

	system := &models.System{
		UUID: models.NewSystemUUID(), Name: "zz-verify-alerts-" + suffix,
		Enabled: true, TokenHash: "hash-" + suffix, Revision: 1,
	}
	if err := tx.Create(system).Error; err != nil {
		t.Fatalf("creating the system: %v", err)
	}
	// --- the defaults a machine starts with ---------------------------------
	view, err := alerts.View(ctx, system.ID)
	if err != nil {
		t.Fatalf("reading the default rules: %v", err)
	}
	if len(view.Rules) != len(models.AllSystemAlertMetrics) {
		t.Fatalf("got %d rules, want %d", len(view.Rules), len(models.AllSystemAlertMetrics))
	}
	for _, rule := range view.Rules {
		spec, ok := models.SystemAlertSpecOf(rule.Metric)
		if !ok {
			t.Fatalf("%s is not in the catalogue", rule.Metric)
		}
		if rule.Enabled != spec.DefaultEnabled || rule.MinMinutes != spec.DefaultMinutes || rule.Threshold != spec.DefaultThreshold {
			t.Errorf("%s default = %+v", rule.Metric, rule)
		}
	}
	// The view carries the catalogue and the defaults the reset restores, so the
	// dialog never keeps a second copy of the table.
	if len(view.Specs) != len(models.AllSystemAlertMetrics) {
		t.Errorf("the view carries %d specs, want %d", len(view.Specs), len(models.AllSystemAlertMetrics))
	}
	if len(view.Defaults.Rules) != len(models.AllSystemAlertMetrics) {
		t.Errorf("the view carries %d defaults, want %d", len(view.Defaults.Rules), len(models.AllSystemAlertMetrics))
	}

	// --- a save keeps a rule switched OFF (the default:true trap) ------------
	saved, err := alerts.Save(ctx, system.ID, SystemAlertsInput{
		Rules: []models.SystemAlertRule{
			{Metric: models.SystemAlertCPU, Enabled: false, Threshold: 70, MinMinutes: 3},
			{Metric: models.SystemAlertDisk, Enabled: true, Threshold: 90, MinMinutes: 10},
		},
	})
	if err != nil {
		t.Fatalf("saving the rules: %v", err)
	}
	byMetric := map[models.SystemAlertMetric]models.SystemAlertRule{}
	for _, rule := range saved.Rules {
		byMetric[rule.Metric] = rule
	}
	if cpu := byMetric[models.SystemAlertCPU]; cpu.Enabled || cpu.Threshold != 70 || cpu.MinMinutes != 3 {
		t.Errorf("the cpu rule was not stored as sent: %+v", cpu)
	}
	if disk := byMetric[models.SystemAlertDisk]; !disk.Enabled || disk.Threshold != 90 || disk.MinMinutes != 10 {
		t.Errorf("the disk rule was not stored as sent: %+v", disk)
	}
	// An omitted metric keeps its stored rule: a save patches the rules it names.
	if memory := byMetric[models.SystemAlertMemory]; !memory.Enabled {
		t.Errorf("the memory rule was reset: %+v", memory)
	}

	// --- the latch: exactly one caller wins ---------------------------------
	first, err := alerts.Claim(ctx, system.ID, models.SystemAlertCPU)
	if err != nil || !first {
		t.Fatalf("the first claim = %v, %v", first, err)
	}
	if second, err := alerts.Claim(ctx, system.ID, models.SystemAlertCPU); err != nil || second {
		t.Fatalf("the second claim = %v, %v (both callers won the edge)", second, err)
	}
	if triggered, err := alerts.Triggered(ctx, system.ID, models.SystemAlertCPU); err != nil || !triggered {
		t.Fatalf("Triggered = %v, %v", triggered, err)
	}
	if released, err := alerts.Release(ctx, system.ID, models.SystemAlertCPU); err != nil || !released {
		t.Fatalf("the first release = %v, %v", released, err)
	}
	if released, err := alerts.Release(ctx, system.ID, models.SystemAlertCPU); err != nil || released {
		t.Fatalf("the second release = %v, %v (a recovery nobody saw was announced)", released, err)
	}

	// --- the channels and the four events -----------------------------------
	var (
		mu       sync.Mutex
		received []string
	)
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The body is what carries the event: the header values of a webhook are
		// static configuration, only the body template is rendered.
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		received = append(received, string(body))
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()
	taken := func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), received...)
	}

	channel := &models.Notification{
		UUID: uuid.NewString(), Name: "zz-verify-channel-" + suffix,
		Type: models.NotificationWebhook, Active: true,
		Config: models.NotificationConfig{Webhook: &models.WebhookConfig{
			URL:    target.URL,
			Method: http.MethodPost,
			// An empty template makes the body the default JSON payload, which is
			// what a consumer parses.
			BodyTemplate: "",
		}},
	}
	if err := tx.Create(channel).Error; err != nil {
		t.Fatalf("creating the channel: %v", err)
	}

	// Down off, up on, threshold on, recovered off.
	if _, err := alerts.Save(ctx, system.ID, SystemAlertsInput{
		Channels: []SystemChannelInput{{
			NotificationID: channel.ID,
			OnDown:         false, OnUp: true, OnThreshold: true, OnRecovered: false,
		}},
	}); err != nil {
		t.Fatalf("linking the channel: %v", err)
	}

	downRule := models.SystemAlertRule{Metric: models.SystemAlertDown, Enabled: true, MinMinutes: 5}
	logs := notifications.DispatchSystem(ctx, system, models.EventSystemDown, downRule, 0, "silent", "verify")
	if len(logs) != 0 || len(taken()) != 0 {
		t.Fatalf("a channel that opted out of down was attempted: %d logs, %v", len(logs), taken())
	}

	threshold := models.SystemAlertRule{Metric: models.SystemAlertCPU, Enabled: true, Threshold: 80, MinMinutes: 5}
	logs = notifications.DispatchSystem(ctx, system, models.EventSystemThreshold, threshold, 92, "92%", "verify")
	if len(logs) != 1 {
		t.Fatalf("threshold deliveries = %d, want 1", len(logs))
	}
	if !logs[0].Success || logs[0].SystemID != system.ID || logs[0].MonitorID != 0 {
		t.Errorf("the delivery log does not name the machine: %+v", logs[0])
	}
	if logs[0].Event != models.EventSystemThreshold {
		t.Errorf("the delivery log event = %s", logs[0].Event)
	}
	got := taken()
	if len(got) != 1 || !strings.Contains(got[0], `"event":"system_threshold"`) {
		t.Errorf("the target received %v, want one system_threshold", got)
	}
	if !strings.Contains(got[0], `"system":"`+system.Name+`"`) || !strings.Contains(got[0], `"metric":"cpu"`) {
		t.Errorf("the payload does not name the machine and the rule: %s", got[0])
	}

	// --- the evaluation of a sample -----------------------------------------
	// The cpu rule was saved switched OFF above, so the evaluation has nothing to
	// fire until it is turned back on: that is the rule the boxes and the latch
	// both read.
	if _, err := alerts.Save(ctx, system.ID, SystemAlertsInput{
		Rules: []models.SystemAlertRule{{Metric: models.SystemAlertCPU, Enabled: true, Threshold: 80, MinMinutes: 5}},
	}); err != nil {
		t.Fatalf("enabling the cpu rule: %v", err)
	}

	// Six minutes of samples above the cpu threshold: the evaluation must claim
	// the latch; below it, the evaluation releases it (and the channel opted out
	// of the recovery, so the target must not hear about it).
	now := time.Now().UTC()
	for i := 0; i < 6; i++ {
		if err := tx.Create(&models.SystemMetric{
			SystemID: system.ID, CreatedAt: now.Add(-time.Duration(i) * time.Minute), CPU: 95,
		}).Error; err != nil {
			t.Fatalf("creating a sample: %v", err)
		}
	}
	alerts.EvaluateSample(ctx, system.ID, now)
	if triggered, err := alerts.Triggered(ctx, system.ID, models.SystemAlertCPU); err != nil || !triggered {
		t.Fatalf("the cpu rule did not fire on a sustained 95%%: %v, %v", triggered, err)
	}
	if err := tx.Model(&models.SystemMetric{}).Where("system_id = ?", system.ID).
		Update("cpu", 10).Error; err != nil {
		t.Fatalf("lowering the samples: %v", err)
	}
	alerts.EvaluateSample(ctx, system.ID, now)
	if triggered, err := alerts.Triggered(ctx, system.ID, models.SystemAlertCPU); err != nil || triggered {
		t.Fatalf("the cpu rule is still firing after recovering: %v, %v", triggered, err)
	}
	if got := taken(); len(got) != 1 {
		t.Errorf("the target received %v, want the recovery to stay silent (on_recovered off)", got)
	}

	// --- copying the rules of another machine -------------------------------
	other := &models.System{
		UUID: models.NewSystemUUID(), Name: "zz-verify-source-" + suffix,
		Enabled: true, TokenHash: "hash-source-" + suffix, Revision: 1,
	}
	if err := tx.Create(other).Error; err != nil {
		t.Fatalf("creating the source system: %v", err)
	}
	if _, err := alerts.Save(ctx, other.ID, SystemAlertsInput{Rules: []models.SystemAlertRule{
		{Metric: models.SystemAlertDisk, Enabled: true, Threshold: 55, MinMinutes: 2},
		{Metric: models.SystemAlertContainer, Enabled: true, Threshold: 3},
	}}); err != nil {
		t.Fatalf("saving the source rules: %v", err)
	}
	copied, err := alerts.CopyFrom(ctx, system.ID, other.ID)
	if err != nil {
		t.Fatalf("copying the rules: %v", err)
	}
	byMetric = map[models.SystemAlertMetric]models.SystemAlertRule{}
	for _, rule := range copied.Rules {
		byMetric[rule.Metric] = rule
	}
	if disk := byMetric[models.SystemAlertDisk]; !disk.Enabled || disk.Threshold != 55 || disk.MinMinutes != 2 {
		t.Errorf("the disk rule was not copied: %+v", disk)
	}
	// A state rule has no window, whatever the source stored.
	if container := byMetric[models.SystemAlertContainer]; container.Threshold != 3 || container.MinMinutes != 0 {
		t.Errorf("the container rule was not copied: %+v", container)
	}
	// A rule the source never configured arrives at its default, not missing.
	if memory := byMetric[models.SystemAlertMemory]; !memory.Enabled || memory.Threshold != models.DefaultSystemAlertThreshold {
		t.Errorf("the memory rule did not fall back to the default: %+v", memory)
	}
	if _, err := alerts.CopyFrom(ctx, system.ID, system.ID); err == nil {
		t.Error("a machine copied its own rules")
	}
	if _, err := alerts.CopyFrom(ctx, system.ID, 0); err == nil {
		t.Error("a copy without a source was accepted")
	}

	// --- resetting to the defaults ------------------------------------------
	// A firing rule stays firing across the reset: the latch is the observation
	// of this node, not configuration, and the next evaluation is what releases
	// it (and sends the recovery).
	if _, err := alerts.Claim(ctx, system.ID, models.SystemAlertDisk); err != nil {
		t.Fatalf("claiming the disk rule: %v", err)
	}
	reset, err := alerts.ResetToDefaults(ctx, system.ID)
	if err != nil {
		t.Fatalf("resetting the rules: %v", err)
	}
	for _, rule := range reset.Rules {
		spec, ok := models.SystemAlertSpecOf(rule.Metric)
		if !ok {
			t.Fatalf("%s is not in the catalogue", rule.Metric)
		}
		if rule.Enabled != spec.DefaultEnabled || rule.Threshold != spec.DefaultThreshold || rule.MinMinutes != spec.DefaultMinutes {
			t.Errorf("%s after the reset = %+v", rule.Metric, rule)
		}
	}
	if triggered, err := alerts.Triggered(ctx, system.ID, models.SystemAlertDisk); err != nil || !triggered {
		t.Errorf("the reset cleared the latch: %v, %v", triggered, err)
	}
}
