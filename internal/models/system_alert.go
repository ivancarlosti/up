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
	SystemAlertSwap   SystemAlertMetric = "swap"
	SystemAlertDisk   SystemAlertMetric = "disk"
	// The three load averages: one rule each, because a machine that is fine
	// right now but has been saturated for a quarter of an hour is a different
	// incident from a one minute spike.
	SystemAlertLoad   SystemAlertMetric = "load"
	SystemAlertLoad5  SystemAlertMetric = "load5"
	SystemAlertLoad15 SystemAlertMetric = "load15"
	// SystemAlertTemperature watches the hottest sensor of the sample.
	SystemAlertTemperature SystemAlertMetric = "temperature"
	// SystemAlertBandwidth watches the traffic (sent plus received) averaged
	// over the window, in MB/s.
	SystemAlertBandwidth SystemAlertMetric = "bandwidth"
	SystemAlertGPU       SystemAlertMetric = "gpu"
	// SystemAlertContainer and SystemAlertSmart are state rules: they count
	// unhealthy containers (a snapshot) and failing disks (the last SMART
	// report) and fire as soon as the count reaches the threshold.
	SystemAlertContainer SystemAlertMetric = "container"
	SystemAlertSmart     SystemAlertMetric = "smart"
)

// AllSystemAlertMetrics lists the rules every system has, in the order the UI
// shows them: the order of the catalogue in system_alert_spec.go, which is the
// single place a rule is described.
var AllSystemAlertMetrics = allSystemAlertMetrics()

// allSystemAlertMetrics projects the catalogue onto its metric names.
func allSystemAlertMetrics() []SystemAlertMetric {
	out := make([]SystemAlertMetric, 0, len(systemAlertSpecs))
	for _, spec := range systemAlertSpecs {
		out = append(out, spec.Metric)
	}
	return out
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

// The well known default values. The catalogue in system_alert_spec.go is the
// source of truth (each rule carries its own); these constants name the two
// thresholds and the window the documentation and the tests talk about.
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
// enabled at its built-in default. No channel is linked: an alert nobody asked to
// receive is worse than no alert at all.
//
// These are the BUILT-IN values. What a machine actually starts with is the
// global setting (models.SystemAlertDefaults), which may have been edited in
// Admin > Settings; SystemAlertDefaults() reads it and falls back to these.
func DefaultSystemAlerts() []SystemAlertRule {
	rules := make([]SystemAlertRule, 0, len(systemAlertSpecs))
	for _, spec := range systemAlertSpecs {
		rules = append(rules, DefaultSystemAlert(spec.Metric))
	}
	return rules
}

// DefaultSystemAlert is the built-in rule of one metric.
func DefaultSystemAlert(metric SystemAlertMetric) SystemAlertRule {
	spec, ok := SystemAlertSpecOf(metric)
	if !ok {
		return SystemAlertRule{Metric: metric, MinMinutes: DefaultSystemAlertMinutes}
	}
	return SystemAlertRule{
		Metric:     spec.Metric,
		Enabled:    spec.DefaultEnabled,
		Threshold:  spec.DefaultThreshold,
		MinMinutes: spec.DefaultMinutes,
	}
}

// SystemAlertDefaults is the global rule set of the installation: what a machine
// starts with and what "reset to defaults" restores (Admin > Settings).
//
// It is one JSON blob in the settings table, so it travels with the other
// synchronised settings and every node applies the same reset.
type SystemAlertDefaults struct {
	Rules []SystemAlertRule `json:"rules"`
}

// DefaultSystemAlertDefaults is the built-in rule set.
func DefaultSystemAlertDefaults() SystemAlertDefaults {
	return SystemAlertDefaults{Rules: DefaultSystemAlerts()}
}

// NormalizeSystemAlertDefaults turns whatever was stored (or posted) into a
// complete, canonical rule set: one rule per catalogue metric, in catalogue
// order, clamped, with the latch always clear.
//
// A rule missing from the input keeps its built-in value and an unknown metric
// is dropped, so an operator upgrading the binary never has to rewrite the
// setting by hand, and an older node reading a newer setting can still make
// sense of it.
func NormalizeSystemAlertDefaults(in SystemAlertDefaults) SystemAlertDefaults {
	incoming := make(map[SystemAlertMetric]SystemAlertRule, len(in.Rules))
	for _, rule := range in.Rules {
		normalized, ok := NormalizeSystemAlert(rule)
		if !ok {
			continue
		}
		incoming[normalized.Metric] = normalized
	}
	out := DefaultSystemAlertDefaults()
	for i, rule := range out.Rules {
		if normalized, ok := incoming[rule.Metric]; ok {
			out.Rules[i] = normalized
		}
	}
	return out
}

// NormalizeSystemAlert clamps one incoming rule to the stored domain.
//
// It reports false for a metric this build does not know, which is how an older
// node reacts to a rule a newer one invented: the rule is dropped instead of
// being stored under a name nothing evaluates.
func NormalizeSystemAlert(rule SystemAlertRule) (SystemAlertRule, bool) {
	spec, ok := SystemAlertSpecOf(rule.Metric)
	if !ok {
		return SystemAlertRule{}, false
	}
	out := rule
	// The domain comes from the catalogue, not from a hardcoded 1..100: a load
	// rule accepts 0, a temperature rule starts at 40, a bandwidth one goes to
	// 250. Anything outside it is the default.
	if !spec.HasThreshold {
		out.Threshold = spec.DefaultThreshold
	} else if out.Threshold < spec.Min || out.Threshold > spec.Max {
		out.Threshold = spec.DefaultThreshold
	}
	// A state rule has no window: its minutes are always zero, never "the
	// default minutes", so a save cannot invent a warm-up it does not have.
	switch {
	case !spec.HasWindow:
		out.MinMinutes = 0
	case out.MinMinutes <= 0:
		out.MinMinutes = spec.DefaultMinutes
	case out.MinMinutes > MaxSystemAlertMinutes:
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
	spec, ok := SystemAlertSpecOf(rule.Metric)
	if !ok || !rule.Enabled || !spec.HasThreshold || !spec.HasWindow {
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

// EvaluateSystemCountAlert reports whether a state rule (unhealthy containers,
// failing disks) is firing.
//
// There is no window and no warm-up: the count IS the state of the machine right
// now, so the alert fires the moment it reaches the threshold and recovers the
// moment it drops below. A count of zero is the healthy case, which is also what
// a machine that reported nothing looks like (no rows to count).
func EvaluateSystemCountAlert(rule SystemAlertRule, count int64) bool {
	if !rule.Enabled {
		return false
	}
	if spec, ok := SystemAlertSpecOf(rule.Metric); !ok || spec.HasWindow {
		return false
	}
	return float64(count) >= rule.Threshold
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
