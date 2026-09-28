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

// TestUnlinkScope documents the other half of the scope rule: a group-scoped run
// replaces the scope of the template, so the monitors that follow it from
// outside the selection are detached. The type scope has no outside, and a
// monitor that is not following the template is never touched (it is not in the
// follower list at all).
func TestUnlinkScope(t *testing.T) {
	followers := []*models.Monitor{{ID: 1, TemplateUUID: "t"}, {ID: 2, TemplateUUID: "t"}, {ID: 3, TemplateUUID: "t"}}

	partial := unlinkScope(followers, []*models.Monitor{{ID: 2}})
	if len(partial) != 2 || partial[0].ID != 1 || partial[1].ID != 3 {
		t.Fatalf("the followers outside the scope must be detached: %v", partial)
	}

	// The whole scope: a run over a group that holds every follower detaches
	// nobody.
	none := unlinkScope(followers, followers)
	if len(none) != 0 {
		t.Fatalf("a scope that covers every follower must detach nobody: %v", none)
	}

	// A group with no member is an empty scope: it detaches every follower,
	// exactly like the group with no member that links nothing.
	all := unlinkScope(followers, nil)
	if len(all) != len(followers) {
		t.Fatalf("an empty scope must detach every follower: %v", all)
	}

	// The monitor that is not following the template is not a follower, so a run
	// over a group it does not belong to leaves it alone.
	untouched := unlinkScope([]*models.Monitor{{ID: 1, TemplateUUID: "t"}}, nil)
	if len(untouched) != 1 || untouched[0].ID != 1 {
		t.Fatalf("only the followers can be detached: %v", untouched)
	}
}
