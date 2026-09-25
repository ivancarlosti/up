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

// TestGroupScope documents the scope of a link run: no selection means every
// monitor of the type, a selection narrows the run to the members of the chosen
// groups, and a group with no member is an empty scope (never "everything").
func TestGroupScope(t *testing.T) {
	monitors := []*models.Monitor{{ID: 1}, {ID: 2}, {ID: 3}}

	all := groupScope(monitors, nil)
	if len(all) != 3 {
		t.Fatalf("an unselected scope must keep every monitor: %v", all)
	}

	scoped := groupScope(monitors, map[uint]bool{2: true})
	if len(scoped) != 1 || scoped[0].ID != 2 {
		t.Fatalf("only the members must survive the scope: %v", scoped)
	}

	empty := groupScope(monitors, map[uint]bool{})
	if len(empty) != 0 {
		t.Fatalf("a group without members must scope out every monitor: %v", empty)
	}
}
