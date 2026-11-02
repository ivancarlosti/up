package models

import (
	"testing"
	"time"
)

// TestDefaultSystemAlerts pins the rules a new machine starts with: one per
// metric, at the built-in value of the catalogue and with no latch.
func TestDefaultSystemAlerts(t *testing.T) {
	rules := DefaultSystemAlerts()
	if len(rules) != len(systemAlertSpecs) {
		t.Fatalf("got %d default rules, want one per metric (%d)", len(rules), len(systemAlertSpecs))
	}
	byMetric := map[SystemAlertMetric]SystemAlertRule{}
	for _, rule := range rules {
		byMetric[rule.Metric] = rule
	}
	for _, spec := range systemAlertSpecs {
		rule, ok := byMetric[spec.Metric]
		if !ok {
			t.Fatalf("%s has no default rule", spec.Metric)
		}
		if rule.Enabled != spec.DefaultEnabled {
			t.Errorf("%s enabled = %v, want %v", spec.Metric, rule.Enabled, spec.DefaultEnabled)
		}
		if rule.Threshold != spec.DefaultThreshold {
			t.Errorf("%s threshold = %v, want %v", spec.Metric, rule.Threshold, spec.DefaultThreshold)
		}
		if rule.MinMinutes != spec.DefaultMinutes {
			t.Errorf("%s window = %d, want %d", spec.Metric, rule.MinMinutes, spec.DefaultMinutes)
		}
		if rule.Triggered || rule.TriggeredAt != nil {
			t.Errorf("%s starts latched: %+v", spec.Metric, rule)
		}
	}
	if got := byMetric[SystemAlertDisk].Threshold; got != DefaultSystemDiskAlertThreshold {
		t.Errorf("disk threshold = %v, want %v", got, DefaultSystemDiskAlertThreshold)
	}
	if got := byMetric[SystemAlertCPU].Threshold; got != DefaultSystemAlertThreshold {
		t.Errorf("cpu threshold = %v, want %v", got, DefaultSystemAlertThreshold)
	}
}

