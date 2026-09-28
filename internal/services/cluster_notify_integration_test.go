// This file holds an OPT-IN integration check of the notification pipeline end to end
// (EvaluateAndNotify -> claim -> Dispatch -> the request the target receives) on a
// single node. It is the regression test of the reported symptom, "alerts never fire":
//
//   - an incident evaluated while no channel is linked must NOT be recorded as
//     reported, so the alert goes out as soon as a channel is linked while the monitor
//     is still down (before the fix the incident stayed silent for its whole duration);
//   - the query of the webhook URL (the token a target authenticates with) must arrive;
//   - the channel level resend_interval_seconds must actually repeat the alert.
//
// It is skipped unless UP_SCRATCH_DSN points at a throwaway MariaDB/MySQL database: it
// creates the tables it needs (AutoMigrate only adds what is absent) and rolls back its
// rows, but it does write to the database. The usual way to run it:
//
//	docker run --rm -d --name up-pipeline-verify -p 127.0.0.1:3308:3306 \
//	  -e MARIADB_ROOT_PASSWORD=verify -e MARIADB_DATABASE=up \
//	  -e MARIADB_USER=up -e MARIADB_PASSWORD=verify mariadb:11
//	UP_SCRATCH_DSN='up:verify@tcp(127.0.0.1:3308)/up?charset=utf8mb4&parseTime=true&loc=UTC' \
//	  go test ./internal/services/ -run TestNotificationPipelineLive -v
//	docker rm -f up-pipeline-verify
package services

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"sync"
	"testing"
	"time"

	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"github.com/ivancarlosti/up/internal/config"
	"github.com/ivancarlosti/up/internal/models"
	"github.com/ivancarlosti/up/internal/notify"
)

