package services

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"

	"github.com/ivancarlosti/up/internal/models"
)

// ---------------------------------------------------------------------------
// Evaluation and dispatch of the hardware alerts
// ---------------------------------------------------------------------------
//
// Two entry points, one rule set:
//
//   - EvaluateSample runs after a sample was stored, on the node that owns the
//     agent connection. It is the live path: the counters are already there, so
//     a threshold crossing is noticed the second it happens.
//   - Sweep runs every 30 s on every node. It is what notices a SILENT machine
//     (no sample means no ingest, so nothing else would ever run), and what
//     releases a latch whose samples came back on another node after a failover.
//
// Both are idempotent: an edge is dispatched only by the caller that wins the
// latch (Claim/Release), so running them twice changes nothing.

// EvaluateSample evaluates every rule of a machine after a sample was stored.
//
// It reads the stored rules without writing: `Create` writes the defaults and the
// sweep repairs a machine that has none (a peer's, from before the alerts), so the
// ingest path never pays for a no-op upsert every minute.
func (s *SystemAlertService) EvaluateSample(ctx context.Context, systemID uint, now time.Time) {
	rules, err := s.storedRules(ctx, s.db, systemID)
	if err != nil {
		s.log.Warn("could not evaluate the alerts of a system", "system_id", systemID, "error", err)
		return
	}
	s.evaluate(ctx, systemID, rules, now)
}

// Sweep evaluates every machine: the silent ones (the down rule) and the ones
// that are reporting (a threshold that came back under while another node held
// the connection).
func (s *SystemAlertService) Sweep(ctx context.Context) {
	if err := s.ensureLegacyRules(ctx); err != nil {
		s.log.Warn("could not prepare the alert rules of the existing systems", "error", err)
	}
	var rows []struct {
		models.System
		RuleEnabled bool `gorm:"column:rule_enabled"`
	}
	err := s.db.WithContext(ctx).Model(&models.System{}).
		Select("systems.*, system_alerts.enabled AS rule_enabled").
		Joins("JOIN system_alerts ON system_alerts.system_id = systems.id AND system_alerts.metric = ?", models.SystemAlertDown).
		Find(&rows).Error
	if err != nil {
		s.log.Warn("could not load the systems to evaluate", "error", err)
		return
	}
	now := time.Now().UTC()
	for _, row := range rows {
		if !row.Enabled || !row.RuleEnabled || row.LastSampleAt == nil {
			// A disabled machine is not polled, and a machine that never
			// reported is "pending", not down: neither is an incident.
			continue
		}
		rule, err := s.rule(ctx, row.ID, models.SystemAlertDown)
		if err != nil {
			s.log.Warn("could not read the down rule of a system", "system_id", row.ID, "error", err)
			continue
		}
		s.evaluateDown(ctx, row.ID, rule, *row.LastSampleAt, now)
		s.evaluateThresholds(ctx, row.ID, now)
	}
}

// ensureLegacyRules writes the defaults of a machine that has no rule row at all.
//
// A machine created before the alerts existed (or created by a peer that predates
// them) has no `system_alerts` row, and a rule nothing can claim is a rule that
// never fires: the sweep is where that is repaired, once per installation.
func (s *SystemAlertService) ensureLegacyRules(ctx context.Context) error {
	var missing []uint
	err := s.db.WithContext(ctx).Model(&models.System{}).
		Where("NOT EXISTS (SELECT 1 FROM system_alerts WHERE system_alerts.system_id = systems.id)").
		Pluck("systems.id", &missing).Error
	if err != nil || len(missing) == 0 {
		return err
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, id := range missing {
			if err := s.EnsureDefaults(ctx, tx, id); err != nil {
				return err
			}
		}
		return nil
	})
}

// evaluate applies the rules of one machine.
func (s *SystemAlertService) evaluate(ctx context.Context, systemID uint, rules []models.SystemAlertRule, now time.Time) {
	for _, rule := range rules {
		if rule.Metric == models.SystemAlertDown {
			// A stored sample means the machine is talking again: the down
			// latch is over. The silence itself is judged by the sweep.
			s.releaseAndNotify(ctx, systemID, rule, 0, "the machine is reporting again")
			continue
		}
		s.evaluateThreshold(ctx, systemID, rule, now)
	}
}

// evaluateThresholds re-reads the rules of a machine and evaluates the numeric
// ones (the sweep path, which does not carry them).
func (s *SystemAlertService) evaluateThresholds(ctx context.Context, systemID uint, now time.Time) {
	rules, err := s.storedRules(ctx, s.db, systemID)
	if err != nil {
		s.log.Warn("could not read the alert rules of a system", "system_id", systemID, "error", err)
		return
	}
	for _, rule := range rules {
		if rule.Metric == models.SystemAlertDown {
			continue
		}
		s.evaluateThreshold(ctx, systemID, rule, now)
	}
}

