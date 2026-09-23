package database

import (
	"testing"
	"time"

	"github.com/ivancarlosti/up/internal/models"
)

// mkNode builds a node row for the repair tests.
func mkNode(id uint, nodeID string, created time.Time) models.Node {
	return models.Node{ID: id, NodeID: nodeID, IsPrimary: true, CreatedAt: created}
}

// TestPickPrimary covers the repair of a cluster that ended up with several
// primary rows: the oldest node keeps the role, node_id breaks a tie, and the
// outcome must not depend on the order the rows came back in (the function sorts
// a copy itself).
func TestPickPrimary(t *testing.T) {
	base := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

	cases := []struct {
		name    string
		in      []models.Node
		wantOn  string
		wantOff []string
	}{
		{name: "no primary is a no-op", in: nil, wantOn: "", wantOff: nil},
		{
			name:   "a single primary is kept",
			in:     []models.Node{mkNode(1, "a", base)},
			wantOn: "a", wantOff: nil,
		},
		{
			name:   "the oldest is kept whatever the row order",
			in:     []models.Node{mkNode(2, "b", base.Add(time.Minute)), mkNode(1, "a", base)},
			wantOn: "a", wantOff: []string{"b"},
		},
		{
			name:   "node_id breaks a tie",
			in:     []models.Node{mkNode(2, "b", base), mkNode(1, "a", base)},
			wantOn: "a", wantOff: []string{"b"},
		},
		{
			name: "three primaries keep exactly one",
			in: []models.Node{
				mkNode(3, "c", base),
				mkNode(2, "b", base.Add(-time.Hour)),
				mkNode(1, "a", base.Add(time.Hour)),
			},
			wantOn: "b", wantOff: []string{"c", "a"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			keep, demote := pickPrimary(tc.in)
			if keep.NodeID != tc.wantOn {
				t.Fatalf("keep = %q, want %q", keep.NodeID, tc.wantOn)
			}
			if len(demote) != len(tc.wantOff) {
				t.Fatalf("demoted %d node(s), want %d", len(demote), len(tc.wantOff))
			}
			for i, want := range tc.wantOff {
				if demote[i].NodeID != want {
					t.Errorf("demoted[%d] = %q, want %q", i, demote[i].NodeID, want)
				}
			}
		})
	}
}

// TestPickPrimaryKeepsExactlyOne is the invariant that matters for the cluster:
// however many rows claim the role, what comes back is one keeper and the rest.
func TestPickPrimaryKeepsExactlyOne(t *testing.T) {
	base := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	in := []models.Node{
		mkNode(1, "a", base),
		mkNode(2, "b", base),
		mkNode(3, "c", base),
		mkNode(4, "d", base),
	}
	_, demote := pickPrimary(in)
	if len(demote) != len(in)-1 {
		t.Fatalf("kept %d, want 1", len(in)-len(demote))
	}
}
