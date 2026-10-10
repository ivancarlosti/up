package services

import (
	"context"
	"fmt"
	"time"

	"github.com/ivancarlosti/up/internal/i18n"
	"github.com/ivancarlosti/up/internal/models"
	"github.com/ivancarlosti/up/internal/notify"
)

// Dispatch delivers an event to every channel linked to the monitor and writes
// a delivery log entry per channel.
func (s *NotificationService) Dispatch(ctx context.Context, monitor *models.Monitor, event models.NotificationEvent,
	status models.AggregateStatus, detail string, latencyMS int64, nodeID string,
	opts ...notify.MessageOption) []models.NotificationLog {

	links, err := s.links(ctx, monitor.ID)
	if err != nil {
		s.log.Error("could not load the notification links", "monitor_id", monitor.ID, "error", err)
		return nil
	}

	message := notify.NewMessage(event, monitor, status, detail, latencyMS, nodeID, s.cfg.AppURL,
		s.groupRef(ctx, monitor.ID), opts...)
	logs := make([]models.NotificationLog, 0, len(links))

	for _, link := range links {
		if !eventEnabled(link, event) {
			continue
		}
		var notification models.Notification
		if err := s.db.WithContext(ctx).First(&notification, link.NotificationID).Error; err != nil {
			continue
		}
		if !notification.Active {
			continue
		}
		logs = append(logs, s.deliver(ctx, &notification, monitor, event, message, nodeID))
	}
	return logs
}

// Test sends a test message through a channel so the operator can validate the
// configuration from the UI.
func (s *NotificationService) Test(ctx context.Context, id uint) (*models.NotificationLog, error) {
	notification, err := s.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	sample := &models.Monitor{
		ID:   0,
		Name: "Up test notification",
		Type: models.MonitorTypeHTTP,
		Config: models.MonitorConfig{
			URL: "https://example.com",
		},
	}
	message := notify.NewMessage(models.EventTest, sample, models.AggregateDown,
		"Test message sent from Admin > Notifications", 123, s.cfg.NodeID, s.cfg.AppURL, nil)
	entry := s.deliver(ctx, notification, sample, models.EventTest, message, s.cfg.NodeID)
	return &entry, nil
}

// deliver performs a single delivery and stores the outcome.
func (s *NotificationService) deliver(ctx context.Context, notification *models.Notification, monitor *models.Monitor,
	event models.NotificationEvent, message notify.Message, nodeID string) models.NotificationLog {

	started := time.Now()
	err := s.engine.Send(notification, message)
	entry := models.NotificationLog{
		NotificationID: notification.ID,
		MonitorID:      monitor.ID,
		Event:          event,
		Success:        err == nil,
		DurationMS:     time.Since(started).Milliseconds(),
		NodeID:         nodeID,
		CreatedAt:      time.Now().UTC(),
	}
	if err != nil {
		entry.Error = trimmed(err.Error(), 900)
		s.log.Warn("notification delivery failed",
			"notification_id", notification.ID, "notification", notification.Name,
			"monitor_id", monitor.ID, "event", event, "error", err)
	} else {
		s.log.Info("notification delivered",
			"notification_id", notification.ID, "notification", notification.Name,
			"monitor_id", monitor.ID, "event", event, "duration_ms", entry.DurationMS)
	}
	if dbErr := s.db.WithContext(ctx).Create(&entry).Error; dbErr != nil {
		s.log.Error("could not store the notification log", "error", dbErr)
	}
	s.publish("notification.log", entry)
	return entry
}

// Logs returns the most recent delivery attempts.
func (s *NotificationService) Logs(ctx context.Context, limit int) ([]models.NotificationLog, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	var logs []models.NotificationLog
	if err := s.db.WithContext(ctx).Order("created_at DESC").Limit(limit).Find(&logs).Error; err != nil {
		return nil, ErrInternal(fmt.Errorf("listing notification logs: %w", err))
	}
	return logs, nil
}

