// This file holds an OPT-IN integration check of the monitor links a notification
// response carries (NotificationService.Get, see notification.go and the channel
// endpoints of docs/api.md).
//
// It pins the contract the create and update handlers rest on: the request of the
// dialog never carries the links (they are made from the monitor editor), so the
// handler reads the stored channel back and answers with it. Do that read wrong
// and the answer has no `monitor_ids` at all, which is what the card of Admin >
// Notifications reads unguarded - a null threw inside its render and Vue answered
// with an empty placeholder, so an edited channel looked deleted.
//
// It is skipped unless UP_SCRATCH_DSN points at a MariaDB/MySQL database. Every
// row it writes lives inside a transaction that is rolled back at the end, and
// the tables it needs are created when they are missing. The usual way to run it:
//
//	docker run --rm -d --name up-notify-ids-verify -p 127.0.0.1:3310:3306 \
//	  -e MARIADB_ROOT_PASSWORD=verify -e MARIADB_DATABASE=up mariadb:11
//	UP_SCRATCH_DSN='root:verify@tcp(127.0.0.1:3310)/up?charset=utf8mb4&parseTime=true&loc=UTC' \
//	  go test ./internal/services/ -run TestNotificationMonitorIDsLive -v
//	docker rm -f up-notify-ids-verify
//
// Why it needs a real engine: the links are rows of monitor_notifications, so a
// write that left them alone cannot be told from one that dropped them without
// rows to lose, and the wire shape the page depends on (`[]`, never null) is what
// the JSON tag of a nil slice decides.
package services

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"github.com/ivancarlosti/up/internal/config"
	"github.com/ivancarlosti/up/internal/models"
)

