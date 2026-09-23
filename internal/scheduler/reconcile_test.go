package scheduler

import (
	"reflect"
	"testing"
)

// TestPlanReconcile covers the pure core of the periodic reconciliation: the
// difference between the monitors this node must run (the shared database, so
// it can contain monitors created on another node) and the running workers.
func TestPlanReconcile(t *testing.T) {
	cases := []struct {
		name     string
		expected []uint
		running  []uint
		want     reconcilePlan
		changed  bool
	}{
		{
			name:     "nothing running, nothing expected",
			expected: nil,
			running:  nil,
			want:     reconcilePlan{},
			changed:  false,
		},
		{
			name:     "boot starts every monitor",
			expected: []uint{3, 1, 2},
			running:  nil,
			want:     reconcilePlan{Start: []uint{1, 2, 3}},
			changed:  true,
		},
		{
			name:     "no change keeps every worker",
			expected: []uint{2, 1},
			running:  []uint{1, 2},
			want:     reconcilePlan{Keep: []uint{1, 2}},
			changed:  false,
		},
		{
			// The case the periodic reconciliation exists for: a monitor
			// created on another node appears in the shared database.
			name:     "monitor created on another node",
			expected: []uint{1, 2, 7},
			running:  []uint{1, 2},
			want:     reconcilePlan{Start: []uint{7}, Keep: []uint{1, 2}},
			changed:  true,
		},
		{
			name:     "monitor deleted on another node",
			expected: []uint{1},
			running:  []uint{1, 2, 9},
			want:     reconcilePlan{Stop: []uint{2, 9}, Keep: []uint{1}},
			changed:  true,
		},
		{
			name:     "monitor paused on another node",
			expected: []uint{4},
			running:  []uint{4, 5},
			want:     reconcilePlan{Stop: []uint{5}, Keep: []uint{4}},
			changed:  true,
		},
		{
			name:     "run_on moved the monitor to another node",
			expected: []uint{8},
			running:  []uint{6},
			want:     reconcilePlan{Start: []uint{8}, Stop: []uint{6}},
			changed:  true,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got := planReconcile(testCase.expected, testCase.running)
			if !reflect.DeepEqual(got, testCase.want) {
				t.Fatalf("planReconcile(%v, %v) = %+v, want %+v",
					testCase.expected, testCase.running, got, testCase.want)
			}
			if got.changed() != testCase.changed {
				t.Fatalf("changed() = %t, want %t", got.changed(), testCase.changed)
			}
		})
	}
}

// TestPlanReconcileIsDeterministic documents why the plan is sorted: the callers
// build the id lists from maps, whose order is not stable, and the log line (and
// therefore the operator's debugging) must not change on every pass.
func TestPlanReconcileIsDeterministic(t *testing.T) {
	first := planReconcile([]uint{9, 4, 7, 1}, []uint{3, 8, 2})
	second := planReconcile([]uint{1, 7, 9, 4}, []uint{2, 3, 8})
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("the plan depends on the input order: %+v != %+v", first, second)
	}
	want := reconcilePlan{Start: []uint{1, 4, 7, 9}, Stop: []uint{2, 3, 8}}
	if !reflect.DeepEqual(first, want) {
		t.Fatalf("plan = %+v, want %+v", first, want)
	}
}