// evaluateThreshold fires or releases one numeric rule.
//
// A window with no sample is skipped entirely: a machine that stopped reporting
// must not "recover" from its CPU alert just because the average of nothing is
// zero. Its down rule is the one that speaks for it.
func (s *SystemAlertService) evaluateThreshold(ctx context.Context, systemID uint, rule models.SystemAlertRule, now time.Time) {
	average, oldest, samples, err := s.window(ctx, systemID, rule.Metric, rule.MinMinutes, now)
	if err != nil {
		s.log.Warn("could not read the alert window of a system",
			"system_id", systemID, "metric", rule.Metric, "error", err)
		return
	}
	if samples == 0 {
		return
	}
	detail := fmt.Sprintf("average %.1f%% over the last %d minutes (threshold %.0f%%)",
		average, rule.MinMinutes, rule.Threshold)
	if models.EvaluateSystemAlert(rule, average, oldest, now) {
		claimed, err := s.Claim(ctx, systemID, rule.Metric)
		if err != nil {
			s.log.Warn("could not claim an alert", "system_id", systemID, "metric", rule.Metric, "error", err)
			return
		}
		if claimed {
			s.dispatch(ctx, systemID, models.EventSystemThreshold, rule, average, detail)
		}
		return
	}
	s.releaseAndNotify(ctx, systemID, rule, average, detail)
}

// evaluateDown fires the down rule of a silent machine.
func (s *SystemAlertService) evaluateDown(ctx context.Context, systemID uint, rule models.SystemAlertRule, lastSample, now time.Time) {
	if !models.EvaluateDown(rule, lastSample, now) {
		s.releaseAndNotify(ctx, systemID, rule, 0, "the machine is reporting again")
		return
	}
	claimed, err := s.Claim(ctx, systemID, models.SystemAlertDown)
	if err != nil {
		s.log.Warn("could not claim the down alert", "system_id", systemID, "error", err)
		return
	}
	if !claimed {
		return
	}
	silence := now.Sub(lastSample).Round(time.Minute)
	s.dispatch(ctx, systemID, models.EventSystemDown, rule, 0,
		fmt.Sprintf("no sample for %s", silence))
}

// releaseAndNotify clears a latch and sends the matching recovery event.
//
// The event only leaves when the latch was actually set: a recovery nobody was
// told about must not send a mail either.
func (s *SystemAlertService) releaseAndNotify(ctx context.Context, systemID uint, rule models.SystemAlertRule, value float64, detail string) {
	released, err := s.Release(ctx, systemID, rule.Metric)
	if err != nil {
		s.log.Warn("could not release an alert", "system_id", systemID, "metric", rule.Metric, "error", err)
		return
	}
	if !released {
		return
	}
	event := models.EventSystemRecovered
	if rule.Metric == models.SystemAlertDown {
		event = models.EventSystemUp
	}
	s.dispatch(ctx, systemID, event, rule, value, detail)
}

// rule returns one rule of a machine (its default when it has no row yet).
func (s *SystemAlertService) rule(ctx context.Context, systemID uint, metric models.SystemAlertMetric) (models.SystemAlertRule, error) {
	var row models.SystemAlert
	switch err := s.db.WithContext(ctx).
		Where("system_id = ? AND metric = ?", systemID, metric).Take(&row).Error; {
	case err == nil:
		return models.SystemAlertRuleOf(row), nil
	case errors.Is(err, gorm.ErrRecordNotFound):
		return models.DefaultSystemAlert(metric), nil
	default:
		return models.SystemAlertRule{}, err
	}
}

// window returns the average of a metric over [now-minutes, now], the oldest
// sample of that window and how many samples it holds.
func (s *SystemAlertService) window(ctx context.Context, systemID uint, metric models.SystemAlertMetric, minutes int, now time.Time) (float64, *time.Time, int64, error) {
	column, ok := metricColumn(metric)
	if !ok {
		return 0, nil, 0, nil
	}
	var row struct {
		Average float64    `gorm:"column:average"`
		Oldest  *time.Time `gorm:"column:oldest"`
		Samples int64      `gorm:"column:samples"`
	}
	since := now.Add(-time.Duration(minutes) * time.Minute)
	err := s.db.WithContext(ctx).Model(&models.SystemMetric{}).
		Select(fmt.Sprintf("COALESCE(AVG(%s), 0) AS average, MIN(created_at) AS oldest, COUNT(*) AS samples", column)).
		Where("system_id = ? AND created_at >= ?", systemID, since).
		Scan(&row).Error
	if err != nil {
		return 0, nil, 0, fmt.Errorf("reading the %s window of system %d: %w", metric, systemID, err)
	}
	return row.Average, row.Oldest, row.Samples, nil
}

// metricColumn maps a rule to the column it averages.
func metricColumn(metric models.SystemAlertMetric) (string, bool) {
	switch metric {
	case models.SystemAlertCPU:
		return "cpu", true
	case models.SystemAlertMemory:
		return "mem_pct", true
	case models.SystemAlertDisk:
		return "disk_pct", true
	}
	return "", false
}

// dispatch delivers an event to the channels of the machine and tells the UI.
func (s *SystemAlertService) dispatch(ctx context.Context, systemID uint, event models.NotificationEvent,
	rule models.SystemAlertRule, value float64, detail string) {

	s.log.Info("hardware alert",
		"system_id", systemID, "metric", rule.Metric, "event", event, "value", value)
	s.publish("system.alert", map[string]any{
		"id": systemID, "metric": rule.Metric, "event": event, "value": value,
	})
	if s.notifier == nil || s.systems == nil {
		return
	}
	system, err := s.systems.find(ctx, systemID)
	if err != nil {
		s.log.Warn("could not load the system of an alert", "system_id", systemID, "error", err)
		return
	}
	s.notifier.DispatchSystem(ctx, system, event, rule, value, detail, s.nodeID())
}

// nodeID is this node identity (stamped on every delivery log row).
func (s *SystemAlertService) nodeID() string {
	if s.cfg == nil {
		return ""
	}
	return s.cfg.NodeID
}