// TestNotificationMonitorIDsLive pins what a channel read answers about its links:
// the ids of the monitors that listen to it, as an array - `[]` for a channel
// nobody listens to, never a null the caller has to guard against - and an update
// that leaves that set alone.
func TestNotificationMonitorIDsLive(t *testing.T) {
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
	monitors := make([]*models.Monitor, 0, 2)
	for _, prefix := range []string{"zz-verify-ids-a-", "zz-verify-ids-b-"} {
		monitor := &models.Monitor{Name: prefix + suffix, Type: models.MonitorTypeHTTP}
		if err := tx.Create(monitor).Error; err != nil {
			t.Fatalf("creating the monitor: %v", err)
		}
		monitors = append(monitors, monitor)
	}

	channel := func(name string) *models.Notification {
		return &models.Notification{
			Name:   name + suffix,
			Type:   models.NotificationWebhook,
			Active: true,
			Config: models.NotificationConfig{Webhook: &models.WebhookConfig{
				URL: "https://hooks.example.com/up",
			}},
		}
	}
	listened := channel("zz-verify-ids-listened-")
	if err := tx.Create(listened).Error; err != nil {
		t.Fatalf("creating the listened-to channel: %v", err)
	}
	for _, monitor := range monitors {
		// A map insert writes on_down and on_up verbatim, false included.
		link := map[string]any{
			"monitor_id":      monitor.ID,
			"notification_id": listened.ID,
			"on_down":         true,
			"on_up":           true,
		}
		if err := tx.Model(&models.MonitorNotification{}).Create(link).Error; err != nil {
			t.Fatalf("linking the channel to monitor %d: %v", monitor.ID, err)
		}
	}
	// The channel nobody listens to is the case that used to travel as null.
	bare := channel("zz-verify-ids-bare-")
	if err := tx.Create(bare).Error; err != nil {
		t.Fatalf("creating the unlinked channel: %v", err)
	}

	service := NewNotificationService(tx, &config.Config{NodeID: "verify"},
		slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	ctx := context.Background()

	read, err := service.Get(ctx, listened.ID)
	if err != nil {
		t.Fatalf("reading the listened-to channel: %v", err)
	}
	if len(read.MonitorIDs) != len(monitors) {
		t.Errorf("Get(listened).MonitorIDs = %v, want the %d monitor ids", read.MonitorIDs, len(monitors))
	}
	for _, monitor := range monitors {
		if !holdsMonitorID(read.MonitorIDs, monitor.ID) {
			t.Errorf("Get(listened).MonitorIDs = %v, missing monitor %d", read.MonitorIDs, monitor.ID)
		}
	}
	assertMonitorIDsArray(t, "Get(listened)", read, false)

	if read, err = service.Get(ctx, bare.ID); err != nil {
		t.Fatalf("reading the unlinked channel: %v", err)
	}
	if read.MonitorIDs == nil {
		t.Error("Get(unlinked).MonitorIDs is nil, which the API writes as null")
	}
	if len(read.MonitorIDs) != 0 {
		t.Errorf("Get(unlinked).MonitorIDs = %v, want none", read.MonitorIDs)
	}
	// This is the exact payload the card of the page breaks on: null is not a list,
	// and the render of the card takes the whole channel box down with it.
	assertMonitorIDsArray(t, "Get(unlinked)", read, true)

	// The update leg is the one that regressed on screen: the dialog sends the
	// channel without its links (see AdminNotificationsView.channelPayload), which
	// is why the handler reads the saved row back instead of echoing the request.
	edit := channel("zz-verify-ids-renamed-")
	edit.ID = listened.ID
	if edit.MonitorIDs != nil {
		t.Fatal("the update payload carries links: the premise of the read-back is wrong")
	}
	if err := service.Update(ctx, edit); err != nil {
		t.Fatalf("updating the listened-to channel: %v", err)
	}
	saved, err := service.Get(ctx, listened.ID)
	if err != nil {
		t.Fatalf("reading the updated channel: %v", err)
	}
	if saved.Name != edit.Name {
		t.Errorf("the update did not store the new name: got %q, want %q", saved.Name, edit.Name)
	}
	// The dialog swaps this row into its list as-is, so it has to carry the
	// persisted timestamps rather than the zero values of the request.
	if saved.CreatedAt.IsZero() || saved.UpdatedAt.IsZero() {
		t.Errorf("the saved channel has zero timestamps: created_at=%v updated_at=%v", saved.CreatedAt, saved.UpdatedAt)
	}
	if saved.MonitorIDs == nil {
		t.Error("the saved channel is missing MonitorIDs: the page reads it as a list")
	}
	if len(saved.MonitorIDs) != len(monitors) {
		t.Errorf("Get(after update).MonitorIDs = %v, want the %d monitor ids: the write dropped the links",
			saved.MonitorIDs, len(monitors))
	}
	assertMonitorIDsArray(t, "Get(after update)", saved, false)
}

// holdsMonitorID reports whether ids holds id.
func holdsMonitorID(ids []uint, id uint) bool {
	for _, candidate := range ids {
		if candidate == id {
			return true
		}
	}
	return false
}

// assertMonitorIDsArray fails when the JSON of a channel carries `monitor_ids` as
// anything but an array - a null is what the card of the admin page cannot read -
// and checks the empty case when wantEmpty is set. It reads the encoded field back
// rather than the struct, since the array is what travels.
func assertMonitorIDsArray(t *testing.T, what string, channel *models.Notification, wantEmpty bool) {
	t.Helper()
	payload, err := json.Marshal(channel)
	if err != nil {
		t.Fatalf("encoding the channel: %v", err)
	}
	var wire map[string]json.RawMessage
	if err := json.Unmarshal(payload, &wire); err != nil {
		t.Fatalf("decoding the encoded channel: %v", err)
	}
	raw := string(wire["monitor_ids"])
	if !strings.HasPrefix(raw, "[") {
		t.Errorf("%s encodes monitor_ids as %s, want a JSON array", what, raw)
		return
	}
	if wantEmpty && raw != "[]" {
		t.Errorf("%s encodes monitor_ids as %s, want [] for a channel nobody listens to", what, raw)
	}
}
