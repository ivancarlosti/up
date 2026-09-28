// This file holds an OPT-IN integration check of the re-notification interval read by
// ClusterService.channelResendInterval (NotificationService.MaxResendInterval, see
// notification_dispatch.go and docs/notifications.md).
//
// It is skipped unless UP_SCRATCH_DSN points at a throwaway MariaDB/MySQL database.
// The statement joins two tables (notifications and monitor_notifications) and filters
// on on_down / active: a unit test cannot tell a query that reads the interval of the
// eligible channels from one with a misspelled table, column or join condition, which
// would quietly return 0 and leave the whole feature dead again. The tables are created
// when they are missing, and every row it writes lives inside a transaction that is
// rolled back at the end. The usual way to run it:
//
//	docker run --rm -d --name up-resend-verify -p 127.0.0.1:3308:3306 \
//	  -e MARIADB_ROOT_PASSWORD=verify -e MARIADB_DATABASE=up \
//	  -e MARIADB_USER=up -e MARIADB_PASSWORD=verify mariadb:11
//	UP_SCRATCH_DSN='up:verify@tcp(127.0.0.1:3308)/up?charset=utf8mb4&parseTime=true&loc=UTC' \
//	  go test ./internal/services/ -run TestNotificationMaxResendIntervalLive -v
//	docker rm -f up-resend-verify
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

// TestNotificationMaxResendIntervalLive pins which channels may set the re-notification
// cadence of a monitor: the active ones whose link asks for "down" alerts, and only the
// largest of their intervals.
func TestNotificationMaxResendIntervalLive(t *testing.T) {
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
	// The tables are created when they are missing, so that a fresh scratch database
	// works; AutoMigrate only ever adds what is absent.
	if err := db.AutoMigrate(&models.Notification{}, &models.Monitor{}, &models.MonitorNotification{}); err != nil {
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

	suffix := time.Now().UTC().Format("20060102150405.000000")
	monitor := &models.Monitor{Name: "zz-verify-resend-" + suffix, Type: models.MonitorTypeHTTP}
	if err := tx.Create(monitor).Error; err != nil {
		t.Fatalf("creating the monitor: %v", err)
	}
	bare := &models.Monitor{Name: "zz-verify-bare-" + suffix, Type: models.MonitorTypeHTTP}
	if err := tx.Create(bare).Error; err != nil {
		t.Fatalf("creating the monitor without channels: %v", err)
	}

	// The intervals are chosen so that only one answer is possible: a lookup that
	// ignored the filters, or the MAX, would return a different number.
	channels := []struct {
		name     string
		interval int
		active   bool
		onDown   bool
	}{
		{name: "zz-verify-alert-" + suffix, interval: 300, active: true, onDown: true},
		{name: "zz-verify-slow-" + suffix, interval: 900, active: true, onDown: true},
		{name: "zz-verify-off-" + suffix, interval: 6000, active: false, onDown: true},
		{name: "zz-verify-up-only-" + suffix, interval: 7200, active: true, onDown: false},
	}
	for _, channel := range channels {
		notification := &models.Notification{
			Name:                  channel.name,
			Type:                  models.NotificationWebhook,
			Active:                channel.active,
			ResendIntervalSeconds: channel.interval,
			Config: models.NotificationConfig{Webhook: &models.WebhookConfig{
				URL: "https://hooks.example.com/up",
			}},
		}
		if err := tx.Create(notification).Error; err != nil {
			t.Fatalf("creating the channel %s: %v", channel.name, err)
		}
		// The zero values are forced through an explicit UPDATE: GORM omits the zero
		// value of a column that carries a `default:` tag, so a false would silently
		// become true. That trap is precisely what the query must survive — the filter
		// is what has to exclude these rows, not the column default.
		if err := tx.Model(&models.Notification{}).Where("id = ?", notification.ID).
			UpdateColumns(map[string]any{"active": channel.active}).Error; err != nil {
			t.Fatalf("setting the state of the channel %s: %v", channel.name, err)
		}
		// A map insert writes on_down verbatim, false included.
		link := map[string]any{
			"monitor_id":      monitor.ID,
			"notification_id": notification.ID,
			"on_down":         channel.onDown,
			"on_up":           true,
		}
		if err := tx.Model(&models.MonitorNotification{}).Create(link).Error; err != nil {
			t.Fatalf("linking the channel %s: %v", channel.name, err)
		}
	}

	service := NewNotificationService(tx, &config.Config{NodeID: "verify"},
		slog.New(slog.NewTextHandler(io.Discard, nil)), nil)

	if got := service.MaxResendInterval(context.Background(), monitor.ID); got != 900 {
		t.Errorf("MaxResendInterval() = %d, want 900 (only the active, on_down channels count)", got)
	}
	// A monitor with no channel must read as "no interval configured", never as a
	// failed lookup that silently disables the configured one.
	if got := service.MaxResendInterval(context.Background(), bare.ID); got != 0 {
		t.Errorf("MaxResendInterval(monitor without channels) = %d, want 0", got)
	}
}
