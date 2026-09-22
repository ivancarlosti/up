package services

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"gorm.io/gorm"

	"github.com/ivancarlosti/up/internal/config"
	"github.com/ivancarlosti/up/internal/i18n"
	"github.com/ivancarlosti/up/internal/models"
	"github.com/ivancarlosti/up/internal/notify"
)

// NotificationService owns the notification channels (SMTP and Webhook), the
// delivery log and the dispatch of the status change events.
type NotificationService struct {
	db     *gorm.DB
	cfg    *config.Config
	log    *slog.Logger
	engine *notify.Engine
	hub    EventPublisher
}

// NewNotificationService builds the notification service.
func NewNotificationService(db *gorm.DB, cfg *config.Config, log *slog.Logger, engine *notify.Engine) *NotificationService {
	return &NotificationService{db: db, cfg: cfg, log: log, engine: engine}
}

// SetPublisher injects the real time publisher.
func (s *NotificationService) SetPublisher(p EventPublisher) { s.hub = p }

// List returns every channel with the monitors it is linked to.
func (s *NotificationService) List(ctx context.Context) ([]*models.Notification, error) {
	var notifications []*models.Notification
	if err := s.db.WithContext(ctx).Order("name ASC").Find(&notifications).Error; err != nil {
		return nil, ErrInternal(fmt.Errorf("listing notifications: %w", err))
	}
	var links []models.MonitorNotification
	if err := s.db.WithContext(ctx).Find(&links).Error; err != nil {
		return nil, ErrInternal(err)
	}
	byNotification := map[uint][]uint{}
	for _, link := range links {
		byNotification[link.NotificationID] = append(byNotification[link.NotificationID], link.MonitorID)
	}
	for _, n := range notifications {
		n.MonitorIDs = byNotification[n.ID]
		if n.MonitorIDs == nil {
			n.MonitorIDs = []uint{}
		}
	}
	return notifications, nil
}

// Get loads a single channel.
func (s *NotificationService) Get(ctx context.Context, id uint) (*models.Notification, error) {
	var notification models.Notification
	err := s.db.WithContext(ctx).First(&notification, id).Error
	if err == gorm.ErrRecordNotFound {
		return nil, ErrNotFound(i18n.CodeNotificationNotFound, fmt.Sprintf("notification %d does not exist", id))
	}
	if err != nil {
		return nil, ErrInternal(err)
	}
	monitorIDs, err := s.monitorIDs(ctx, id)
	if err != nil {
		return nil, err
	}
	notification.MonitorIDs = monitorIDs
	return &notification, nil
}

// Create stores a new channel.
func (s *NotificationService) Create(ctx context.Context, notification *models.Notification) error {
	notification.Normalize()
	if problem := notification.Validate(); problem != "" {
		return ErrBadRequest(i18n.CodeNotificationConfig, problem)
	}
	if err := s.db.WithContext(ctx).Create(notification).Error; err != nil {
		return ErrInternal(fmt.Errorf("creating notification: %w", err))
	}
	s.log.Info("notification channel created", "id", notification.ID, "name", notification.Name, "type", notification.Type)
	s.publish("notification.created", notification)
	return nil
}

// Update saves an existing channel.
func (s *NotificationService) Update(ctx context.Context, notification *models.Notification) error {
	notification.Normalize()
	if problem := notification.Validate(); problem != "" {
		return ErrBadRequest(i18n.CodeNotificationConfig, problem)
	}
	// The Config column is written as a marshalled JSON string (GORM only
	// applies "serializer:json" to model fields, not to raw map values).
	configJSON, err := json.Marshal(notification.Config)
	if err != nil {
		return ErrInternal(fmt.Errorf("encoding the notification configuration: %w", err))
	}
	updates := map[string]any{
		"name":                    notification.Name,
		"type":                    notification.Type,
		"active":                  notification.Active,
		"is_default":              notification.IsDefault,
		"resend_interval_seconds": notification.ResendIntervalSeconds,
		"config":                  string(configJSON),
		"updated_at":              time.Now().UTC(),
	}
	result := s.db.WithContext(ctx).Model(&models.Notification{}).Where("id = ?", notification.ID).Updates(updates)
	if result.Error != nil {
		return ErrInternal(result.Error)
	}
	if result.RowsAffected == 0 {
		return ErrNotFound(i18n.CodeNotificationNotFound, fmt.Sprintf("notification %d does not exist", notification.ID))
	}
	s.log.Info("notification channel updated", "id", notification.ID, "name", notification.Name)
	s.publish("notification.updated", notification)
	return nil
}

// Delete removes a channel together with its links and logs.
func (s *NotificationService) Delete(ctx context.Context, id uint) error {
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("notification_id = ?", id).Delete(&models.MonitorNotification{}).Error; err != nil {
			return err
		}
		if err := tx.Where("notification_id = ?", id).Delete(&models.NotificationLog{}).Error; err != nil {
			return err
		}
		return tx.Delete(&models.Notification{}, id).Error
	})
	if err != nil {
		return ErrInternal(fmt.Errorf("deleting notification %d: %w", id, err))
	}
	s.log.Info("notification channel deleted", "id", id)
	s.publish("notification.deleted", map[string]any{"id": id})
	return nil
}

func (s *NotificationService) monitorIDs(ctx context.Context, notificationID uint) ([]uint, error) {
	var links []models.MonitorNotification
	if err := s.db.WithContext(ctx).Where("notification_id = ?", notificationID).Find(&links).Error; err != nil {
		return nil, ErrInternal(err)
	}
	ids := make([]uint, 0, len(links))
	for _, link := range links {
		ids = append(ids, link.MonitorID)
	}
	return ids, nil
}

func (s *NotificationService) publish(event string, payload any) {
	if s.hub != nil {
		s.hub.Publish(event, payload)
	}
}
