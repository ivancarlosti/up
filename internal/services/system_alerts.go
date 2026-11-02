package services

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/ivancarlosti/up/internal/config"
	"github.com/ivancarlosti/up/internal/i18n"
	"github.com/ivancarlosti/up/internal/models"
)

// SystemAlertService owns the alert configuration of the hardware monitored
// machines: one rule per metric, the channels those rules notify, and the local
// latch that says which rule is firing right now.
//
// The rules and the links are CONFIGURATION and travel to the peers (see
// models.SystemPayload.Alerts and models.EntitySystemNotification). The latch is
// not: it is the observation of the node that held the agent connection, and it
// is claimed and released in the shared database so exactly one node dispatches
// an edge even when the connection moves (see system_alert_eval.go).
type SystemAlertService struct {
	db       *gorm.DB
	cfg      *config.Config
	log      *slog.Logger
	systems  *SystemService
	notifier *NotificationService
	hub      EventPublisher
}

// NewSystemAlertService builds the alert service of the hardware monitoring.
func NewSystemAlertService(db *gorm.DB, cfg *config.Config, log *slog.Logger) *SystemAlertService {
	return &SystemAlertService{db: db, cfg: cfg, log: log}
}

// SetSystemService injects the machine service: it is how a save bumps the
// revision of the system and publishes it to the peers.
func (s *SystemAlertService) SetSystemService(systems *SystemService) { s.systems = systems }

// SetNotificationService injects the delivery service.
func (s *SystemAlertService) SetNotificationService(n *NotificationService) { s.notifier = n }

// SetPublisher injects the real time publisher.
func (s *SystemAlertService) SetPublisher(p EventPublisher) { s.hub = p }

// SystemChannelInput is one machine-to-channel link as the dialog sends it.
type SystemChannelInput struct {
	NotificationID uint `json:"notification_id"`
	OnDown         bool `json:"on_down"`
	OnUp           bool `json:"on_up"`
	OnThreshold    bool `json:"on_threshold"`
	OnRecovered    bool `json:"on_recovered"`
}

// SystemAlertsInput is the editable alert configuration of one machine.
type SystemAlertsInput struct {
	Rules    []models.SystemAlertRule `json:"rules"`
	Channels []SystemChannelInput     `json:"channels"`
}

// SystemAlertsView is the alert configuration of one machine.
type SystemAlertsView struct {
	SystemID uint                        `json:"system_id"`
	Rules    []models.SystemAlertRule    `json:"rules"`
	Channels []models.SystemNotification `json:"channels"`
}

// Rules returns the alert rules of a machine, with their latch, in the display
// order. A machine that never opened the dialog still has the defaults: they are
// written on first read, because the evaluator must have a row to claim.
func (s *SystemAlertService) Rules(ctx context.Context, systemID uint) ([]models.SystemAlertRule, error) {
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return s.EnsureDefaults(ctx, tx, systemID)
	}); err != nil {
		return nil, ErrInternal(fmt.Errorf("preparing the alert rules of system %d: %w", systemID, err))
	}
	return s.storedRules(ctx, s.db, systemID)
}

// storedRules reads the rules of one machine without writing anything.
func (s *SystemAlertService) storedRules(ctx context.Context, db *gorm.DB, systemID uint) ([]models.SystemAlertRule, error) {
	var rows []models.SystemAlert
	if err := db.WithContext(ctx).Where("system_id = ?", systemID).Find(&rows).Error; err != nil {
		return nil, ErrInternal(fmt.Errorf("reading the alert rules of system %d: %w", systemID, err))
	}
	return orderSystemAlertRules(rows), nil
}

// orderSystemAlertRules sorts the stored rows into the display order and fills
// the rules a machine has no row for with their defaults, so a caller always
// receives the full set.
func orderSystemAlertRules(rows []models.SystemAlert) []models.SystemAlertRule {
	byMetric := make(map[models.SystemAlertMetric]models.SystemAlert, len(rows))
	for _, row := range rows {
		byMetric[row.Metric] = row
	}
	out := make([]models.SystemAlertRule, 0, len(models.AllSystemAlertMetrics))
	for _, metric := range models.AllSystemAlertMetrics {
		row, ok := byMetric[metric]
		if !ok {
			out = append(out, models.DefaultSystemAlert(metric))
			continue
		}
		out = append(out, models.SystemAlertRuleOf(row))
	}
	return out
}