// TestSystemAlertSpecs pins the catalogue invariants the form and the evaluator
// rely on: unique metrics in the same order as AllSystemAlertMetrics, a default
// inside the domain, and a window exactly for the sustained rules.
func TestSystemAlertSpecs(t *testing.T) {
	specs := AllSystemAlertSpecs()
	if len(specs) != len(AllSystemAlertMetrics) {
		t.Fatalf("catalogue has %d specs but %d metrics", len(specs), len(AllSystemAlertMetrics))
	}
	seen := map[SystemAlertMetric]bool{}
	for i, spec := range specs {
		if seen[spec.Metric] {
			t.Fatalf("%s appears twice in the catalogue", spec.Metric)
		}
		seen[spec.Metric] = true
		if AllSystemAlertMetrics[i] != spec.Metric {
			t.Errorf("position %d: catalogue says %s, metrics say %s", i, spec.Metric, AllSystemAlertMetrics[i])
		}
		if !spec.Metric.Valid() {
			t.Errorf("%s is in the catalogue but not valid", spec.Metric)
		}
		if spec.HasThreshold && (spec.DefaultThreshold < spec.Min || spec.DefaultThreshold > spec.Max) {
			t.Errorf("%s default %v is outside %v..%v", spec.Metric, spec.DefaultThreshold, spec.Min, spec.Max)
		}
		if spec.HasWindow && spec.DefaultMinutes <= 0 {
			t.Errorf("%s has a window but no default minutes", spec.Metric)
		}
		if !spec.HasWindow && spec.DefaultMinutes != 0 {
			t.Errorf("%s has no window but %d default minutes", spec.Metric, spec.DefaultMinutes)
		}
	}
	for _, metric := range []SystemAlertMetric{SystemAlertLoad, SystemAlertTemperature, SystemAlertBandwidth, SystemAlertGPU, SystemAlertContainer, SystemAlertSmart} {
		if !seen[metric] {
			t.Errorf("%s is missing from the catalogue", metric)
		}
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

	if _, ok := NormalizeSystemAlert(SystemAlertRule{Metric: SystemAlertMetric("battery")}); ok {
		t.Error("an unknown metric was accepted")
	}

	// A zero window is not "fire immediately": it falls back to the default.
	if rule, _ := NormalizeSystemAlert(SystemAlertRule{Metric: SystemAlertMemory, Threshold: 70}); rule.MinMinutes != DefaultSystemAlertMinutes {
		t.Errorf("min_minutes = %d, want %d", rule.MinMinutes, DefaultSystemAlertMinutes)
	}

	// The domain is the one of the catalogue, not 1..100 for everything: a
	// temperature rule cannot be set below 40 and a load rule accepts 0.
	if rule, _ := NormalizeSystemAlert(SystemAlertRule{Metric: SystemAlertTemperature, Threshold: 20}); rule.Threshold != 80 {
		t.Errorf("temperature threshold = %v, want the default 80", rule.Threshold)
	}
	if rule, _ := NormalizeSystemAlert(SystemAlertRule{Metric: SystemAlertLoad, Threshold: 0}); rule.Threshold != 0 {
		t.Errorf("load threshold = %v, want 0 (it is a valid load)", rule.Threshold)
	}
	if rule, _ := NormalizeSystemAlert(SystemAlertRule{Metric: SystemAlertBandwidth, Threshold: 300}); rule.Threshold != 80 {
		t.Errorf("bandwidth threshold = %v, want the default 80", rule.Threshold)
	}

	// A state rule has no window: whatever the caller sends, the stored rule
	// carries zero minutes, so the evaluator never waits for an average it does
	// not compute.
	if rule, _ := NormalizeSystemAlert(SystemAlertRule{Metric: SystemAlertContainer, Threshold: 2, MinMinutes: 30}); rule.MinMinutes != 0 {
		t.Errorf("container min_minutes = %d, want 0", rule.MinMinutes)
	}
	if rule, _ := NormalizeSystemAlert(SystemAlertRule{Metric: SystemAlertSmart, Enabled: true, Threshold: 1, MinMinutes: 60}); rule.MinMinutes != 0 {
		t.Errorf("smart min_minutes = %d, want 0", rule.MinMinutes)
	}
}

// TestNormalizeSystemAlertDefaults pins the default set as a complete rule set:
// a partial input keeps the built-in values of the rules it omits, and an
// unknown metric is dropped instead of being stored.
func TestNormalizeSystemAlertDefaults(t *testing.T) {
	latched := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)
	out := NormalizeSystemAlertDefaults(SystemAlertDefaults{Rules: []SystemAlertRule{
		{Metric: SystemAlertCPU, Enabled: false, Threshold: 95, MinMinutes: 10, Triggered: true, TriggeredAt: &latched},
		{Metric: SystemAlertMetric("battery"), Enabled: true, Threshold: 10, MinMinutes: 5},
	}})
	if len(out.Rules) != len(systemAlertSpecs) {
		t.Fatalf("got %d rules, want the whole catalogue (%d)", len(out.Rules), len(systemAlertSpecs))
	}
	for i, rule := range out.Rules {
		if rule.Metric != systemAlertSpecs[i].Metric {
			t.Fatalf("position %d: got %s, want %s", i, rule.Metric, systemAlertSpecs[i].Metric)
		}
		if rule.Triggered || rule.TriggeredAt != nil {
			t.Errorf("%s kept a latch: %+v", rule.Metric, rule)
		}
	}
	byMetric := map[SystemAlertMetric]SystemAlertRule{}
	for _, rule := range out.Rules {
		byMetric[rule.Metric] = rule
	}
	cpu := byMetric[SystemAlertCPU]
	if cpu.Enabled || cpu.Threshold != 95 || cpu.MinMinutes != 10 {
		t.Errorf("the stored cpu rule was not kept: %+v", cpu)
	}
	if _, ok := byMetric[SystemAlertMetric("battery")]; ok {
		t.Error("an unknown metric reached the normalised set")
	}
	// The rule the caller omitted is the built-in one.
	if disk := byMetric[SystemAlertDisk]; disk.Threshold != DefaultSystemDiskAlertThreshold || !disk.Enabled {
		t.Errorf("disk = %+v, want the built-in rule", disk)
	}
}

