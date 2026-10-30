package database_test

import (
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"github.com/ivancarlosti/up/internal/database"
	"github.com/ivancarlosti/up/internal/models"
)

// TestStatusPageSubscriptionSchema is an opt-in check of the subscription tables
// against a throwaway MariaDB/MySQL database:
//
//	docker run --rm -d --name up-subscription-verify -p 127.0.0.1:3307:3306 \
//	  -e MARIADB_ROOT_PASSWORD=verify -e MARIADB_DATABASE=up \
//	  -e MARIADB_USER=up -e MARIADB_PASSWORD=verify mariadb:11
//	UP_SCRATCH_DSN='up:verify@tcp(127.0.0.1:3307)/up?charset=utf8mb4&parseTime=true&loc=UTC' \
//	  go test ./internal/database/ -run TestStatusPageSubscriptionSchema -v
//
// It pins the parts the unit tests cannot: the deterministic subscriber identity
// coexists with the (page, email) unique index, an incident can be closed and a
// NEW one opened for the same page+monitor (only the open key is unique), and a
// subscriber receives at most one notice per kind per incident.
func TestStatusPageSubscriptionSchema(t *testing.T) {
	dsn := os.Getenv("UP_SCRATCH_DSN")
	if dsn == "" {
		t.Skip("UP_SCRATCH_DSN is not set: point it at a throwaway database to run this check")
	}
	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{
		Logger: gormlogger.Default.LogMode(gormlogger.Silent),
	})
	if err != nil {
		t.Fatalf("connecting: %v", err)
	}
	for _, model := range []any{
		&models.StatusPageSubscriber{}, &models.SubscriptionNotice{}, &models.SubscriptionIncident{},
	} {
		if err := db.Migrator().DropTable(model); err != nil {
			t.Fatalf("dropping the scratch table: %v", err)
		}
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := database.Migrate(db, logger); err != nil {
		t.Fatalf("migrating: %v", err)
	}

	page := models.StatusPage{Slug: "subscription-verify", Title: "Subscription verify"}
	if err := db.Create(&page).Error; err != nil {
		t.Fatalf("creating the page: %v", err)
	}
	subscriber := models.StatusPageSubscriber{
		UUID:                 models.StatusPageSubscriberUUID(page.UUID, "person@example.com"),
		StatusPageID:         page.ID,
		Email:                "person@example.com",
		Locale:               "en-US",
		UnsubscribeTokenHash: "hash",
	}
	if err := db.Create(&subscriber).Error; err != nil {
		t.Fatalf("creating the subscriber: %v", err)
	}
	duplicate := subscriber
	duplicate.ID = 0
	duplicate.UUID = "another-uuid"
	if err := db.Create(&duplicate).Error; err == nil {
		t.Fatal("two subscribers of the same page and address were accepted")
	}

	openKey := "page:monitor"
	first := models.SubscriptionIncident{
		StatusPageID: page.ID, MonitorID: 1, OpenKey: &openKey, OpenedAt: time.Now().UTC(),
	}
	if err := db.Create(&first).Error; err != nil {
		t.Fatalf("opening the first incident: %v", err)
	}
	secondOpen := first
	secondOpen.ID = 0
	if err := db.Create(&secondOpen).Error; err == nil {
		t.Fatal("two open incidents of the same page+monitor were accepted")
	}
	if err := db.Model(&models.SubscriptionIncident{}).Where("id = ?", first.ID).
		Updates(map[string]any{"open_key": nil, "closed_at": time.Now().UTC()}).Error; err != nil {
		t.Fatalf("closing the incident: %v", err)
	}
	second := models.SubscriptionIncident{
		StatusPageID: page.ID, MonitorID: 1, OpenKey: &openKey, OpenedAt: time.Now().UTC(),
	}
	if err := db.Create(&second).Error; err != nil {
		t.Fatalf("opening a new incident after closing the first: %v", err)
	}

	notice := models.SubscriptionNotice{
		IncidentID: first.ID, SubscriberID: subscriber.ID, Kind: models.SubscriptionNoticeDown,
		StatusPageID: page.ID, MonitorID: 1, Status: models.SubscriptionNoticePending,
		ScheduledAt: time.Now().UTC(),
	}
	if err := db.Create(&notice).Error; err != nil {
		t.Fatalf("creating the notice: %v", err)
	}
	duplicateNotice := notice
	duplicateNotice.ID = 0
	if err := db.Create(&duplicateNotice).Error; err == nil {
		t.Fatal("two notices of the same incident, subscriber and kind were accepted")
	}
}
