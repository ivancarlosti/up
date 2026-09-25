package models

import "testing"

// TestNormalizeHeartbeatRetentionDays pins the retention rules: 0 means "never
// purge", a negative input reads as the documented default (not "delete
// everything") and an absurd value is clamped.
func TestNormalizeHeartbeatRetentionDays(t *testing.T) {
	cases := []struct {
		in   int
		want int
	}{
		{0, 0},
		{30, 30},
		{180, 180},
		{-1, DefaultHeartbeatRetentionDays},
		{MaxHeartbeatRetentionDays + 1, MaxHeartbeatRetentionDays},
	}
	for _, tc := range cases {
		if got := NormalizeHeartbeatRetentionDays(tc.in); got != tc.want {
			t.Errorf("NormalizeHeartbeatRetentionDays(%d) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

// TestNormalizeUptimeWindowHours covers the "closest offered window" rule: an
// unknown period must still render a label the UI knows.
func TestNormalizeUptimeWindowHours(t *testing.T) {
	cases := []struct {
		in   int
		want int
	}{
		{0, DefaultUptimeWindowHours},
		{24, 24},
		{48, 24},
		{168, 168},
		{200, 168},
		{300, 336},
		{720, 720},
		{10000, 720},
	}
	for _, tc := range cases {
		if got := NormalizeUptimeWindowHours(tc.in); got != tc.want {
			t.Errorf("NormalizeUptimeWindowHours(%d) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

// TestUptimeWindowLabel pins the labels the UI and the docs share.
func TestUptimeWindowLabel(t *testing.T) {
	cases := []struct {
		in   int
		want string
	}{
		{24, "24h"},
		{168, "7d"},
		{336, "14d"},
		{720, "30d"},
	}
	for _, tc := range cases {
		if got := UptimeWindowLabel(tc.in); got != tc.want {
			t.Errorf("UptimeWindowLabel(%d) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestStatusPageEffectiveUptimeWindow covers the inherit rule of a status page.
func TestStatusPageEffectiveUptimeWindow(t *testing.T) {
	inherit := &StatusPage{UptimeWindowHours: 0}
	if got := inherit.EffectiveUptimeWindow(168); got != 168 {
		t.Errorf("inherit = %d, want the global 168", got)
	}
	override := &StatusPage{UptimeWindowHours: 336}
	if got := override.EffectiveUptimeWindow(24); got != 336 {
		t.Errorf("override = %d, want 336", got)
	}
}
