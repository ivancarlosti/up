package models

import (
	"testing"
	"time"
)

// TestDefaultSystemAlerts pins the rules a new machine starts with: one per
// metric, enabled, at the Beszel defaults and with no latch.
func TestDefaultSystemAlerts(t *testing.T) {
	rules := DefaultSystemAlerts()
	if len(rules) != len(AllSystemAlertMetrics) {
		t.Fatalf("got %d default rules, want one per metric (%d)", len(rules), len(AllSystemAlertMetrics))
	}
	byMetric := map[SystemAlertMetric]SystemAlertRule{}
	for _, rule := range rules {
		byMetric[rule.Metric] = rule
	}
	for _, metric := range AllSystemAlertMetrics {
		rule, ok := byMetric[metric]
		if !ok {
			t.Fatalf("%s has no default rule", metric)
		}
		if !rule.Enabled {
			t.Errorf("%s is disabled by default", metric)
		}
		if rule.MinMinutes != DefaultSystemAlertMinutes {
			t.Errorf("%s window = %d, want %d", metric, rule.MinMinutes, DefaultSystemAlertMinutes)
		}
		if rule.Triggered || rule.TriggeredAt != nil {
			t.Errorf("%s starts latched: %+v", metric, rule)
		}
	}
	if got := byMetric[SystemAlertDisk].Threshold; got != DefaultSystemDiskAlertThreshold {
		t.Errorf("disk threshold = %v, want %v", got, DefaultSystemDiskAlertThreshold)
	}
	if got := byMetric[SystemAlertCPU].Threshold; got != DefaultSystemAlertThreshold {
		t.Errorf("cpu threshold = %v, want %v", got, DefaultSystemAlertThreshold)
	}
}

// TestNormalizeSystemAlert pins the clamp an incoming (or peer) rule goes
// through, including the rule that the latch is never trusted from outside.
func TestNormalizeSystemAlert(t *testing.T) {
	latched := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)
	rule, ok := NormalizeSystemAlert(SystemAlertRule{
		Metric:      SystemAlertCPU,
		Enabled:     false,
		Threshold:   250,
		MinMinutes:  MaxSystemAlertMinutes + 100,
		Triggered:   true,
		TriggeredAt: &latched,
	})
	if !ok {
		t.Fatal("a known metric was refused")
	}
	if rule.Enabled {
		t.Error("the enabled flag was overwritten")
	}
	if rule.Threshold != DefaultSystemAlertThreshold {
		t.Errorf("threshold = %v, want the default %v", rule.Threshold, DefaultSystemAlertThreshold)
	}
	if rule.MinMinutes != MaxSystemAlertMinutes {
		t.Errorf("min_minutes = %d, want the clamp %d", rule.MinMinutes, MaxSystemAlertMinutes)
	}
	if rule.Triggered || rule.TriggeredAt != nil {
		t.Errorf("the latch of the caller was kept: %+v", rule)
	}

	if _, ok := NormalizeSystemAlert(SystemAlertRule{Metric: SystemAlertMetric("load")}); ok {
		t.Error("an unknown metric was accepted")
	}

	// A zero window is not "fire immediately": it falls back to the default.
	if rule, _ := NormalizeSystemAlert(SystemAlertRule{Metric: SystemAlertMemory, Threshold: 70}); rule.MinMinutes != DefaultSystemAlertMinutes {
		t.Errorf("min_minutes = %d, want %d", rule.MinMinutes, DefaultSystemAlertMinutes)
	}
}

// TestEvaluateSystemAlert pins the sustained rule: the average must be over the
// threshold AND the window must hold the history it claims to average.
func TestEvaluateSystemAlert(t *testing.T) {
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	rule := SystemAlertRule{Metric: SystemAlertCPU, Enabled: true, Threshold: 80, MinMinutes: 5}
	covered := now.Add(-5 * time.Minute)
	tooYoung := now.Add(-90 * time.Second)

	cases := []struct {
		name    string
		rule    SystemAlertRule
		average float64
		oldest  *time.Time
		want    bool
	}{
		{"over and covered", rule, 85, &covered, true},
		{"exactly at the threshold", rule, 80, &covered, true},
		{"under the threshold", rule, 79.9, &covered, false},
		{"over but the window is too young", rule, 85, &tooYoung, false},
		{"over but no sample at all", rule, 85, nil, false},
		{"disabled", func() SystemAlertRule { r := rule; r.Enabled = false; return r }(), 99, &covered, false},
		{"the down rule has no number", SystemAlertRule{Metric: SystemAlertDown, Enabled: true, MinMinutes: 5}, 99, &covered, false},
		{"disk threshold", SystemAlertRule{Metric: SystemAlertDisk, Enabled: true, Threshold: 85, MinMinutes: 5}, 86, &covered, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := EvaluateSystemAlert(tc.rule, tc.average, tc.oldest, now); got != tc.want {
				t.Fatalf("EvaluateSystemAlert = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestEvaluateDown pins the down rule: the listing already waits SystemDownAfter,
// so the rule adds its minutes on top of that grace.
func TestEvaluateDown(t *testing.T) {
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	rule := SystemAlertRule{Metric: SystemAlertDown, Enabled: true, MinMinutes: 5}

	at := func(silence time.Duration) time.Time { return now.Add(-silence) }
	cases := []struct {
		name string
		rule SystemAlertRule
		last time.Time
		want bool
	}{
		{"fresh sample", rule, at(time.Minute), false},
		{"inside the grace window", rule, at(SystemDownAfter), false},
		{"grace plus one minute", rule, at(SystemDownAfter + time.Minute), false},
		{"grace plus the rule", rule, at(SystemDownAfter + 5*time.Minute), true},
		{"long silent", rule, at(3 * time.Hour), true},
		{"disabled", func() SystemAlertRule { r := rule; r.Enabled = false; return r }(), at(time.Hour), false},
		{"a threshold rule never means down", SystemAlertRule{Metric: SystemAlertCPU, Enabled: true, MinMinutes: 5}, at(time.Hour), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := EvaluateDown(tc.rule, tc.last, now); got != tc.want {
				t.Fatalf("EvaluateDown = %v, want %v", got, tc.want)
			}
		})
	}
}
