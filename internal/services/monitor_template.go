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
}

// NewMonitorTemplateService builds the template service.
func NewMonitorTemplateService(db *gorm.DB, cfg *config.Config, log *slog.Logger) *MonitorTemplateService {
	return &MonitorTemplateService{db: db, cfg: cfg, log: log}
}

// SetPublisher injects the real time publisher.
func (s *MonitorTemplateService) SetPublisher(p EventPublisher) { s.hub = p }

// SetMonitorService injects the monitor service (used to apply a template).
func (s *MonitorTemplateService) SetMonitorService(m *MonitorService) { s.mono = m }

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
	if err := s.db.WithContext(ctx).Create(template).Error; err != nil {
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
	result := s.db.WithContext(ctx).Model(&models.MonitorTemplate{}).Where("id = ?", template.ID).Updates(map[string]any{
		"name":        template.Name,
		"description": template.Description,
		"type":        template.Type,
		"config":      template.Config,
		"defaults":    template.Defaults,
		"updated_at":  time.Now().UTC(),
	})
	if result.Error != nil {
		return ErrInternal(result.Error)
	}
	if result.RowsAffected == 0 {
		return ErrNotFound(i18n.CodeMonitorTemplateNotFound, fmt.Sprintf("monitor template %d does not exist", template.ID))
	}
	s.log.Info("monitor template updated", "id", template.ID, "name", template.Name)
	s.publish("monitor.template.updated", template)
	return nil
}

// Delete removes a template (the monitors created from it stay).
func (s *MonitorTemplateService) Delete(ctx context.Context, id uint) error {
	result := s.db.WithContext(ctx).Delete(&models.MonitorTemplate{}, id)
	if result.Error != nil {
		return ErrInternal(result.Error)
	}
	if result.RowsAffected == 0 {
		return ErrNotFound(i18n.CodeMonitorTemplateNotFound, fmt.Sprintf("monitor template %d does not exist", id))
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
