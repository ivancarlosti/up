package services

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/ivancarlosti/up/internal/i18n"
	"github.com/ivancarlosti/up/internal/models"
)

// timeNow is the clock used by the write paths (a function so a test can pin it).
var timeNow = func() time.Time { return time.Now().UTC() }

// List returns every system, decorated for the dashboard.
func (s *SystemService) List(ctx context.Context) ([]SystemView, error) {
	var rows []models.System
	if err := s.db.WithContext(ctx).Order("name ASC").Find(&rows).Error; err != nil {
		return nil, ErrInternal(fmt.Errorf("listing systems: %w", err))
	}
	owners, err := s.ownerIndex(ctx)
	if err != nil {
		return nil, err
	}
	ids := make([]uint, 0, len(rows))
	for i := range rows {
		ids = append(ids, rows[i].ID)
	}
	alerts, err := s.alertIndex(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := make([]SystemView, 0, len(rows))
	for i := range rows {
		view := s.decorate(&rows[i], owners[rows[i].ID])
		view.Alerts = alerts[rows[i].ID]
		out = append(out, view)
	}
	return out, nil
}

// Get returns one system with its runtime state.
func (s *SystemService) Get(ctx context.Context, id uint) (*SystemView, error) {
	system, err := s.find(ctx, id)
	if err != nil {
		return nil, err
	}
	owners, err := s.ownerIndex(ctx)
	if err != nil {
		return nil, err
	}
	view := s.decorate(system, owners[system.ID])
	alerts, err := s.alertIndex(ctx, []uint{system.ID})
	if err != nil {
		return nil, err
	}
	view.Alerts = alerts[system.ID]
	return &view, nil
}

// alertIndex reads the alert rules of the given machines (one query). A machine
// with no row yet answers with the defaults, so a GET never writes.
func (s *SystemService) alertIndex(ctx context.Context, ids []uint) (map[uint][]models.SystemAlertRule, error) {
	if s.alerts == nil {
		return nil, nil
	}
	return s.alerts.States(ctx, ids)
}

// find loads a system row or reports the stable not found error.
func (s *SystemService) find(ctx context.Context, id uint) (*models.System, error) {
	var system models.System
	if err := s.db.WithContext(ctx).First(&system, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound(i18n.CodeSystemNotFound, "system not found")
		}
		return nil, ErrInternal(fmt.Errorf("reading system %d: %w", id, err))
	}
	return &system, nil
}

// Create stores a new system and returns it with the generated token.
//
// The token is the only time the agent credential exists in clear: the row keeps
// its hash, which is enough to authenticate the agent and useless as a credential
// of its own.
func (s *SystemService) Create(ctx context.Context, input SystemInput) (*SystemView, error) {
	name := models.NormalizeSystemName(input.Name)
	if name == "" {
		return nil, ErrBadRequest(i18n.CodeValidation, "a system needs a name")
	}
	if len(name) > 120 {
		return nil, ErrBadRequest(i18n.CodeValidation, "the system name is longer than 120 characters")
	}
	token, hash, err := generateAgentToken()
	if err != nil {
		return nil, ErrInternal(err)
	}
	enabled := true
	if input.Enabled != nil {
		enabled = *input.Enabled
	}
	system := models.System{
		UUID:         models.NewSystemUUID(),
		OriginNodeID: s.nodeID(),
		Revision:     1,
		Name:         name,
		Host:         strings.TrimSpace(input.Host),
		Enabled:      enabled,
		TokenHash:    hash,
		TokenPrefix:  tokenPrefix(token),
	}
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&system).Error; err != nil {
			if isDuplicateKey(err) {
				return ErrConflict(i18n.CodeSystemNameTaken, "a system with that name already exists")
			}
			return fmt.Errorf("creating the system: %w", err)
		}
		// Every machine starts with the default alert rules: the evaluator must
		// have a row to claim, and the dialog opens on something sensible.
		if s.alerts != nil {
			if err := s.alerts.EnsureDefaults(ctx, tx, system.ID); err != nil {
				return err
			}
		}
		return s.emit(ctx, tx, system.ID)
	}); err != nil {
		return nil, AsAPIError(err)
	}
	s.publish("system.created", map[string]any{"id": system.ID, "uuid": system.UUID})
	view, err := s.Get(ctx, system.ID)
	if err != nil {
		return nil, err
	}
	view.Token = token
	view.InstallCommand = s.InstallCommand(view.Host, token)
	view.AgentKey = s.agentKeyForCommand()
	return view, nil
}

