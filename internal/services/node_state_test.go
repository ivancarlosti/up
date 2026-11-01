package services

import (
	"strings"
	"testing"
	"time"

	"github.com/ivancarlosti/up/internal/models"
)

// TestGroupByVoteWindow pins the bucketing the boot seed relies on: monitors
// whose vote window has the same width share one statement, the groups come out
// narrowest-first, and the window itself is the one the voting uses - so the seed
// can never copy a verdict the evaluator would then discard as stale.
func TestGroupByVoteWindow(t *testing.T) {
	cases := []struct {
		name     string
		monitors []monitorInterval
		want     []time.Duration
		wantIDs  [][]uint
	}{
		{"nothing to seed", nil, nil, nil},
		{
			// 60 s and 30 s both floor at the 2 minute minimum of voteWindow.
			name:     "the default interval shares one group",
			monitors: []monitorInterval{{ID: 1, IntervalSeconds: 60}, {ID: 2, IntervalSeconds: 30}},
			want:     []time.Duration{2 * time.Minute},
			wantIDs:  [][]uint{{1, 2}},
		},
		{
			// The slow monitor keeps its own - 24 h - bound instead of dragging the
			// fast ones along, and the groups are ordered narrowest first.
			name: "a slow monitor gets its own window",
			monitors: []monitorInterval{
				{ID: 7, IntervalSeconds: 43200},
				{ID: 3, IntervalSeconds: 120},
				{ID: 4, IntervalSeconds: 60},
			},
			want:    []time.Duration{2 * time.Minute, 4 * time.Minute, 24 * time.Hour},
			wantIDs: [][]uint{{4}, {3}, {7}},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := groupByVoteWindow(tc.monitors)
			if len(got) != len(tc.want) {
				t.Fatalf("groupByVoteWindow() returned %d groups, want %d: %+v", len(got), len(tc.want), got)
			}
			for i := range got {
				if got[i].window != tc.want[i] {
					t.Errorf("group %d window = %s, want %s", i, got[i].window, tc.want[i])
				}
				if len(got[i].ids) != len(tc.wantIDs[i]) {
					t.Fatalf("group %d ids = %v, want %v", i, got[i].ids, tc.wantIDs[i])
				}
				for j := range got[i].ids {
					if got[i].ids[j] != tc.wantIDs[i][j] {
						t.Errorf("group %d id %d = %d, want %d", i, j, got[i].ids[j], tc.wantIDs[i][j])
					}
				}
			}
		})
	}
}

// TestChunkIDs pins the split that keeps a seed statement bindable: an empty list
// is not a statement at all, a list that fits is one chunk, and the last chunk of
// a longer list is never padded.
func TestChunkIDs(t *testing.T) {
	if got := chunkIDs(nil, 2); got != nil {
		t.Fatalf("chunkIDs(nil) = %v, want nil", got)
	}
	if got := chunkIDs([]uint{1, 2}, 2); len(got) != 1 || len(got[0]) != 2 {
		t.Fatalf("chunkIDs(2 ids, size 2) = %v, want one chunk of 2", got)
	}
	got := chunkIDs([]uint{1, 2, 3, 4, 5}, 2)
	want := [][]uint{{1, 2}, {3, 4}, {5}}
	if len(got) != len(want) {
		t.Fatalf("chunkIDs(5 ids, size 2) = %v, want %v", got, want)
	}
	for i := range want {
		if len(got[i]) != len(want[i]) {
			t.Fatalf("chunk %d = %v, want %v", i, got[i], want[i])
		}
		for j := range want[i] {
			if got[i][j] != want[i][j] {
				t.Fatalf("chunk %d = %v, want %v", i, got[i], want[i])
			}
		}
	}
}

// TestNodeStateStatementsBindTheirArguments counts the placeholders of the two
// hand written statements of the per node state table.
//
// A missing or an extra `?` is not a compile error: it is a query that fails at
// runtime, on the write path of every probe (saveNodeState) or at boot (the seed),
// which is exactly where a mistake is most expensive. Counting is enough because
// neither statement can be built from a variable list.
func TestNodeStateStatementsBindTheirArguments(t *testing.T) {
	cases := []struct {
		name   string
		query  string
		params int
	}{
		// 8 for the INSERT and 2 for each of the 6 assignments of the UPDATE.
		{"upsert", monitorNodeStateUpsert, 8 + 6*2},
		// One for the id list, one for the window bound.
		{"seed", nodeStateSeedQuery, 2},
	}
	for _, tc := range cases {
		if got := strings.Count(tc.query, "?"); got != tc.params {
			t.Errorf("%s: %d placeholders, want %d", tc.name, got, tc.params)
		}
	}
	// The guard of the upsert is what keeps an out of order write from replacing a
	// fresher verdict, and it only works while checked_at is the LAST assignment:
	// every comparison before it has to read the stored value.
	updates := monitorNodeStateUpsert[strings.Index(monitorNodeStateUpsert, "ON DUPLICATE KEY UPDATE"):]
	if last := updates[strings.LastIndex(updates, ",")+1:]; !strings.HasPrefix(strings.TrimSpace(last), "checked_at") {
		t.Errorf("checked_at is not the last assignment of the upsert: %q", strings.TrimSpace(last))
	}
}

// TestHeartbeatToAggregate is a small guard for the mapping the votes rely on:
// every status the probes store has an aggregated counterpart (an unmapped one
// would silently become AggregateUnknown and never fail a monitor).
func TestHeartbeatToAggregateMapping(t *testing.T) {
	cases := map[models.HeartbeatStatus]models.AggregateStatus{
		models.StatusUp:          models.AggregateUp,
		models.StatusDown:        models.AggregateDown,
		models.StatusPending:     models.AggregatePending,
		models.StatusMaintenance: models.AggregateMaintenance,
	}
	for heartbeat, want := range cases {
		if got := heartbeatToAggregate(heartbeat); got != want {
			t.Errorf("heartbeatToAggregate(%s) = %s, want %s", heartbeat, got, want)
		}
	}
}