// TestNotificationPipelineLive drives the real pipeline of one node, from the
// aggregated state to the HTTP request.
func TestNotificationPipelineLive(t *testing.T) {
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
	// Only the tables the pipeline touches, so a fresh scratch database works.
	if err := db.AutoMigrate(
		&models.Node{}, &models.Setting{}, &models.ClusterSettings{},
		&models.Monitor{}, &models.Heartbeat{}, &models.MonitorState{},
		&models.MonitorGroup{}, &models.MonitorGroupMember{},
		&models.Notification{}, &models.MonitorNotification{},
		&models.NotificationLock{}, &models.NotificationLog{},
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

	// The target: it records what it was asked for, so the assertions are about the
	// request that really left the process.
	type received struct {
		method string
		token  string
		query  url.Values
	}
	var (
		mu       sync.Mutex
		captured []received
	)
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		captured = append(captured, received{method: r.Method, token: r.URL.Query().Get("token"), query: r.URL.Query()})
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()
	sent := func() []received {
		mu.Lock()
		defer mu.Unlock()
		return append([]received(nil), captured...)
	}

	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	nodeID := "verify"
	suffix := time.Now().UTC().Format("20060102150405.000000")
	now := time.Now().UTC()

	// One online node, one monitor that is failing, and the state row of an incident
	// that was never reported (notified_at is NULL).
	if err := tx.Create(&models.Node{
		Name: "verify", NodeID: nodeID, Status: models.NodeStatusOnline, IsPrimary: true, LastHeartbeat: &now,
	}).Error; err != nil {
		t.Fatalf("creating the node: %v", err)
	}
	monitor := &models.Monitor{
		Name: "zz-verify-pipeline-" + suffix, Type: models.MonitorTypeHTTP, Active: true,
		IntervalSeconds: 60, Config: models.MonitorConfig{URL: "https://api.example.com/health"},
	}
	if err := tx.Create(monitor).Error; err != nil {
		t.Fatalf("creating the monitor: %v", err)
	}
	if err := tx.Create(&models.Heartbeat{
		MonitorID: monitor.ID, NodeID: nodeID, Status: models.StatusDown, Important: true,
		Message: "connection refused", CreatedAt: now,
	}).Error; err != nil {
		t.Fatalf("creating the heartbeat: %v", err)
	}
	if err := tx.Create(&models.MonitorState{
		MonitorID: monitor.ID, Status: models.AggregateDown, ChangedAt: now.Add(-time.Hour),
		LastCheckAt: &now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("creating the monitor state: %v", err)
	}

	cfg := &config.Config{NodeID: nodeID, AppURL: "https://up.example.com"}
	notifications := NewNotificationService(tx, cfg, log, notify.NewEngine(log, 5*time.Second))
	cluster := NewClusterService(tx, cfg, log, NewSettingService(tx, cfg, log), NewStatsService(tx))
	cluster.SetNotificationService(notifications)

	loadState := func() models.MonitorState {
		t.Helper()
		var state models.MonitorState
		if err := tx.Where("monitor_id = ?", monitor.ID).First(&state).Error; err != nil {
			t.Fatalf("loading the monitor state: %v", err)
		}
		return state
	}
	// nextWindow clears the de-duplication locks a previous evaluation left behind, so
	// the next call is the next claim window (a real run waits for the window to roll).
	nextWindow := func() {
		t.Helper()
		if err := tx.Where("monitor_id = ?", monitor.ID).Delete(&models.NotificationLock{}).Error; err != nil {
			t.Fatalf("clearing the notification locks: %v", err)
		}
	}

	// Phase 1: the incident is evaluated while the monitor has no linked channel.
	if err := cluster.EvaluateAndNotify(ctx, monitor.ID); err != nil {
		t.Fatalf("evaluating the incident: %v", err)
	}
	if got := sent(); len(got) != 0 {
		t.Errorf("no channel is linked, so the target must not be called, got %d requests", len(got))
	}
	if state := loadState(); state.NotifiedAt != nil {
		t.Errorf("an incident with no eligible channel must stay re-evaluable, notified_at = %v", state.NotifiedAt)
	} else if state.Status != models.AggregateDown {
		t.Errorf("the aggregated status must still be recorded as down, got %q", state.Status)
	}

	// Phase 2: the operator links a channel while the monitor is down. The alert must go
	// out on the next evaluation, and the token of the URL must reach the target.
	channel := &models.Notification{
		Name: "zz-verify-hook-" + suffix, Type: models.NotificationWebhook, Active: true,
		Config: models.NotificationConfig{Webhook: &models.WebhookConfig{
			URL:          target.URL + "/hook?token=abc123",
			Method:       "POST",
			ContentType:  "application/json",
			BodyTemplate: models.DefaultWebhookBodyTemplate,
		}},
	}
	if err := tx.Create(channel).Error; err != nil {
		t.Fatalf("creating the channel: %v", err)
	}
	if err := tx.Create(&models.MonitorNotification{
		MonitorID: monitor.ID, NotificationID: channel.ID, OnDown: true, OnUp: true,
	}).Error; err != nil {
		t.Fatalf("linking the channel: %v", err)
	}
	nextWindow()
	if err := cluster.EvaluateAndNotify(ctx, monitor.ID); err != nil {
		t.Fatalf("evaluating the incident with a channel: %v", err)
	}
	requests := sent()
	if len(requests) != 1 {
		t.Fatalf("the alert must be delivered as soon as a channel is linked, got %d requests", len(requests))
	}
	if requests[0].token != "abc123" {
		t.Errorf("the target must receive the token of the webhook URL, got %q (%v)", requests[0].token, requests[0].query)
	}
	if state := loadState(); state.NotifiedAt == nil {
		t.Error("a delivered incident must be recorded as reported")
	}

	// Phase 3: the channel interval decides the repeats. With both intervals at 0 the
	// clock alone must not repeat an alert...
	nextWindow()
	if err := cluster.EvaluateAndNotify(ctx, monitor.ID); err != nil {
		t.Fatalf("evaluating the incident again: %v", err)
	}
	if got := sent(); len(got) != 1 {
		t.Errorf("no resend interval is configured, so no repeat is owed, got %d requests", len(got))
	}

	// ...and once the channel asks for a repeat that is due, it is sent.
	if err := tx.Model(&models.Notification{}).Where("id = ?", channel.ID).
		UpdateColumns(map[string]any{"resend_interval_seconds": 300}).Error; err != nil {
		t.Fatalf("setting the channel interval: %v", err)
	}
	if err := tx.Model(&models.MonitorState{}).Where("monitor_id = ?", monitor.ID).
		UpdateColumns(map[string]any{"notified_at": now.Add(-10 * time.Minute)}).Error; err != nil {
		t.Fatalf("backdating the notification clock: %v", err)
	}
	nextWindow()
	if err := cluster.EvaluateAndNotify(ctx, monitor.ID); err != nil {
		t.Fatalf("evaluating the incident after the interval: %v", err)
	}
	if got := sent(); len(got) != 2 {
		t.Fatalf("the channel interval must repeat the alert once it elapses, got %d requests", len(got))
	}
	if token := sent()[1].token; token != "abc123" {
		t.Errorf("the repeated alert must carry the token too, got %q", token)
	}
}
