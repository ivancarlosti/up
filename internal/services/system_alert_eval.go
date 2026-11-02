package services

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

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
// sweep fills the rules a machine is missing (a peer's, from before a rule
// existed), so the ingest path never pays for a no-op upsert every minute.
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
	if err := s.ensureAllRules(ctx); err != nil {
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
		if !row.RuleEnabled {
			// The down rule was switched off: whatever it latched is no longer
			// an incident, and nothing else would ever release it (the sweep is
			// the only judge of silence). The clear is silent on purpose - the
			// machine may well still be down, so a "back up" mail would be a lie.
			if err := s.clearLatch(ctx, row.ID, models.SystemAlertDown); err != nil {
				s.log.Warn("could not clear the down alert of a system", "system_id", row.ID, "error", err)
			}
			continue
		}
		if !row.Enabled || row.LastSampleAt == nil {
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

// ensureAllRules fills the rules a machine is missing.
//
// A machine created before a rule existed (the four original rules, or a machine
// a peer created with its own, older catalogue) has no row for it, and a rule
// nothing can claim never fires: the sweep is where that is repaired. It runs
// every 30 s and the insert is a single `ON CONFLICT DO NOTHING` batch, so the
// cost of the no-op case is one `SELECT system_id, metric` and nothing is ever
// overwritten: an edited threshold, a disabled rule and a latch all survive.
func (s *SystemAlertService) ensureAllRules(ctx context.Context) error {
	var systems []uint
	if err := s.db.WithContext(ctx).Model(&models.System{}).Pluck("id", &systems).Error; err != nil {
		return err
	}
	if len(systems) == 0 {
		return nil
	}
	var rows []struct {
		SystemID uint
		Metric   models.SystemAlertMetric
	}
	if err := s.db.WithContext(ctx).Model(&models.SystemAlert{}).
		Select("system_id, metric").Find(&rows).Error; err != nil {
		return err
	}
	known := make(map[uint]map[models.SystemAlertMetric]bool, len(systems))
	for _, row := range rows {
		if known[row.SystemID] == nil {
			known[row.SystemID] = make(map[models.SystemAlertMetric]bool, len(models.AllSystemAlertMetrics))
		}
		known[row.SystemID][row.Metric] = true
	}
	now := time.Now().UTC()
	defaults := s.defaults().Rules
	missing := make([]models.SystemAlert, 0, len(systems))
	for _, systemID := range systems {
		for _, rule := range defaults {
			if known[systemID][rule.Metric] {
				continue
			}
			missing = append(missing, models.SystemAlert{
				SystemID:   systemID,
				Metric:     rule.Metric,
				Enabled:    rule.Enabled,
				Threshold:  rule.Threshold,
				MinMinutes: rule.MinMinutes,
				UpdatedAt:  now,
			})
		}
	}
	if len(missing) == 0 {
		return nil
	}
	return s.db.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).CreateInBatches(&missing, 100).Error
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
		s.evaluateRule(ctx, systemID, rule, now)
	}
}

// evaluateRule dispatches one rule to the evaluator its kind needs: the state
// rules (unhealthy containers, failing disks) count rows, everything else
// averages a window.
func (s *SystemAlertService) evaluateRule(ctx context.Context, systemID uint, rule models.SystemAlertRule, now time.Time) {
	spec, ok := models.SystemAlertSpecOf(rule.Metric)
	if !ok {
		return
	}
	if !spec.HasWindow {
		s.evaluateCount(ctx, systemID, rule)
		return
	}
	s.evaluateThreshold(ctx, systemID, rule, now)
}

// evaluateThresholds re-reads the rules of a machine and evaluates them (the
// sweep path, which does not carry them).
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
		s.evaluateRule(ctx, systemID, rule, now)
	}
}

