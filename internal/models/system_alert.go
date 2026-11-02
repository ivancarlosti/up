package models

import "time"

// ---------------------------------------------------------------------------
// Alert rules of a hardware monitored system
// ---------------------------------------------------------------------------
//
// A system has one rule per metric (table system_alerts). The rule is
// CONFIGURATION and travels to the peers inside models.SystemPayload; the latch
// (Triggered/TriggeredAt) is LOCAL state and never leaves the node that
// evaluated it - a peer must not inherit an incident it did not see.
//
// The evaluation itself is stateless (see EvaluateSystemAlert and
// EvaluateDown): nothing but the latch is remembered between two ticks, so a
// restart, a failover or a clock jump can neither lose nor invent an edge.

// SystemAlertMetric is the metric a rule watches.
type SystemAlertMetric string

const (
	// SystemAlertDown watches the silence of the agent, not a number.
	SystemAlertDown   SystemAlertMetric = "down"
	SystemAlertCPU    SystemAlertMetric = "cpu"
	SystemAlertMemory SystemAlertMetric = "memory"
	SystemAlertDisk   SystemAlertMetric = "disk"
)

// AllSystemAlertMetrics lists the rules every system has, in the order the UI
// shows them.
var AllSystemAlertMetrics = []SystemAlertMetric{
	SystemAlertDown, SystemAlertCPU, SystemAlertMemory, SystemAlertDisk,
}

// Valid reports whether the metric is one this build knows.
func (m SystemAlertMetric) Valid() bool {
	for _, known := range AllSystemAlertMetrics {
		if m == known {
			return true
		}
	}
	return false
}

// The defaults of a new rule, in percent for the thresholds and in minutes for
// the window. They follow the Beszel defaults: 80% of CPU and memory, 85% of the
// root filesystem, sustained five minutes.
const (
	DefaultSystemAlertThreshold     = 80.0
	DefaultSystemDiskAlertThreshold = 85.0
	DefaultSystemAlertMinutes       = 5
	// The window of a rule is clamped to one month: a longer average would need
	// the whole history of the machine and would never recover.
	MaxSystemAlertMinutes = 1440
)

// SystemAlertRule is the rule of one metric as the API and the payloads carry
// it: the configuration plus the local latch.
type SystemAlertRule struct {
	Metric    SystemAlertMetric `json:"metric"`
	Enabled   bool              `json:"enabled"`
	Threshold float64           `json:"threshold"`
	// MinMinutes is how long the condition must hold before the alert fires.
	MinMinutes int `json:"min_minutes"`
	// Triggered and TriggeredAt are the local latch: whether the alert is
	// currently firing, and since when. They are never synchronised.
	Triggered   bool       `json:"triggered"`
	TriggeredAt *time.Time `json:"triggered_at,omitempty"`
}

// SystemAlert is the stored row of one rule (table system_alerts).
//
// Neither Enabled nor Triggered carries a `default:` tag on purpose: GORM
// replaces a zero value with the column default on insert, so a rule saved as
// "off" would be stored as "on" (the trap documented in
// plans/2026-11-02-status-page-clone.md §4).
type SystemAlert struct {
	SystemID    uint              `gorm:"primaryKey" json:"system_id"`
	Metric      SystemAlertMetric `gorm:"primaryKey;size:16" json:"metric"`
	Enabled     bool              `gorm:"not null" json:"enabled"`
	Threshold   float64           `gorm:"not null" json:"threshold"`
	MinMinutes  int               `gorm:"not null" json:"min_minutes"`
	Triggered   bool              `gorm:"not null" json:"triggered"`
	TriggeredAt *time.Time        `json:"triggered_at,omitempty"`
	UpdatedAt   time.Time         `json:"updated_at"`
}

// TableName keeps the table name stable.
func (SystemAlert) TableName() string { return "system_alerts" }

// DefaultSystemAlerts returns the rules a system starts with: every metric
// enabled at its Beszel default. No channel is linked: an alert nobody asked to
// receive is worse than no alert at all.
func DefaultSystemAlerts() []SystemAlertRule {
	rules := make([]SystemAlertRule, 0, len(AllSystemAlertMetrics))
	for _, metric := range AllSystemAlertMetrics {
		rules = append(rules, DefaultSystemAlert(metric))
	}
	return rules
}