// TestEvaluateSystemCountAlert pins the state rules: no window, no warm-up, and
// the count alone decides.
func TestEvaluateSystemCountAlert(t *testing.T) {
	cases := []struct {
		name  string
		rule  SystemAlertRule
		count int64
		want  bool
	}{
		{"one unhealthy container", SystemAlertRule{Metric: SystemAlertContainer, Enabled: true, Threshold: 1}, 1, true},
		{"no unhealthy container", SystemAlertRule{Metric: SystemAlertContainer, Enabled: true, Threshold: 1}, 0, false},
		{"two disks, threshold two", SystemAlertRule{Metric: SystemAlertSmart, Enabled: true, Threshold: 2}, 2, true},
		{"one disk, threshold two", SystemAlertRule{Metric: SystemAlertSmart, Enabled: true, Threshold: 2}, 1, false},
		{"disabled", SystemAlertRule{Metric: SystemAlertContainer, Enabled: false, Threshold: 1}, 5, false},
		{"a sustained rule is not a count rule", SystemAlertRule{Metric: SystemAlertCPU, Enabled: true, Threshold: 1}, 100, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := EvaluateSystemCountAlert(tc.rule, tc.count); got != tc.want {
				t.Fatalf("EvaluateSystemCountAlert = %v, want %v", got, tc.want)
			}
		})
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
		{"a state rule has no window", SystemAlertRule{Metric: SystemAlertContainer, Enabled: true, Threshold: 1}, 99, &covered, false},
		{"disk threshold", SystemAlertRule{Metric: SystemAlertDisk, Enabled: true, Threshold: 85, MinMinutes: 5}, 86, &covered, true},
		{"bandwidth reads in MB/s", SystemAlertRule{Metric: SystemAlertBandwidth, Enabled: true, Threshold: 80, MinMinutes: 5}, 91, &covered, true},
		{"temperature", SystemAlertRule{Metric: SystemAlertTemperature, Enabled: true, Threshold: 80, MinMinutes: 5}, 84, &covered, true},
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

// TestFormatSystemAlert pins the wording of the notification details: the unit
// is the one of the metric, so a bandwidth alert never reads "12.3%".
func TestFormatSystemAlert(t *testing.T) {
	values := []struct {
		metric SystemAlertMetric
		value  float64
		want   string
	}{
		{SystemAlertCPU, 82.44, "82.4%"},
		{SystemAlertBandwidth, 12.34, "12.3 MB/s"},
		{SystemAlertTemperature, 85.04, "85.0°C"},
		{SystemAlertLoad, 12.345, "12.35 load"},
	}
	for _, tc := range values {
		if got := FormatSystemAlertValue(tc.metric, tc.value); got != tc.want {
			t.Errorf("FormatSystemAlertValue(%s) = %q, want %q", tc.metric, got, tc.want)
		}
	}
	thresholds := []struct {
		metric SystemAlertMetric
		value  float64
		want   string
	}{
		{SystemAlertCPU, 80, "80%"},
		{SystemAlertBandwidth, 80, "80 MB/s"},
		{SystemAlertTemperature, 38.5, "38.5°C"},
		{SystemAlertLoad, 10, "10"},
		{SystemAlertContainer, 1, "1"},
	}
	for _, tc := range thresholds {
		if got := FormatSystemAlertThreshold(tc.metric, tc.value); got != tc.want {
			t.Errorf("FormatSystemAlertThreshold(%s) = %q, want %q", tc.metric, got, tc.want)
		}
	}
	if got := SystemAlertCountDetail(SystemAlertContainer, 2, 1); got != "2 unhealthy containers (threshold 1)" {
		t.Errorf("container detail = %q", got)
	}
	if got := SystemAlertCountDetail(SystemAlertSmart, 1, 1); got != "1 failing disks (threshold 1)" {
		t.Errorf("smart detail = %q", got)
	}
}