// EvaluateMetric evaluates one rule of a machine right away.
//
// The SMART report has its own slow cadence (ten minutes), so a failing disk
// would wait for the next sample before it was noticed: the refresh calls this
// with its own metric and the alert leaves in the same breath.
func (s *SystemAlertService) EvaluateMetric(ctx context.Context, systemID uint, metric models.SystemAlertMetric) {
	rule, err := s.rule(ctx, systemID, metric)
	if err != nil {
		s.log.Warn("could not read an alert rule", "system_id", systemID, "metric", metric, "error", err)
		return
	}
	spec, ok := models.SystemAlertSpecOf(metric)
	if !ok || spec.HasWindow {
		return
	}
	s.evaluateCount(ctx, systemID, rule)
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
	detail := fmt.Sprintf("average %s over the last %d minutes (threshold %s)",
		models.FormatSystemAlertValue(rule.Metric, average), rule.MinMinutes,
		models.FormatSystemAlertThreshold(rule.Metric, rule.Threshold))
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

// evaluateCount fires or releases one state rule (unhealthy containers, failing
// disks).
//
// There is no window: the count is the state of the machine right now, and a
// machine that stopped reporting keeps its last snapshot, so the alert does not
// flicker while it is down (its down rule is the one that speaks for it).
func (s *SystemAlertService) evaluateCount(ctx context.Context, systemID uint, rule models.SystemAlertRule) {
	count, err := s.count(ctx, systemID, rule.Metric)
	if err != nil {
		s.log.Warn("could not read the alert count of a system",
			"system_id", systemID, "metric", rule.Metric, "error", err)
		return
	}
	detail := models.SystemAlertCountDetail(rule.Metric, count, rule.Threshold)
	value := float64(count)
	if models.EvaluateSystemCountAlert(rule, count) {
		claimed, err := s.Claim(ctx, systemID, rule.Metric)
		if err != nil {
			s.log.Warn("could not claim an alert", "system_id", systemID, "metric", rule.Metric, "error", err)
			return
		}
		if claimed {
			s.dispatch(ctx, systemID, models.EventSystemThreshold, rule, value, detail)
		}
		return
	}
	s.releaseAndNotify(ctx, systemID, rule, value, detail)
}

// count returns the number a state rule watches.
//
// The container count reads the snapshot table (a container that disappeared is
// deleted, so a stopped one never counts) and the SMART count reads the column
// the refresh denormalised: neither parses a JSON blob per tick.
func (s *SystemAlertService) count(ctx context.Context, systemID uint, metric models.SystemAlertMetric) (int64, error) {
	switch metric {
	case models.SystemAlertContainer:
		var count int64
		if err := s.db.WithContext(ctx).Model(&models.SystemContainer{}).
			Where("system_id = ? AND health = ?", systemID, "unhealthy").
			Count(&count).Error; err != nil {
			return 0, fmt.Errorf("counting the unhealthy containers of system %d: %w", systemID, err)
		}
		return count, nil
	case models.SystemAlertSmart:
		// A machine whose SMART report was never read has no row at all, which
		// is the healthy case: there is no disk to report.
		var row struct {
			Failed int64 `gorm:"column:failed"`
		}
		err := s.db.WithContext(ctx).Model(&models.SystemSmart{}).
			Select("COALESCE(failed, 0) AS failed").
			Where("system_id = ?", systemID).Take(&row).Error
		switch {
		case err == nil:
			return row.Failed, nil
		case errors.Is(err, gorm.ErrRecordNotFound):
			return 0, nil
		default:
			return 0, fmt.Errorf("reading the failed disks of system %d: %w", systemID, err)
		}
	}
	return 0, nil
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
	case models.SystemAlertSwap:
		return "swap_pct", true
	case models.SystemAlertDisk:
		return "disk_pct", true
	case models.SystemAlertLoad:
		return "load_avg", true
	case models.SystemAlertLoad5:
		return "load_avg5", true
	case models.SystemAlertLoad15:
		return "load_avg15", true
	case models.SystemAlertTemperature:
		return "temperature", true
	case models.SystemAlertGPU:
		return "gpu_pct", true
	case models.SystemAlertBandwidth:
		// The two counters are bytes per second (the agent already divided them
		// by the poll interval) and the rule is in MB/s, so the window averages
		// the sum of both directions.
		return "(net_sent + net_recv) / 1048576.0", true
	}
	return "", false
}

// clearLatch drops a firing rule without dispatching anything.
//
// It is the "this rule is no longer watched" path (a down rule that was switched
// off): the incident ends from the operator's point of view, but it did not
// recover, so a recovery notification would be wrong.
func (s *SystemAlertService) clearLatch(ctx context.Context, systemID uint, metric models.SystemAlertMetric) error {
	now := time.Now().UTC()
	return s.db.WithContext(ctx).Model(&models.SystemAlert{}).
		Where("system_id = ? AND metric = ? AND triggered = ?", systemID, metric, true).
		Updates(map[string]any{"triggered": false, "triggered_at": nil, "updated_at": now}).Error
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