// DefaultSystemAlert is the default rule of one metric.
func DefaultSystemAlert(metric SystemAlertMetric) SystemAlertRule {
	threshold := DefaultSystemAlertThreshold
	if metric == SystemAlertDisk {
		threshold = DefaultSystemDiskAlertThreshold
	}
	return SystemAlertRule{
		Metric:     metric,
		Enabled:    true,
		Threshold:  threshold,
		MinMinutes: DefaultSystemAlertMinutes,
	}
}

// NormalizeSystemAlert clamps one incoming rule to the stored domain.
//
// It reports false for a metric this build does not know, which is how an older
// node reacts to a rule a newer one invented: the rule is dropped instead of
// being stored under a name nothing evaluates.
func NormalizeSystemAlert(rule SystemAlertRule) (SystemAlertRule, bool) {
	if !rule.Metric.Valid() {
		return SystemAlertRule{}, false
	}
	out := rule
	if out.Threshold <= 0 || out.Threshold > 100 {
		out.Threshold = DefaultSystemAlert(out.Metric).Threshold
	}
	if out.MinMinutes <= 0 {
		out.MinMinutes = DefaultSystemAlertMinutes
	}
	if out.MinMinutes > MaxSystemAlertMinutes {
		out.MinMinutes = MaxSystemAlertMinutes
	}
	// The latch is server state: a client may send it back while saving, and it
	// must not be trusted.
	out.Triggered = false
	out.TriggeredAt = nil
	return out, true
}

// SystemAlertRuleOf converts a stored row into the API shape.
func SystemAlertRuleOf(row SystemAlert) SystemAlertRule {
	return SystemAlertRule{
		Metric:      row.Metric,
		Enabled:     row.Enabled,
		Threshold:   row.Threshold,
		MinMinutes:  row.MinMinutes,
		Triggered:   row.Triggered,
		TriggeredAt: row.TriggeredAt,
	}
}

// EvaluateSystemAlert reports whether a threshold rule is firing.
//
// average is the mean of the metric over [now-min, now] and oldest is the first
// sample of that window: a window the history does not cover is NOT an alert,
// otherwise the first sample after a restart would fire on a single point.
func EvaluateSystemAlert(rule SystemAlertRule, average float64, oldest *time.Time, now time.Time) bool {
	if !rule.Enabled || rule.Metric == SystemAlertDown {
		return false
	}
	if average < rule.Threshold {
		return false
	}
	if oldest == nil {
		return false
	}
	window := time.Duration(rule.MinMinutes) * time.Minute
	// 90% of the window: the poll cadence is one minute, so a five minute window
	// holds four or five samples depending on where the ticks fall.
	return now.Sub(*oldest) >= window*9/10
}

// EvaluateDown reports whether a system has been silent long enough to be
// called down.
//
// The listing already gives the agent SystemDownAfter (three missed polls)
// before it paints the badge, so min_minutes is the patience ON TOP of that
// grace: a rule of 5 fires after 8 minutes without a sample.
func EvaluateDown(rule SystemAlertRule, lastSampleAt, now time.Time) bool {
	if !rule.Enabled || rule.Metric != SystemAlertDown {
		return false
	}
	silence := now.Sub(lastSampleAt) - SystemDownAfter
	return silence >= time.Duration(rule.MinMinutes)*time.Minute
}

// SystemNotification links a machine to a notification channel.
//
// It is the system counterpart of models.MonitorNotification, with one toggle
// per event: a machine has four edges (down, up, threshold, recovered) and an
// operator who wants the disk warning but not the recovery mail must be able to
// say so.
type SystemNotification struct {
	SystemID       uint `gorm:"primaryKey" json:"system_id"`
	NotificationID uint `gorm:"primaryKey" json:"notification_id"`
	OnDown         bool `gorm:"not null" json:"on_down"`
	OnUp           bool `gorm:"not null" json:"on_up"`
	OnThreshold    bool `gorm:"not null" json:"on_threshold"`
	OnRecovered    bool `gorm:"not null" json:"on_recovered"`
}

// TableName keeps the table name stable.
func (SystemNotification) TableName() string { return "system_notifications" }