// groupRef returns the single group of a monitor: the id and the name the
// notification bodies report.
//
// It is resolved here, and not read off the monitor, because the monitor a
// notification is about comes from the check that just ran and not from a decorated
// listing. It is passed to notify.NewMessage so every delivery path ends up with the
// same populated payload.
//
// The lookup runs once per Dispatch, not once per channel. A failure is logged and
// ignored (nil): an alert must still be delivered without its group rather than not
// at all.
func (s *NotificationService) groupRef(ctx context.Context, monitorID uint) *models.MonitorGroupRef {
	var row struct {
		ID   uint   `gorm:"column:id"`
		Name string `gorm:"column:name"`
	}
	err := s.db.WithContext(ctx).
		Table("monitor_groups").
		Select("monitor_groups.id, monitor_groups.name").
		Joins("JOIN monitor_group_members ON monitor_group_members.group_id = monitor_groups.id").
		Where("monitor_group_members.monitor_id = ?", monitorID).
		// A monitor belongs to one group. The order makes the pick deterministic
		// even on a database that still holds a duplicate (the boot consolidation
		// removes them): the group that comes first in the group list the operator
		// sees, then its id.
		Order("monitor_groups.sort_order ASC, monitor_groups.id ASC").
		Scan(&row).Error
	if err != nil {
		s.log.Warn("could not load the group of a monitor for a notification",
			"monitor_id", monitorID, "error", err)
		return nil
	}
	if row.ID == 0 {
		return nil
	}
	return &models.MonitorGroupRef{ID: row.ID, Name: row.Name}
}

// MaxResendInterval returns the largest re-notification interval configured on the
// channels that can actually receive a "down" alert for a monitor (0 when no such
// channel is linked, or when it cannot be read).
//
// Only the channels that would be attempted count: Dispatch skips an inactive
// channel and a link that opted out of the event, so letting one of those set the
// cadence of an incident would re-alert the channels that did not ask for it.
// A read failure is logged and reported as 0: the monitor level interval still
// applies, and a notification must not be dropped because a preference could not
// be read.
func (s *NotificationService) MaxResendInterval(ctx context.Context, monitorID uint) int {
	var intervals []int
	err := s.db.WithContext(ctx).
		Table("notifications").
		Joins("JOIN monitor_notifications ON monitor_notifications.notification_id = notifications.id").
		Where("monitor_notifications.monitor_id = ?", monitorID).
		Where("monitor_notifications.on_down = ?", true).
		Where("notifications.active = ?", true).
		Pluck("notifications.resend_interval_seconds", &intervals).Error
	if err != nil {
		s.log.Warn("could not load the resend interval of the linked channels",
			"monitor_id", monitorID, "error", err)
		return 0
	}
	largest := 0
	for _, interval := range intervals {
		if interval > largest {
			largest = interval
		}
	}
	return largest
}

// links loads the monitor -> notification links.
func (s *NotificationService) links(ctx context.Context, monitorID uint) ([]models.MonitorNotification, error) {
	var links []models.MonitorNotification
	if err := s.db.WithContext(ctx).Where("monitor_id = ?", monitorID).Find(&links).Error; err != nil {
		return nil, err
	}
	return links, nil
}

// normalizeEvent converts an aggregated status into a notification event.
func normalizeEvent(status models.AggregateStatus) models.NotificationEvent {
	if status == models.AggregateUp {
		return models.EventUp
	}
	return models.EventDown
}

// eventEnabled tells whether the link opted in for this event.
//
// The certificate and domain events ride on the "notify me when something is
// wrong" flag (on_down): an expiration that is approaching is a problem
// notification, and this way every existing link keeps working without a
// migration.
func eventEnabled(link models.MonitorNotification, event models.NotificationEvent) bool {
	switch event {
	case models.EventUp:
		return link.OnUp
	case models.EventDown, models.EventCertExpiring, models.EventCertExpired,
		models.EventDomainExpiring, models.EventDomainExpired:
		return link.OnDown
	}
	return true
}

// NotifyChannelNotFound is returned when a channel disappears between the
// link lookup and the delivery.
func NotifyChannelNotFound(id uint) *APIError {
	return ErrNotFound(i18n.CodeNotificationNotFound, fmt.Sprintf("notification %d does not exist", id))
}

func trimmed(value string, max int) string {
	if len(value) <= max {
		return value
	}
	return value[:max]
}