// States returns the rules of every machine, keyed by system id (used by the
// listing decoration: one query for the whole page).
func (s *SystemAlertService) States(ctx context.Context, systemIDs []uint) (map[uint][]models.SystemAlertRule, error) {
	out := make(map[uint][]models.SystemAlertRule, len(systemIDs))
	if len(systemIDs) == 0 {
		return out, nil
	}
	var rows []models.SystemAlert
	if err := s.db.WithContext(ctx).Where("system_id IN ?", systemIDs).Find(&rows).Error; err != nil {
		return nil, ErrInternal(fmt.Errorf("reading the alert rules of %d systems: %w", len(systemIDs), err))
	}
	grouped := make(map[uint][]models.SystemAlert, len(systemIDs))
	for _, row := range rows {
		grouped[row.SystemID] = append(grouped[row.SystemID], row)
	}
	for _, id := range systemIDs {
		out[id] = orderSystemAlertRules(grouped[id])
	}
	return out, nil
}

// EnsureDefaults writes the rule a machine is missing, and nothing else: an
// existing rule (with its latch) is left untouched. It is idempotent and runs
// inside the caller's transaction.
func (s *SystemAlertService) EnsureDefaults(ctx context.Context, tx *gorm.DB, systemID uint) error {
	for _, rule := range models.DefaultSystemAlerts() {
		row := models.SystemAlert{
			SystemID:   systemID,
			Metric:     rule.Metric,
			Enabled:    rule.Enabled,
			Threshold:  rule.Threshold,
			MinMinutes: rule.MinMinutes,
			UpdatedAt:  time.Now().UTC(),
		}
		if err := tx.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error; err != nil {
			return fmt.Errorf("creating the %s rule of system %d: %w", rule.Metric, systemID, err)
		}
	}
	return nil
}

// Save stores the alert configuration of one machine: the rules it names and the
// channels that receive them.
//
// Everything happens in one transaction and in one revision of the system row,
// so a peer never sees the rules of a save with the channels of the previous one.
func (s *SystemAlertService) Save(ctx context.Context, systemID uint, input SystemAlertsInput) (*SystemAlertsView, error) {
	if s.systems == nil {
		return nil, ErrInternal(fmt.Errorf("the alert service is not wired to the system service"))
	}
	if _, err := s.systems.find(ctx, systemID); err != nil {
		return nil, err
	}
	before, err := s.links(ctx, systemID)
	if err != nil {
		return nil, err
	}
	channels, err := s.validateChannels(ctx, input.Channels)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := s.saveRules(ctx, tx, systemID, input.Rules, now); err != nil {
			return err
		}
		if err := s.saveChannels(ctx, tx, systemID, channels); err != nil {
			return err
		}
		// One revision for the whole save: the rules travel inside the system
		// payload, so bumping it is what tells a peer to read them again.
		if err := tx.WithContext(ctx).Model(&models.System{}).Where("id = ?", systemID).
			Updates(map[string]any{
				"revision":   gorm.Expr("revision + 1"),
				"updated_at": now,
			}).Error; err != nil {
			return fmt.Errorf("bumping the revision of system %d: %w", systemID, err)
		}
		if err := s.systems.emit(ctx, tx, systemID); err != nil {
			return err
		}
		return s.emitLinks(ctx, tx, systemID, before, channels)
	}); err != nil {
		return nil, AsAPIError(err)
	}
	s.publish("system.alerts", map[string]any{"id": systemID})
	return s.View(ctx, systemID)
}

// View returns the whole alert configuration of one machine.
func (s *SystemAlertService) View(ctx context.Context, systemID uint) (*SystemAlertsView, error) {
	// The rules of an unknown machine are not created: a GET must not leave four
	// orphan rows behind for a system that does not exist.
	if s.systems != nil {
		if _, err := s.systems.find(ctx, systemID); err != nil {
			return nil, err
		}
	}
	rules, err := s.Rules(ctx, systemID)
	if err != nil {
		return nil, err
	}
	links, err := s.links(ctx, systemID)
	if err != nil {
		return nil, err
	}
	return &SystemAlertsView{SystemID: systemID, Rules: rules, Channels: links}, nil
}

