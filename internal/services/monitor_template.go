package services

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"gorm.io/gorm"

	"github.com/ivancarlosti/up/internal/config"
	"github.com/ivancarlosti/up/internal/i18n"
	"github.com/ivancarlosti/up/internal/models"
)

// MonitorTemplateService owns the reusable monitor blueprints.
type MonitorTemplateService struct {
	db   *gorm.DB
	cfg  *config.Config
	log  *slog.Logger
	hub  EventPublisher
	mono *MonitorService
	// emit publishes the local writes to the peers (nil outside federated mode).
	emit *SyncEmitter
}

// NewMonitorTemplateService builds the template service.
func NewMonitorTemplateService(db *gorm.DB, cfg *config.Config, log *slog.Logger) *MonitorTemplateService {
	return &MonitorTemplateService{db: db, cfg: cfg, log: log}
}

// SetPublisher injects the real time publisher.
func (s *MonitorTemplateService) SetPublisher(p EventPublisher) { s.hub = p }

// SetMonitorService injects the monitor service (used to apply a template).
func (s *MonitorTemplateService) SetMonitorService(m *MonitorService) { s.mono = m }

// SetSyncEmitter injects the publisher of the synchronisation outbox.
func (s *MonitorTemplateService) SetSyncEmitter(emitter *SyncEmitter) { s.emit = emitter }

// publishTemplate records the template in the outbox, inside the caller's
// transaction.
func (s *MonitorTemplateService) publishTemplate(ctx context.Context, tx *gorm.DB, id uint) error {
	if s.emit == nil {
		return nil
	}
	return s.emit.EmitMonitorTemplate(ctx, tx, id)
}

// templateUUID reads the global identity of a template (needed before a delete).
func (s *MonitorTemplateService) templateUUID(ctx context.Context, tx *gorm.DB, id uint) (string, error) {
	if s.emit == nil {
		return "", nil
	}
	return s.emit.RowUUID(ctx, tx, models.EntityMonitorTemplate, id)
}

// tombstoneTemplate publishes the removal of a template.
func (s *MonitorTemplateService) tombstoneTemplate(ctx context.Context, tx *gorm.DB, uuid string) error {
	if s.emit == nil || uuid == "" {
		return nil
	}
	return s.emit.EmitDelete(ctx, tx, models.EntityMonitorTemplate, uuid)
}

func (s *MonitorTemplateService) publish(event string, payload any) {
	if s.hub != nil {
		s.hub.Publish(event, payload)
	}
}

// List returns every template (newest name order = alphabetical).
func (s *MonitorTemplateService) List(ctx context.Context) ([]*models.MonitorTemplate, error) {
	var templates []*models.MonitorTemplate
	if err := s.db.WithContext(ctx).Order("name ASC").Find(&templates).Error; err != nil {
		return nil, ErrInternal(fmt.Errorf("listing monitor templates: %w", err))
	}
	return templates, nil
}

// Get loads one template.
func (s *MonitorTemplateService) Get(ctx context.Context, id uint) (*models.MonitorTemplate, error) {
	var template models.MonitorTemplate
	err := s.db.WithContext(ctx).First(&template, id).Error
	if err == gorm.ErrRecordNotFound {
		return nil, ErrNotFound(i18n.CodeMonitorTemplateNotFound, fmt.Sprintf("monitor template %d does not exist", id))
	}
	if err != nil {
		return nil, ErrInternal(fmt.Errorf("loading monitor template %d: %w", id, err))
	}
	return &template, nil
}

// Create stores a template.
func (s *MonitorTemplateService) Create(ctx context.Context, template *models.MonitorTemplate) error {
	template.Normalize()
	if problem := template.Validate(); problem != "" {
		return ErrBadRequest(i18n.CodeMonitorTemplateInvalid, problem)
	}
	if err := s.validateName(ctx, template.Name, 0); err != nil {
		return err
	}
	// The identity is stamped here rather than by a later update, so the row, the
	// payload and the response all name the same creator from the start.
	template.OriginNodeID = s.cfg.NodeID
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(template).Error; err != nil {
			return err
		}
		return s.publishTemplate(ctx, tx, template.ID)
	}); err != nil {
		return ErrInternal(fmt.Errorf("creating monitor template: %w", err))
	}
	s.log.Info("monitor template created", "id", template.ID, "name", template.Name, "type", template.Type)
	s.publish("monitor.template.created", template)
	return nil
}

// Update saves an existing template.
func (s *MonitorTemplateService) Update(ctx context.Context, template *models.MonitorTemplate) error {
	template.Normalize()
	if problem := template.Validate(); problem != "" {
		return ErrBadRequest(i18n.CodeMonitorTemplateInvalid, problem)
	}
	if err := s.validateName(ctx, template.Name, template.ID); err != nil {
		return err
	}
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		result := tx.Model(&models.MonitorTemplate{}).Where("id = ?", template.ID).Updates(map[string]any{
			"name":        template.Name,
			"description": template.Description,
			"type":        template.Type,
			"config":      template.Config,
			"defaults":    template.Defaults,
			// Every edit advances the revision: it is the primary component of the
			// merge order (see the group update).
			"revision":   gorm.Expr("revision + 1"),
			"updated_at": time.Now().UTC(),
		})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return gorm.ErrRecordNotFound
		}
		return s.publishTemplate(ctx, tx, template.ID)
	})
	if err == gorm.ErrRecordNotFound {
		return ErrNotFound(i18n.CodeMonitorTemplateNotFound, fmt.Sprintf("monitor template %d does not exist", template.ID))
	}
	if err != nil {
		return ErrInternal(err)
	}
	s.log.Info("monitor template updated", "id", template.ID, "name", template.Name)
	s.publish("monitor.template.updated", template)
	return nil
}

// Delete removes a template (the monitors created from it stay).
func (s *MonitorTemplateService) Delete(ctx context.Context, id uint) error {
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// The identity is read BEFORE the delete: once the row is gone its uuid
		// cannot be read any more, and the tombstone has to name it.
		uuid, err := s.templateUUID(ctx, tx, id)
		if err != nil {
			return err
		}
		result := tx.Delete(&models.MonitorTemplate{}, id)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return gorm.ErrRecordNotFound
		}
		return s.tombstoneTemplate(ctx, tx, uuid)
	})
	if err == gorm.ErrRecordNotFound {
		return ErrNotFound(i18n.CodeMonitorTemplateNotFound, fmt.Sprintf("monitor template %d does not exist", id))
	}
	if err != nil {
		return ErrInternal(err)
	}
	s.log.Info("monitor template deleted", "id", id)
	s.publish("monitor.template.deleted", map[string]any{"id": id})
	return nil
}

// validateName enforces the unique name.
func (s *MonitorTemplateService) validateName(ctx context.Context, name string, currentID uint) error {
	if name == "" {
		return ErrBadRequest(i18n.CodeMonitorTemplateInvalid, "name is required")
	}
	var existing models.MonitorTemplate
	query := s.db.WithContext(ctx).Where("LOWER(name) = LOWER(?)", name)
	if currentID > 0 {
		query = query.Where("id <> ?", currentID)
	}
	switch err := query.First(&existing).Error; {
	case err == nil:
		return ErrConflict(i18n.CodeMonitorTemplateInvalid, fmt.Sprintf("a template named %q already exists", existing.Name))
	case err != gorm.ErrRecordNotFound:
		return ErrInternal(err)
	}
	return nil
}
