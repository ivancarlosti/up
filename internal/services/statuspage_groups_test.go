package services

import (
	"reflect"
	"testing"

	"github.com/ivancarlosti/up/internal/models"
)

// TestPlanStatusPage covers the resolution of a status page: the explicit
// monitors, the linked groups, the de-duplication between them and the order the
// public page renders (which is also the order of the flat `monitors` list).
func TestPlanStatusPage(t *testing.T) {
	items := func(ids ...uint) []models.StatusPageMonitor {
		out := make([]models.StatusPageMonitor, 0, len(ids))
		for i, id := range ids {
			out = append(out, models.StatusPageMonitor{MonitorID: id, SortOrder: i})
		}
		return out
	}
	links := func(ids ...uint) []models.StatusPageGroupLink {
		out := make([]models.StatusPageGroupLink, 0, len(ids))
		for i, id := range ids {
			out = append(out, models.StatusPageGroupLink{GroupID: id, SortOrder: i})
		}
		return out
	}

	cases := []struct {
		name         string
		items        []models.StatusPageMonitor
		links        []models.StatusPageGroupLink
		members      map[uint][]uint
		names        map[uint]string
		wantSections []StatusPageSection
		wantFlat     []uint
	}{
		{
			name:         "explicit monitors only",
			items:        items(3, 5),
			wantSections: []StatusPageSection{{Name: "", MonitorIDs: []uint{3, 5}}},
			wantFlat:     []uint{3, 5},
		},
		{
			name:         "group only",
			links:        links(7),
			members:      map[uint][]uint{7: {4, 2}},
			names:        map[uint]string{7: "Databases"},
			wantSections: []StatusPageSection{{Name: "Databases", MonitorIDs: []uint{4, 2}}},
			wantFlat:     []uint{4, 2},
		},
		{
			// The explicit selection wins the first slot, the group adds the
			// monitors that are not listed explicitly.
			name:    "explicit plus group",
			items:   items(9),
			links:   links(7),
			members: map[uint][]uint{7: {9, 4}},
			names:   map[uint]string{7: "Databases"},
			wantSections: []StatusPageSection{
				{Name: "", MonitorIDs: []uint{9}},
				{Name: "Databases", MonitorIDs: []uint{4}},
			},
			wantFlat: []uint{9, 4},
		},
		{
			// A monitor in two groups is rendered once, in the first group that
			// claims it.
			name:    "deduplication between groups",
			links:   links(7, 8),
			members: map[uint][]uint{7: {1, 2}, 8: {2, 3}},
			names:   map[uint]string{7: "First", 8: "Second"},
			wantSections: []StatusPageSection{
				{Name: "First", MonitorIDs: []uint{1, 2}},
				{Name: "Second", MonitorIDs: []uint{3}},
			},
			wantFlat: []uint{1, 2, 3},
		},
		{
			// An empty group is skipped: an empty heading on the public page
			// looks like a bug.
			name:         "empty group is skipped",
			links:        links(7, 8),
			members:      map[uint][]uint{7: {}, 8: {5}},
			names:        map[uint]string{7: "Empty", 8: "Full"},
			wantSections: []StatusPageSection{{Name: "Full", MonitorIDs: []uint{5}}},
			wantFlat:     []uint{5},
		},
		{
			name:         "display name overrides the group name",
			links:        []models.StatusPageGroupLink{{GroupID: 7, DisplayName: "Edge"}},
			members:      map[uint][]uint{7: {6}},
			names:        map[uint]string{7: "Databases"},
			wantSections: []StatusPageSection{{Name: "Edge", MonitorIDs: []uint{6}}},
			wantFlat:     []uint{6},
		},
		{
			name:         "duplicated explicit ids are ignored",
			items:        items(3, 3, 4),
			wantSections: []StatusPageSection{{Name: "", MonitorIDs: []uint{3, 4}}},
			wantFlat:     []uint{3, 4},
		},
		{
			name:         "nothing configured",
			wantSections: []StatusPageSection{},
			wantFlat:     []uint{},
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			sections, flat := planStatusPage(testCase.items, testCase.links, testCase.members, testCase.names)
			if !reflect.DeepEqual(sections, testCase.wantSections) {
				t.Fatalf("sections = %+v, want %+v", sections, testCase.wantSections)
			}
			if !reflect.DeepEqual(flat, testCase.wantFlat) {
				t.Fatalf("flat = %v, want %v", flat, testCase.wantFlat)
			}
			// The flat list must always be the concatenation of the sections:
			// page.monitors and page.groups are rendered from the same order.
			concatenated := []uint{}
			for _, section := range sections {
				concatenated = append(concatenated, section.MonitorIDs...)
			}
			if !reflect.DeepEqual(concatenated, flat) {
				t.Fatalf("the flat list (%v) is not the sections in order (%v)", flat, concatenated)
			}
		})
	}
}