// Update saves the editable fields of a system.
func (s *SystemService) Update(ctx context.Context, id uint, input SystemInput) (*SystemView, error) {
	system, err := s.find(ctx, id)
	if err != nil {
		return nil, err
	}
	columns := map[string]any{
		"updated_at": timeNow(),
		"revision":   gorm.Expr("revision + 1"),
		"host":       strings.TrimSpace(input.Host),
	}
	if name := models.NormalizeSystemName(input.Name); name != "" && name != system.Name {
		columns["name"] = name
	}
	disabled := false
	if input.Enabled != nil && *input.Enabled != system.Enabled {
		columns["enabled"] = *input.Enabled
		disabled = !*input.Enabled
	}
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&models.System{}).Where("id = ?", id).Updates(columns).Error; err != nil {
			if isDuplicateKey(err) {
				return ErrConflict(i18n.CodeSystemNameTaken, "a system with that name already exists")
			}
			return fmt.Errorf("updating the system: %w", err)
		}
		return s.emit(ctx, tx, id)
	}); err != nil {
		return nil, AsAPIError(err)
	}
	// Disabling a system is also "stop talking to it": the agent connection and
	// the lease go away, so no node keeps polling it.
	if disabled {
		s.Detach(id, "the system was disabled")
	}
	view, err := s.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	s.publish("system.updated", map[string]any{"id": id, "uuid": view.UUID})
	return view, nil
}

// Delete removes a system and everything it stored.
func (s *SystemService) Delete(ctx context.Context, id uint) error {
	system, err := s.find(ctx, id)
	if err != nil {
		return err
	}
	s.Detach(id, "the system was deleted")
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// The links are tombstoned before the row: a relation is addressed by its
		// two ends, so its tombstone has to name a system that still exists.
		if s.sync != nil {
			var links []models.SystemNotification
			if err := tx.WithContext(ctx).Where("system_id = ?", id).Find(&links).Error; err != nil {
				return fmt.Errorf("reading the alert channels of system %d: %w", id, err)
			}
			for i := range links {
				if err := s.sync.EmitSystemNotification(ctx, tx, system.UUID, links[i].NotificationID, &links[i], false); err != nil {
					return err
				}
			}
		}
		for _, model := range []any{
			&models.SystemMetric{}, &models.SystemContainer{},
			&models.SystemContainerMetric{}, &models.SystemSmart{},
			&models.SystemLease{},
			&models.SystemAlert{}, &models.SystemNotification{},
		} {
			if err := tx.Where("system_id = ?", id).Delete(model).Error; err != nil {
				return fmt.Errorf("removing the history of system %d: %w", id, err)
			}
		}
		if err := tx.Delete(&models.System{}, id).Error; err != nil {
			return fmt.Errorf("removing system %d: %w", id, err)
		}
		if s.sync != nil {
			return s.sync.EmitDelete(ctx, tx, models.EntitySystem, system.UUID)
		}
		return nil
	}); err != nil {
		return AsAPIError(err)
	}
	s.publish("system.deleted", map[string]any{"id": id, "uuid": system.UUID})
	return nil
}

// RotateToken replaces the agent token of a system.
//
// The agent is disconnected as a side effect: it keeps presenting the old token,
// is refused, and reconnects once the operator restarted it with the new one.
// That is the intended behaviour of a credential rotation.
func (s *SystemService) RotateToken(ctx context.Context, id uint) (*SystemView, error) {
	if _, err := s.find(ctx, id); err != nil {
		return nil, err
	}
	token, hash, err := generateAgentToken()
	if err != nil {
		return nil, ErrInternal(err)
	}
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&models.System{}).Where("id = ?", id).Updates(map[string]any{
			"token_hash":   hash,
			"token_prefix": tokenPrefix(token),
			"fingerprint":  "",
			"updated_at":   timeNow(),
			"revision":     gorm.Expr("revision + 1"),
		}).Error; err != nil {
			return fmt.Errorf("rotating the token: %w", err)
		}
		return s.emit(ctx, tx, id)
	}); err != nil {
		return nil, AsAPIError(err)
	}
	s.Detach(id, "the agent token was rotated")
	view, err := s.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	view.Token = token
	view.InstallCommand = s.InstallCommand(view.Host, token)
	view.AgentKey = s.agentKeyForCommand()
	return view, nil
}

// emit publishes the system for the federated peers (no-op otherwise).
func (s *SystemService) emit(ctx context.Context, tx *gorm.DB, id uint) error {
	if s.sync == nil {
		return nil
	}
	return s.sync.EmitSystem(ctx, tx, id)
}

// publish broadcasts a real time event.
func (s *SystemService) publish(event string, payload any) {
	if s.hub != nil {
		s.hub.Publish(event, payload)
	}
}

// nodeID is this node identity.
func (s *SystemService) nodeID() string {
	if s.cfg == nil {
		return ""
	}
	return s.cfg.NodeID
}
