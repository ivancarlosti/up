package services

import (
	"context"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"github.com/ivancarlosti/up/internal/models"
)

// TestStatusPageSubscriptionRecoveryLive drives the real incident lifecycle of a
// status page against a scratch MariaDB and checks what each subscriber is
// queued: a recovery mail exists only where the outage mail was delivered (or is
// owed), and it never leaves before it.
//
// Opt-in: point UP_SCRATCH_DSN at a throwaway database.
func TestStatusPageSubscriptionRecoveryLive(t *testing.T) {
	dsn := os.Getenv("UP_SCRATCH_DSN")
	if dsn == "" {
		t.Skip("UP_SCRATCH_DSN is not set: point it at a throwaway database to run this check")
	}
	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{
		NowFunc:                func() time.Time { return time.Now().UTC() },
		SkipDefaultTransaction: true,
		Logger:                 gormlogger.Default.LogMode(gormlogger.Silent),
	})
	if err != nil {
		t.Fatalf("connecting: %v", err)
	}
	// Only the tables this check touches, so a fresh scratch database works. The
	// monitors table is migrated but left empty on purpose (see step 4).
	if err := db.AutoMigrate(
		&models.StatusPage{}, &models.StatusPageMonitor{}, &models.StatusPageGroupLink{},
		&models.MonitorGroupMember{}, &models.Notification{}, &models.Monitor{},
		&models.StatusPageSubscriber{}, &models.SubscriptionIncident{}, &models.SubscriptionNotice{},
	); err != nil {
		t.Fatalf("migrating the scratch database: %v", err)
	}

	ctx := context.Background()
	suffix := time.Now().UTC().Format("20060102150405.000000")
	base := time.Now().UTC().Truncate(time.Second)
	const delay = 10 // minutes: the page's subscription_delay_minutes
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	service := NewStatusPageSubscriptionService(db, nil, log)

	// The SMTP channel the pages name. PublicAvailability (and therefore the
	// delivery gate under test) requires it to exist, be active and carry a config.
	channelUUID := uuid.NewString()
	channel := &models.Notification{
		UUID:   channelUUID,
		Name:   "zz-verify-subscription-" + suffix,
		Type:   models.NotificationSMTP,
		Active: true,
		Config: models.NotificationConfig{SMTP: &models.SMTPConfig{
			Host: "localhost", Port: 25, From: "status@example.com",
		}},
	}
	if err := db.Create(channel).Error; err != nil {
		t.Fatalf("creating the SMTP channel: %v", err)
	}

	newPage := func(name string) *models.StatusPage {
		page := &models.StatusPage{
			Slug:                     "zz-verify-subscription-" + name + "-" + suffix,
			Title:                    "verify subscription " + name,
			SubscriptionEnabled:      true,
			SubscriptionSMTPUUID:     channelUUID,
			SubscriptionDelayMinutes: delay,
		}
		if err := db.Create(page).Error; err != nil {
			t.Fatalf("creating the %s page: %v", name, err)
		}
		return page
	}
	publish := func(page *models.StatusPage, monitorID uint) {
		link := &models.StatusPageMonitor{StatusPageID: page.ID, MonitorID: monitorID}
		if err := db.Create(link).Error; err != nil {
			t.Fatalf("publishing monitor %d: %v", monitorID, err)
		}
	}
	subscribe := func(page *models.StatusPage, name string) *models.StatusPageSubscriber {
		now := time.Now().UTC()
		subscriber := &models.StatusPageSubscriber{
			StatusPageID: page.ID,
			Email:        "zz-verify-" + name + "-" + suffix + "@example.com",
			Locale:       "en-US",
			ConfirmedAt:  &now,
		}
		if err := db.Create(subscriber).Error; err != nil {
			t.Fatalf("creating the subscriber %s: %v", name, err)
		}
		return subscriber
	}
	incidentOf := func(pageID, monitorID uint) models.SubscriptionIncident {
		var incident models.SubscriptionIncident
		if err := db.Where("status_page_id = ? AND monitor_id = ?", pageID, monitorID).
			Order("id DESC").First(&incident).Error; err != nil {
			t.Fatalf("loading the incident of monitor %d: %v", monitorID, err)
		}
		return incident
	}
	findNotice := func(incidentID, subscriberID uint, kind string) *models.SubscriptionNotice {
		var notice models.SubscriptionNotice
		err := db.Where("incident_id = ? AND subscriber_id = ? AND kind = ?", incidentID, subscriberID, kind).
			First(&notice).Error
		if err == gorm.ErrRecordNotFound {
			return nil
		}
		if err != nil {
			t.Fatalf("loading the %s notice of incident %d: %v", kind, incidentID, err)
		}
		return &notice
	}

	pages := []*models.StatusPage{}
	defer func() {
		ids := make([]uint, 0, len(pages))
		for _, page := range pages {
			ids = append(ids, page.ID)
		}
		if err := db.Where("status_page_id IN ?", ids).Delete(&models.SubscriptionNotice{}).Error; err != nil {
			t.Errorf("cleaning the notices: %v", err)
		}
		if err := db.Where("status_page_id IN ?", ids).Delete(&models.SubscriptionIncident{}).Error; err != nil {
			t.Errorf("cleaning the incidents: %v", err)
		}
		if err := db.Where("status_page_id IN ?", ids).Delete(&models.StatusPageSubscriber{}).Error; err != nil {
			t.Errorf("cleaning the subscribers: %v", err)
		}
		if err := db.Where("status_page_id IN ?", ids).Delete(&models.StatusPageMonitor{}).Error; err != nil {
			t.Errorf("cleaning the page links: %v", err)
		}
		if err := db.Where("id IN ?", ids).Delete(&models.StatusPage{}).Error; err != nil {
			t.Errorf("cleaning the pages: %v", err)
		}
		if err := db.Where("uuid = ?", channelUUID).Delete(&models.Notification{}).Error; err != nil {
			t.Errorf("cleaning the channel: %v", err)
		}
	}()

	// 1. A blip shorter than the delay: neither mail leaves. This is the reported
	// bug — the recovery mail used to be queued for every subscriber.
	shortPage := newPage("short")
	pages = append(pages, shortPage)
	const shortMonitor = 900001
	publish(shortPage, shortMonitor)
	shortSub := subscribe(shortPage, "short")
	if err := service.ObserveTransition(ctx, shortMonitor, models.AggregateUp, models.AggregateDown, base); err != nil {
		t.Fatalf("opening the short incident: %v", err)
	}
	recoveredEarly := base.Add(2 * time.Minute)
	if err := service.ObserveTransition(ctx, shortMonitor, models.AggregateDown, models.AggregateUp, recoveredEarly); err != nil {
		t.Fatalf("closing the short incident: %v", err)
	}
	shortIncident := incidentOf(shortPage.ID, shortMonitor)
	if shortIncident.ClosedAt == nil {
		t.Fatal("the short incident should be closed")
	}
	if up := findNotice(shortIncident.ID, shortSub.ID, models.SubscriptionNoticeUp); up != nil {
		t.Fatalf("a 2 minute outage must not queue a recovery mail (notice %d, status %q)", up.ID, up.Status)
	}
	shortDown := findNotice(shortIncident.ID, shortSub.ID, models.SubscriptionNoticeDown)
	if shortDown == nil {
		t.Fatal("the outage mail of the short incident should be queued")
	}
	if shortDown.Status != models.SubscriptionNoticePending {
		t.Fatalf("the queued outage mail should stay pending until its gate decides, got %q", shortDown.Status)
	}
	// The closure alone does not cancel it: the delivery gate does, and only
	// because the delay had not elapsed when the monitor recovered.
	if err := service.deliverSubscriptionNotice(ctx, shortDown, base.Add(delay*time.Minute)); err != nil {
		t.Fatalf("delivering the premature outage mail: %v", err)
	}
	shortDown = findNotice(shortIncident.ID, shortSub.ID, models.SubscriptionNoticeDown)
	if shortDown.Status != models.SubscriptionNoticeSkipped {
		t.Fatalf("an outage mail whose delay never elapsed must be skipped, got %q", shortDown.Status)
	}

	// 2. An outage that outlived the delay: the outage mail went out, so the
	// recovery mail is queued and scheduled at the recovery.
	longPage := newPage("long")
	pages = append(pages, longPage)
	const longMonitor = 900002
	publish(longPage, longMonitor)
	longSub := subscribe(longPage, "long")
	if err := service.ObserveTransition(ctx, longMonitor, models.AggregateUp, models.AggregateDown, base); err != nil {
		t.Fatalf("opening the long incident: %v", err)
	}
	longIncident := incidentOf(longPage.ID, longMonitor)
	longDown := findNotice(longIncident.ID, longSub.ID, models.SubscriptionNoticeDown)
	if longDown == nil {
		t.Fatal("the outage mail of the long incident should be queued")
	}
	if err := db.Model(&models.SubscriptionNotice{}).Where("id = ?", longDown.ID).
		Updates(map[string]any{
			"status":  models.SubscriptionNoticeSent,
			"sent_at": base.Add(delay * time.Minute),
		}).Error; err != nil {
		t.Fatalf("marking the outage mail as delivered: %v", err)
	}
	recovered := base.Add(12 * time.Minute)
	if err := service.ObserveTransition(ctx, longMonitor, models.AggregateDown, models.AggregateUp, recovered); err != nil {
		t.Fatalf("closing the long incident: %v", err)
	}
	longUp := findNotice(longIncident.ID, longSub.ID, models.SubscriptionNoticeUp)
	if longUp == nil {
		t.Fatal("a subscriber that received the outage mail must receive the recovery mail")
	}
	if !longUp.ScheduledAt.Equal(recovered) {
		t.Fatalf("the recovery mail should be scheduled at the recovery (%s), got %s", recovered, longUp.ScheduledAt)
	}

	// 3. A subscriber that confirmed while the monitor was already down has no
	// story to close, so it is not told that the monitor is back.
	midPage := newPage("mid")
	pages = append(pages, midPage)
	const midMonitor = 900003
	publish(midPage, midMonitor)
	if err := service.ObserveTransition(ctx, midMonitor, models.AggregateUp, models.AggregateDown, base); err != nil {
		t.Fatalf("opening the mid incident: %v", err)
	}
	midSub := subscribe(midPage, "mid")
	midIncident := incidentOf(midPage.ID, midMonitor)
	if err := service.ObserveTransition(ctx, midMonitor, models.AggregateDown, models.AggregateUp, base.Add(20*time.Minute)); err != nil {
		t.Fatalf("closing the mid incident: %v", err)
	}
	if up := findNotice(midIncident.ID, midSub.ID, models.SubscriptionNoticeUp); up != nil {
		t.Fatalf("a subscriber that never received the outage mail must not receive the recovery mail (notice %d)", up.ID)
	}

	// 4. An outage mail whose delay elapsed before the recovery, but that the
	// scheduler had not picked up yet, is still owed: it goes out, and the
	// recovery mail waits behind it instead of overtaking it.
	owedPage := newPage("owed")
	pages = append(pages, owedPage)
	const owedMonitor = 900004
	publish(owedPage, owedMonitor)
	owedSub := subscribe(owedPage, "owed")
	owedClosedAt := base.Add(delay*time.Minute + 30*time.Second)
	if err := service.ObserveTransition(ctx, owedMonitor, models.AggregateUp, models.AggregateDown, base); err != nil {
		t.Fatalf("opening the owed incident: %v", err)
	}
	if err := service.ObserveTransition(ctx, owedMonitor, models.AggregateDown, models.AggregateUp, owedClosedAt); err != nil {
		t.Fatalf("closing the owed incident: %v", err)
	}
	owedIncident := incidentOf(owedPage.ID, owedMonitor)
	owedDown := findNotice(owedIncident.ID, owedSub.ID, models.SubscriptionNoticeDown)
	if owedDown == nil || owedDown.Status != models.SubscriptionNoticePending {
		t.Fatalf("the owed outage mail should still be pending, got %+v", owedDown)
	}
	owedUp := findNotice(owedIncident.ID, owedSub.ID, models.SubscriptionNoticeUp)
	if owedUp == nil {
		t.Fatal("a subscriber whose delay had already elapsed is owed the recovery mail too")
	}
	if err := service.deliverSubscriptionNotice(ctx, owedUp, owedClosedAt); err != nil {
		t.Fatalf("delivering the waiting recovery mail: %v", err)
	}
	owedUp = findNotice(owedIncident.ID, owedSub.ID, models.SubscriptionNoticeUp)
	if owedUp.Status != models.SubscriptionNoticePending {
		t.Fatalf("the recovery mail must stay pending while its outage mail has not left, got %q", owedUp.Status)
	}
	if owedUp.NextAttemptAt == nil || !owedUp.NextAttemptAt.After(owedClosedAt) {
		t.Fatalf("the recovery mail should be postponed past the recovery, got next_attempt_at %v", owedUp.NextAttemptAt)
	}
	// The outage mail passes the incident gate: this fixture has no monitor row, so
	// it stops right after checking the incident. That is the point — the closure
	// did not cancel it.
	if err := service.deliverSubscriptionNotice(ctx, owedDown, owedClosedAt); err != nil {
		t.Fatalf("delivering the owed outage mail: %v", err)
	}
	owedDown = findNotice(owedIncident.ID, owedSub.ID, models.SubscriptionNoticeDown)
	if owedDown.Status != models.SubscriptionNoticeSkipped || !strings.Contains(owedDown.LastError, "monitor") {
		t.Fatalf("the owed outage mail should pass the incident gate and stop at the monitor, got %q (%q)",
			owedDown.Status, owedDown.LastError)
	}
	// With the outage mail gone for good, the recovery mail gives up: the
	// subscriber would otherwise be told "back up" without ever hearing "down".
	if err := service.deliverSubscriptionNotice(ctx, owedUp, owedClosedAt); err != nil {
		t.Fatalf("delivering the abandoned recovery mail: %v", err)
	}
	owedUp = findNotice(owedIncident.ID, owedSub.ID, models.SubscriptionNoticeUp)
	if owedUp.Status != models.SubscriptionNoticeSkipped {
		t.Fatalf("the recovery mail must be skipped when its outage mail never left, got %q", owedUp.Status)
	}
}
