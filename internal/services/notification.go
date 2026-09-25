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

// NotificationService owns the notification channels (SMTP, Webhook, Slack,
// Discord and Telegram), the delivery log and the dispatch of the status change
// events.
type NotificationService struct {
	db     *gorm.DB
	cfg    *config.Config
	log    *slog.Logger
	engine *notify.Engine
	hub    EventPublisher
	// emit publishes the local writes to the peers (nil outside federated mode).
	emit *SyncEmitter
}

// NewNotificationService builds the notification service.
func NewNotificationService(db *gorm.DB, cfg *config.Config, log *slog.Logger, engine *notify.Engine) *NotificationService {
	return &NotificationService{db: db, cfg: cfg, log: log, engine: engine}
}

// SetPublisher injects the real time publisher.
func (s *NotificationService) SetPublisher(p EventPublisher) { s.hub = p }

// SetSyncEmitter injects the publisher of the synchronisation outbox.
func (s *NotificationService) SetSyncEmitter(emitter *SyncEmitter) { s.emit = emitter }

// publishNotification records the channel in the outbox, inside the caller's
// transaction.
//
// The payload carries the channel configuration, credentials included: the node
// that owns the notification election is the one that sends, so it must hold them.
func (s *NotificationService) publishNotification(ctx context.Context, tx *gorm.DB, id uint) error {
	if s.emit == nil {
		return nil
	}
	return s.emit.EmitNotification(ctx, tx, id)
}

// notificationUUID reads the global identity of a channel (needed before a delete).
func (s *NotificationService) notificationUUID(ctx context.Context, tx *gorm.DB, id uint) (string, error) {
	if s.emit == nil {
		return "", nil
	}
	return s.emit.RowUUID(ctx, tx, models.EntityNotification, id)
}

// notificationLinkMonitors reads the monitors a channel is linked to, so their links
// can be tombstoned before the rows disappear.
func (s *NotificationService) notificationLinkMonitors(ctx context.Context, tx *gorm.DB, id uint) ([]string, error) {
	if s.emit == nil {
		return nil, nil
	}
	return s.emit.MonitorUUIDsOfNotification(ctx, tx, id)
}

// tombstoneNotification publishes the removal of a channel and of its monitor links.
func (s *NotificationService) tombstoneNotification(ctx context.Context, tx *gorm.DB, uuid string, monitorUUIDs []string) error {
	if s.emit == nil || uuid == "" {
		return nil
	}
	if err := s.emit.TombstoneNotificationLinks(ctx, tx, uuid, monitorUUIDs); err != nil {
		return err
	}
	return s.emit.EmitDelete(ctx, tx, models.EntityNotification, uuid)
}

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
	// The identity is stamped here rather than by a later update, so the row, the
	// payload and the response all name the same creator from the start.
	notification.OriginNodeID = s.cfg.NodeID
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(notification).Error; err != nil {
			return err
		}
		return s.publishNotification(ctx, tx, notification.ID)
	}); err != nil {
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
		// Every edit advances the revision: it is the primary component of the
		// merge order (see the group update).
		"revision":   gorm.Expr("revision + 1"),
		"updated_at": time.Now().UTC(),
	}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		result := tx.Model(&models.Notification{}).Where("id = ?", notification.ID).Updates(updates)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return gorm.ErrRecordNotFound
		}
		return s.publishNotification(ctx, tx, notification.ID)
	})
	if err == gorm.ErrRecordNotFound {
		return ErrNotFound(i18n.CodeNotificationNotFound, fmt.Sprintf("notification %d does not exist", notification.ID))
	}
	if err != nil {
		return ErrInternal(err)
	}
	s.log.Info("notification channel updated", "id", notification.ID, "name", notification.Name)
	s.publish("notification.updated", notification)
	return nil
}

// Delete removes a channel together with its links and logs.
func (s *NotificationService) Delete(ctx context.Context, id uint) error {
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// The identity and the linked monitors are read BEFORE the cascade: once the
		// rows are gone they cannot be read, and the tombstones have to name them.
		uuid, err := s.notificationUUID(ctx, tx, id)
		if err != nil {
			return err
		}
		monitorUUIDs, err := s.notificationLinkMonitors(ctx, tx, id)
		if err != nil {
			return err
		}
		if err := tx.Where("notification_id = ?", id).Delete(&models.MonitorNotification{}).Error; err != nil {
			return err
		}
		if err := tx.Where("notification_id = ?", id).Delete(&models.NotificationLog{}).Error; err != nil {
			return err
		}
		if err := tx.Delete(&models.Notification{}, id).Error; err != nil {
			return err
		}
		return s.tombstoneNotification(ctx, tx, uuid, monitorUUIDs)
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

// PurgeOlderThan deletes the delivery history older than the given time.
//
// Unlike the notification de-duplication locks, the delivery history is
// information the operator may want to keep, so the job is opt-in: it only runs
// when NOTIFICATION_LOG_RETENTION_DAYS is greater than zero.
func (s *NotificationService) PurgeOlderThan(ctx context.Context, before time.Time) (int64, error) {
	result := s.db.WithContext(ctx).Where("created_at < ?", before).Delete(&models.NotificationLog{})
	if result.Error != nil {
		return 0, ErrInternal(result.Error)
	}
	return result.RowsAffected, nil
}

func (s *NotificationService) publish(event string, payload any) {
	if s.hub != nil {
		s.hub.Publish(event, payload)
	}
}
