package services

import (
	"testing"

	"github.com/ivancarlosti/up/internal/models"
)

// TestPropagationFields documents what a template pushes to the monitors that
// follow it: the applicable defaults, never the groups/tags (they belong to the
// monitor) and never an empty channel list (which would mute every follower).
func TestPropagationFields(t *testing.T) {
	template := &models.MonitorTemplate{Type: models.MonitorTypeHTTP}
	seen := map[string]bool{}
	for _, field := range propagationFields(template) {
		seen[field] = true
	}
	if seen["tags"] || seen["group_ids"] {
		t.Fatalf("groups and tags must never be propagated: %v", propagationFields(template))
	}
	if seen["notification_ids"] {
		t.Fatalf("an empty channel list must not be propagated: %v", propagationFields(template))
	}
	if !seen["interval_seconds"] || !seen["config"] {
		t.Fatalf("the defaults must be propagated: %v", propagationFields(template))
	}

	template.Defaults.NotificationIDs = []uint{7}
	withChannels := map[string]bool{}
	for _, field := range propagationFields(template) {
		withChannels[field] = true
	}
	if !withChannels["notification_ids"] {
		t.Fatal("a template that lists channels must propagate them")
	}
}
