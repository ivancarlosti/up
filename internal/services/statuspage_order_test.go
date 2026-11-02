package services

import (
	"testing"

	"github.com/ivancarlosti/up/internal/models"
)

// TestApplyStatusPageOrder covers the ordering of a page: the three modes, the manual
// order used as the tie-break of the other two, a monitor nobody positioned yet, and
// the one promise that matters — the setting NEVER moves a monitor out of its section.
func TestApplyStatusPageOrder(t *testing.T) {
	monitor := func(id uint, name string, monitorType models.MonitorType) *models.Monitor {
		return &models.Monitor{ID: id, Name: name, Type: monitorType}
	}
	// The monitors a page can render: ids 1..6, mixed types and mixed cases.
	newMonitors := func() map[uint]*models.Monitor {
		return map[uint]*models.Monitor{
			1: monitor(1, "beta", models.MonitorTypeTCP),
			2: monitor(2, "Alpha", models.MonitorTypeHTTP),
			3: monitor(3, "gamma", models.MonitorTypeHTTP),
			4: monitor(4, "delta", models.MonitorTypeDNS),
			5: monitor(5, "Epsilon", models.MonitorTypeSSL),
			6: monitor(6, "zeta", models.MonitorTypeKeyword),
		}
	}
	// Two sections: the explicit monitors, then one group.
	sections := []StatusPageSection{
		{Name: "", MonitorIDs: []uint{3, 1, 2}},
		{Name: "Edge", MonitorIDs: []uint{4, 6, 5}},
	}

	cases := []struct {
		name     string
		mode     string
		ranks    map[uint]int
		wantFlat []uint
	}{
		{
			// The default: the ranks the operator dragged in.
			name:     "manual follows the ranks",
			mode:     models.MonitorOrderManual,
			ranks:    map[uint]int{3: 0, 1: 1, 2: 2, 4: 0, 6: 1, 5: 2},
			wantFlat: []uint{3, 1, 2, 4, 6, 5},
		},
		{
			// No rank at all is what a page whose selection was written by an older
			// build looks like: the section keeps the order it already had.
			name:     "manual without ranks keeps the plan",
			mode:     models.MonitorOrderManual,
			wantFlat: []uint{3, 1, 2, 4, 6, 5},
		},
		{
			// A monitor that joined a group after the order was saved sinks to the end
			// of its section instead of jumping to the top.
			name:     "manual puts an unranked monitor last",
			mode:     models.MonitorOrderManual,
			ranks:    map[uint]int{3: 0, 1: 1, 4: 0},
			wantFlat: []uint{3, 1, 2, 4, 6, 5},
		},
		{
			// Case insensitive, and the manual rank decides between two equals.
			name:     "alphabetical by the name the page shows",
			mode:     models.MonitorOrderAlphabetical,
			ranks:    map[uint]int{3: 0, 1: 1, 2: 2, 4: 0, 6: 1, 5: 2},
			wantFlat: []uint{2, 1, 3, 4, 5, 6},
		},
		{
			// "type" compares the rank of the type first; the http pair (3 and 2) is
			// settled by the manual order, which is the whole point of keeping it.
			// In the group, keyword(6) comes before dns(4) and ssl(5).
			name:     "type groups the types and keeps the manual order inside",
			mode:     models.MonitorOrderType,
			ranks:    map[uint]int{3: 0, 1: 1, 2: 2, 4: 0, 6: 1, 5: 2},
			wantFlat: []uint{3, 2, 1, 6, 4, 5},
		},
		{
			name:     "an unknown mode renders the page order",
			mode:     "uptime",
			ranks:    map[uint]int{3: 0, 1: 1, 2: 2, 4: 0, 6: 1, 5: 2},
			wantFlat: []uint{3, 1, 2, 4, 6, 5},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotSections, gotFlat := applyStatusPageOrder(sections, newMonitors(), tc.ranks, tc.mode)
			if len(gotFlat) != len(tc.wantFlat) {
				t.Fatalf("flat = %v, want %v", gotFlat, tc.wantFlat)
			}
			for i, want := range tc.wantFlat {
				if gotFlat[i] != want {
					t.Fatalf("flat = %v, want %v", gotFlat, tc.wantFlat)
				}
			}
			// The section order and the section names never move, and every monitor
			// stays in the section it came from.
			if len(gotSections) != len(sections) {
				t.Fatalf("sections = %d, want %d", len(gotSections), len(sections))
			}
			for i, section := range gotSections {
				if section.Name != sections[i].Name {
					t.Fatalf("section %d renamed to %q", i, section.Name)
				}
				for _, monitorID := range section.MonitorIDs {
					if !containsUint(sections[i].MonitorIDs, monitorID) {
						t.Fatalf("monitor %d left its section: got %v", monitorID, gotSections)
					}
				}
			}
		})
	}
}

// TestApplyStatusPageOrderDoesNotMutateThePlan proves the callers can keep using the
// plan they own: the ordering works on a copy of every section.
func TestApplyStatusPageOrderDoesNotMutateThePlan(t *testing.T) {
	sections := []StatusPageSection{{Name: "Edge", MonitorIDs: []uint{2, 1}}}
	monitors := map[uint]*models.Monitor{
		1: {ID: 1, Name: "one", Type: models.MonitorTypeHTTP},
		2: {ID: 2, Name: "two", Type: models.MonitorTypeHTTP},
	}
	applyStatusPageOrder(sections, monitors, map[uint]int{1: 0, 2: 1}, models.MonitorOrderAlphabetical)
	if sections[0].MonitorIDs[0] != 2 {
		t.Fatalf("the plan was reordered in place: %v", sections[0].MonitorIDs)
	}
}

// TestCompareStatusPageMonitors pins the last tie-break: the id, so two nodes that
// render the same page agree on the last pair the other keys cannot separate.
func TestCompareStatusPageMonitors(t *testing.T) {
	a := &models.Monitor{ID: 7, Name: "same", Type: models.MonitorTypeHTTP}
	b := &models.Monitor{ID: 9, Name: "same", Type: models.MonitorTypeHTTP}
	for _, mode := range models.AllMonitorOrders() {
		if got := compareStatusPageMonitors(mode, a, b, 0, 0); got >= 0 {
			t.Fatalf("%s: the lower id must come first, got %d", mode, got)
		}
		if got := compareStatusPageMonitors(mode, a, a, 3, 3); got != 0 {
			t.Fatalf("%s: a monitor must compare equal to itself, got %d", mode, got)
		}
	}
	// The rank outranks the id: the monitor an operator moved up wins.
	if got := compareStatusPageMonitors(models.MonitorOrderManual, b, a, 0, 1); got >= 0 {
		t.Fatalf("the lower rank must come first, got %d", got)
	}
	// The mode key outranks the rank: a page sorted by type ignores the manual
	// position between two different types.
	tcp := &models.Monitor{ID: 1, Name: "zzz", Type: models.MonitorTypeTCP}
	http := &models.Monitor{ID: 2, Name: "aaa", Type: models.MonitorTypeHTTP}
	if got := compareStatusPageMonitors(models.MonitorOrderType, http, tcp, 5, 0); got >= 0 {
		t.Fatalf("http must come before tcp whatever the rank, got %d", got)
	}
}

// containsUint reports whether the slice holds the id (a helper of the cases above).
func containsUint(values []uint, want uint) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