// saveRules upserts the incoming rules of the known metrics.
//
// The latch is never written here: the update lists the configuration columns
// explicitly, so a rule that is firing stays firing while its threshold is
// edited. An unknown metric is dropped (a newer peer may send one this build
// cannot evaluate), and a metric the caller omitted keeps its stored rule.
func (s *SystemAlertService) saveRules(ctx context.Context, tx *gorm.DB, systemID uint, rules []models.SystemAlertRule, now time.Time) error {
	incoming := make(map[models.SystemAlertMetric]models.SystemAlertRule, len(rules))
	for _, rule := range rules {
		normalized, ok := models.NormalizeSystemAlert(rule)
		if !ok {
			continue
		}
		incoming[normalized.Metric] = normalized
	}
	if len(incoming) == 0 {
		return nil
	}
	if err := s.EnsureDefaults(ctx, tx, systemID); err != nil {
		return err
	}
	for metric, rule := range incoming {
		row := models.SystemAlert{
			SystemID:   systemID,
			Metric:     metric,
			Enabled:    rule.Enabled,
			Threshold:  rule.Threshold,
			MinMinutes: rule.MinMinutes,
			UpdatedAt:  now,
		}
		updates := map[string]any{
			"enabled":     rule.Enabled,
			"threshold":   rule.Threshold,
			"min_minutes": rule.MinMinutes,
			"updated_at":  now,
		}
		if err := tx.WithContext(ctx).Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "system_id"}, {Name: "metric"}},
			DoUpdates: clause.Assignments(updates),
		}).Create(&row).Error; err != nil {
			return fmt.Errorf("saving the %s rule of system %d: %w", metric, systemID, err)
		}
	}
	return nil
}

// validateChannels checks the channel ids of a save and drops the duplicates.
func (s *SystemAlertService) validateChannels(ctx context.Context, channels []SystemChannelInput) ([]models.SystemNotification, error) {
	out := make([]models.SystemNotification, 0, len(channels))
	seen := map[uint]bool{}
	for _, channel := range channels {
		if channel.NotificationID == 0 || seen[channel.NotificationID] {
			continue
		}
		seen[channel.NotificationID] = true
		out = append(out, models.SystemNotification{
			SystemID:       0, // filled by saveChannels
			NotificationID: channel.NotificationID,
			OnDown:         channel.OnDown,
			OnUp:           channel.OnUp,
			OnThreshold:    channel.OnThreshold,
			OnRecovered:    channel.OnRecovered,
		})
	}
	if len(out) == 0 {
		return out, nil
	}
	ids := make([]uint, 0, len(out))
	for _, link := range out {
		ids = append(ids, link.NotificationID)
	}
	var found []uint
	if err := s.db.WithContext(ctx).Model(&models.Notification{}).
		Where("id IN ?", ids).Pluck("id", &found).Error; err != nil {
		return nil, ErrInternal(fmt.Errorf("reading the notification channels: %w", err))
	}
	known := make(map[uint]bool, len(found))
	for _, id := range found {
		known[id] = true
	}
	for _, id := range ids {
		if !known[id] {
			return nil, ErrBadRequest(i18n.CodeValidation,
				fmt.Sprintf("notification channel %d does not exist", id))
		}
	}
	return out, nil
}

// links loads the channel links of one machine.
func (s *SystemAlertService) links(ctx context.Context, systemID uint) ([]models.SystemNotification, error) {
	var rows []models.SystemNotification
	if err := s.db.WithContext(ctx).Where("system_id = ?", systemID).
		Order("notification_id ASC").Find(&rows).Error; err != nil {
		return nil, ErrInternal(fmt.Errorf("reading the alert channels of system %d: %w", systemID, err))
	}
	return rows, nil
}

// saveChannels rewrites the links of one machine.
//
// The rows are replaced rather than diffed in place, and the caller publishes
// the before/after diff so a peer sees exactly what moved. The four toggles are
// written explicitly: none of the columns carries a `default:` tag, so a
// checkbox turned off is stored as off.
func (s *SystemAlertService) saveChannels(ctx context.Context, tx *gorm.DB, systemID uint, channels []models.SystemNotification) error {
	if err := tx.WithContext(ctx).Where("system_id = ?", systemID).
		Delete(&models.SystemNotification{}).Error; err != nil {
		return fmt.Errorf("replacing the alert channels of system %d: %w", systemID, err)
	}
	for i := range channels {
		link := channels[i]
		link.SystemID = systemID
		if err := tx.WithContext(ctx).Create(&link).Error; err != nil {
			return fmt.Errorf("linking system %d to notification %d: %w", systemID, link.NotificationID, err)
		}
	}
	return nil
}

