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
	status models.AggregateStatus, detail string, latencyMS int64, nodeID string) []models.NotificationLog {

	links, err := s.links(ctx, monitor.ID)
	if err != nil {
		s.log.Error("could not load the notification links", "monitor_id", monitor.ID, "error", err)
		return nil
	}

	message := notify.NewMessage(event, monitor, status, detail, latencyMS, nodeID, s.cfg.AppURL)
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
		"Test message sent from Admin > Notifications", 123, s.cfg.NodeID, s.cfg.AppURL)
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
// The certificate events ride on the "notify me when something is wrong" flag
// (on_down): a certificate that is about to expire is a problem notification, and
// this way every existing link keeps working without a migration.
func eventEnabled(link models.MonitorNotification, event models.NotificationEvent) bool {
	switch event {
	case models.EventUp:
		return link.OnUp
	case models.EventDown, models.EventCertExpiring, models.EventCertExpired:
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
