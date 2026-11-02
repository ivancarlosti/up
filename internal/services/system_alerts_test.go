package services

import (
	"testing"

	"github.com/ivancarlosti/up/internal/models"
)

// TestSystemEventEnabled pins the mapping between the four hardware events and
// the four toggles of a machine link.
//
// It is the whole contract of the per event switches: a channel that opted out
// must not be attempted, and an event that reaches the wrong flag would either
// deliver something nobody asked for or swallow an alert.
func TestSystemEventEnabled(t *testing.T) {
	cases := []struct {
		name  string
		link  models.SystemNotification
		event models.NotificationEvent
		want  bool
	}{
		{"down on down", models.SystemNotification{OnDown: true}, models.EventSystemDown, true},
		{"down off", models.SystemNotification{OnUp: true, OnThreshold: true, OnRecovered: true}, models.EventSystemDown, false},
		{"up on up", models.SystemNotification{OnUp: true}, models.EventSystemUp, true},
		{"up off", models.SystemNotification{OnDown: true, OnThreshold: true, OnRecovered: true}, models.EventSystemUp, false},
		{"threshold on", models.SystemNotification{OnThreshold: true}, models.EventSystemThreshold, true},
		{"threshold off", models.SystemNotification{OnDown: true, OnUp: true, OnRecovered: true}, models.EventSystemThreshold, false},
		{"recovered on", models.SystemNotification{OnRecovered: true}, models.EventSystemRecovered, true},
		{"recovered off", models.SystemNotification{OnDown: true, OnUp: true, OnThreshold: true}, models.EventSystemRecovered, false},
		// A monitor event never reaches a machine link through DispatchSystem, so
		// the fallback is "allow": it must not silently block a future event.
		{"unknown event", models.SystemNotification{}, models.EventDown, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := systemEventEnabled(tc.link, tc.event); got != tc.want {
				t.Fatalf("systemEventEnabled = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestSameSystemLink pins the comparison that decides whether a save has to
// publish a link: two links that differ only in their four toggles must not be
// considered equal, or a peer would never learn about the change.
func TestSameSystemLink(t *testing.T) {
	base := models.SystemNotification{SystemID: 1, NotificationID: 2, OnDown: true, OnUp: true, OnThreshold: true, OnRecovered: false}
	if !sameSystemLink(base, base) {
		t.Error("a link differs from itself")
	}
	changed := base
	changed.OnRecovered = true
	if sameSystemLink(base, changed) {
		t.Error("a toggled link is considered unchanged")
	}
}

// TestOrderSystemAlertRules pins the display order and the defaults of a machine
// whose rows have not been written yet.
//
// The fallback is the built-in catalogue here because the service carries no
// settings store; with one wired it is the configured default (that path is
// covered by the live test against a scratch database).
func TestOrderSystemAlertRules(t *testing.T) {
	service := &SystemAlertService{}
	rules := service.orderRules(nil)
	if len(rules) != len(models.AllSystemAlertMetrics) {
		t.Fatalf("got %d rules, want %d", len(rules), len(models.AllSystemAlertMetrics))
	}
	for i, rule := range rules {
		if rule.Metric != models.AllSystemAlertMetrics[i] {
			t.Errorf("rule %d is %s, want %s", i, rule.Metric, models.AllSystemAlertMetrics[i])
		}
		if !rule.Enabled {
			t.Errorf("%s should fall back to the default (enabled)", rule.Metric)
		}
	}
	// A stored row always wins over the default, latch included.
	stored := []models.SystemAlert{{Metric: models.SystemAlertCPU, Enabled: false, Threshold: 42, MinMinutes: 9, Triggered: true}}
	for _, rule := range service.orderRules(stored) {
		if rule.Metric != models.SystemAlertCPU {
			continue
		}
		if rule.Enabled || rule.Threshold != 42 || rule.MinMinutes != 9 || !rule.Triggered {
			t.Errorf("the stored cpu rule was not used: %+v", rule)
		}
	}
}