// emitLinks publishes the links that were added, changed or removed.
func (s *SystemAlertService) emitLinks(ctx context.Context, tx *gorm.DB, systemID uint,
	before, after []models.SystemNotification) error {

	if s.systems == nil || s.systems.sync == nil {
		return nil
	}
	emitter := s.systems.sync
	systemUUID, err := emitter.RowUUID(ctx, tx, models.EntitySystem, systemID)
	if err != nil || systemUUID == "" {
		return err
	}
	previous := make(map[uint]models.SystemNotification, len(before))
	for _, link := range before {
		previous[link.NotificationID] = link
	}
	current := make(map[uint]models.SystemNotification, len(after))
	for _, link := range after {
		current[link.NotificationID] = link
	}
	for id, link := range current {
		old, existed := previous[id]
		if existed && sameSystemLink(old, link) {
			continue
		}
		if err := emitter.EmitSystemNotification(ctx, tx, systemUUID, id, &link, true); err != nil {
			return err
		}
	}
	for id, link := range previous {
		if _, still := current[id]; still {
			continue
		}
		if err := emitter.EmitSystemNotification(ctx, tx, systemUUID, id, &link, false); err != nil {
			return err
		}
	}
	return nil
}

// sameSystemLink reports whether two links carry the same four toggles.
func sameSystemLink(a, b models.SystemNotification) bool {
	return a.OnDown == b.OnDown && a.OnUp == b.OnUp &&
		a.OnThreshold == b.OnThreshold && a.OnRecovered == b.OnRecovered
}

// publish broadcasts a real time event.
func (s *SystemAlertService) publish(event string, payload any) {
	if s.hub != nil {
		s.hub.Publish(event, payload)
	}
}

// Claim marks a rule as firing and reports whether this caller won the edge.
//
// The condition is the whole point: the UPDATE only matches while the latch is
// clear, so of every node and every tick evaluating the same rule exactly one
// dispatches. It is also what survives a failover, because the row is shared.
func (s *SystemAlertService) Claim(ctx context.Context, systemID uint, metric models.SystemAlertMetric) (bool, error) {
	now := time.Now().UTC()
	result := s.db.WithContext(ctx).Model(&models.SystemAlert{}).
		Where("system_id = ? AND metric = ? AND triggered = ?", systemID, metric, false).
		Updates(map[string]any{"triggered": true, "triggered_at": now, "updated_at": now})
	if result.Error != nil {
		return false, ErrInternal(fmt.Errorf("claiming the %s alert of system %d: %w", metric, systemID, result.Error))
	}
	return result.RowsAffected == 1, nil
}

// Release clears a firing rule and reports whether it was firing.
//
// The caller notifies only when this returns true: a recovery nobody was told
// about must not send a mail either.
func (s *SystemAlertService) Release(ctx context.Context, systemID uint, metric models.SystemAlertMetric) (bool, error) {
	now := time.Now().UTC()
	result := s.db.WithContext(ctx).Model(&models.SystemAlert{}).
		Where("system_id = ? AND metric = ? AND triggered = ?", systemID, metric, true).
		Updates(map[string]any{"triggered": false, "triggered_at": nil, "updated_at": now})
	if result.Error != nil {
		return false, ErrInternal(fmt.Errorf("releasing the %s alert of system %d: %w", metric, systemID, result.Error))
	}
	return result.RowsAffected == 1, nil
}

// Triggered reports whether the rule of a machine is firing right now.
func (s *SystemAlertService) Triggered(ctx context.Context, systemID uint, metric models.SystemAlertMetric) (bool, error) {
	var row models.SystemAlert
	switch err := s.db.WithContext(ctx).
		Where("system_id = ? AND metric = ?", systemID, metric).Take(&row).Error; {
	case err == nil:
		return row.Triggered, nil
	case errors.Is(err, gorm.ErrRecordNotFound):
		return false, nil
	default:
		return false, ErrInternal(fmt.Errorf("reading the %s alert of system %d: %w", metric, systemID, err))
	}
}
