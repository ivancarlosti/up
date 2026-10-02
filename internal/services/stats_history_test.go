package services

import (
	"math"
	"strings"
	"testing"
	"time"

	"github.com/ivancarlosti/up/internal/models"
)

// TestHeartbeatRollupUpsertParameterCount pins the shape of the incremental
// rollup statement: the VALUES list holds one placeholder per column (total is
// the literal 1) and the UPDATE clause adds the nine counters it accumulates, so
// the Exec call must pass exactly 20 arguments. A missing one would make every
// heartbeat fail to roll up, silently, because Record only logs that error.
func TestHeartbeatRollupUpsertParameterCount(t *testing.T) {
	if got, want := strings.Count(heartbeatRollupUpsert, "?"), 20; got != want {
		t.Fatalf("heartbeatRollupUpsert has %d placeholders, want %d", got, want)
	}
}

// TestQueryTimeFloorsToStoredPrecision guards the precision of every bound the
// window queries bind.
//
// A bound carrying nanoseconds is sent as a temporal literal finer than the
// datetime(3) column it is compared with, and the server then stops using it as
// an index range: the boundary query of a listing is planned as if the range
// covered the whole retained history (measured on a 5 M heartbeat fixture:
// 4 873 349 estimated rows instead of 32 499), which turns a dashboard load from
// under a second into twenty. The floor is therefore not cosmetic, and it has to
// survive in the layout the query is built from.
func TestQueryTimeFloorsToStoredPrecision(t *testing.T) {
	in := time.Date(2026, 10, 1, 20, 36, 27, 303124327, time.UTC)
	got := queryTime(in)
	want := time.Date(2026, 10, 1, 20, 36, 27, 303000000, time.UTC)
	if !got.Equal(want) {
		t.Fatalf("queryTime(%s) = %s, want %s", in, got, want)
	}
	if got.Unix() != in.Unix() {
		t.Fatalf("queryTime must not move the instant to another second: %s -> %s", in, got)
	}

	// Whatever the caller passes, the bounds of the periods - the values the
	// boundary query binds - are stored precision.
	set := newHistoryWindowSet(in, 720)
	for _, window := range set.boundaryRanges() {
		if window.start.Nanosecond()%int(storedPrecision) != 0 {
			t.Errorf("boundary range starts at %s: finer than the stored precision", window.start)
		}
		if !window.end.IsZero() && window.end.Nanosecond()%int(storedPrecision) != 0 {
			t.Errorf("boundary range ends at %s: finer than the stored precision", window.end)
		}
	}
	for _, spec := range set.windows {
		if spec.start.Nanosecond()%int(storedPrecision) != 0 {
			t.Errorf("%d h window starts at %s: finer than the stored precision", spec.hours, spec.start)
		}
	}
}

// TestCeilHour covers the round-up that splits a window between the rollups and
// the raw heartbeats: an instant already on the hour is left alone, anything
// else moves to the next one.
func TestCeilHour(t *testing.T) {
	cases := []struct {
		name string
		in   time.Time
		want time.Time
	}{
		{
			"already on the hour",
			time.Date(2026, 9, 26, 21, 0, 0, 0, time.UTC),
			time.Date(2026, 9, 26, 21, 0, 0, 0, time.UTC),
		},
		{
			"one second past the hour",
			time.Date(2026, 9, 26, 21, 0, 1, 0, time.UTC),
			time.Date(2026, 9, 26, 22, 0, 0, 0, time.UTC),
		},
		{
			"mid hour",
			time.Date(2026, 9, 26, 21, 17, 42, 0, time.UTC),
			time.Date(2026, 9, 26, 22, 0, 0, 0, time.UTC),
		},
		{
			"last millisecond of the hour",
			time.Date(2026, 9, 26, 21, 59, 59, 999000000, time.UTC),
			time.Date(2026, 9, 26, 22, 0, 0, 0, time.UTC),
		},
	}
	for _, tc := range cases {
		if got := ceilHour(tc.in); !got.Equal(tc.want) {
			t.Errorf("%s: ceilHour(%s) = %s, want %s", tc.name, tc.in, got, tc.want)
		}
	}
}

// TestHistoryWindowSetLayout pins the split of every period: the hour in
// progress is never read from a rollup, the selected window is deduplicated
// against the fixed ones, and the rollup horizon starts at the oldest bucket the
// 30 day window can reach.
func TestHistoryWindowSetLayout(t *testing.T) {
	now := time.Date(2026, 9, 26, 21, 17, 42, 0, time.UTC)

	selected := newHistoryWindowSet(now, 720)
	if len(selected.windows) != 3 {
		t.Fatalf("720 h selected: %d windows, want 3 (24/168/720 deduplicated)", len(selected.windows))
	}
	if got := selected.endHour; !got.Equal(time.Date(2026, 9, 26, 21, 0, 0, 0, time.UTC)) {
		t.Errorf("endHour = %s", got)
	}
	if got := selected.rollupStart; !got.Equal(time.Date(2026, 8, 27, 21, 0, 0, 0, time.UTC)) {
		t.Errorf("rollupStart = %s", got)
	}

	// The 24 h window starts mid hour, so that hour is read raw and the rollups
	// begin at the next boundary.
	day := selected.indexOf[24]
	if got, want := selected.windows[day].leadEnd, time.Date(2026, 9, 25, 22, 0, 0, 0, time.UTC); !got.Equal(want) {
		t.Errorf("24 h leadEnd = %s, want %s", got, want)
	}
	if selected.rollupInside(day, time.Date(2026, 9, 25, 21, 0, 0, 0, time.UTC)) {
		t.Error("the hour the 24 h window starts in must not be counted from a rollup")
	}
	if !selected.rollupInside(day, time.Date(2026, 9, 25, 22, 0, 0, 0, time.UTC)) {
		t.Error("the first whole hour of the 24 h window must be counted from a rollup")
	}
	if selected.rollupInside(day, selected.endHour) {
		t.Error("the hour in progress must not be counted from a rollup")
	}

	// What the rollups do not cover is read raw: the hour in progress, open
	// ended, and the hour every window starts in. Those are the boundary ranges
	// the query aggregates, and each one knows the periods it feeds.
	ranges := selected.boundaryRanges()
	if got := len(ranges); got != len(selected.windows)+1 {
		t.Fatalf("%d boundary ranges for %d windows, want one per window plus the hour in progress",
			got, len(selected.windows))
	}
	if !ranges[0].start.Equal(selected.endHour) || !ranges[0].end.IsZero() {
		t.Errorf("the hour in progress must be the open ended boundary range, got [%s, %s)",
			ranges[0].start, ranges[0].end)
	}
	if !ranges[0].serves(day) {
		t.Error("the hour in progress belongs to every period, the 24 h one included")
	}
	leadStart, leadEnd := selected.windows[day].start, selected.windows[day].leadEnd
	lead := 0
	for _, window := range ranges[1:] {
		if window.start.Equal(leadStart) && window.end.Equal(leadEnd) {
			lead++
			if len(window.windows) != 1 || !window.serves(day) {
				t.Errorf("the partial start hour of the 24 h window must feed it alone, feeds %v", window.windows)
			}
		}
	}
	if lead != 1 {
		t.Errorf("the partial start hour of the 24 h window appears in %d boundary ranges, want 1", lead)
	}

	// A window that is also one of the fixed ones must not be laid out twice.
	collapsed := newHistoryWindowSet(now, 24)
	if len(collapsed.windows) != 3 {
		t.Fatalf("24 h selected: %d windows, want 3 (the selection is the 24 h window)", len(collapsed.windows))
	}

	// 14 days adds a fourth period and does not move the horizon.
	fourteen := newHistoryWindowSet(now, 336)
	if len(fourteen.windows) != 4 {
		t.Fatalf("336 h selected: %d windows, want 4", len(fourteen.windows))
	}
	if got := fourteen.rollupStart; !got.Equal(selected.rollupStart) {
		t.Errorf("336 h rollupStart = %s, want %s", got, selected.rollupStart)
	}
}

// TestHistoryBoundaryRangesDisjoint checks the property the query relies on: the
// raw ranges never overlap, so a heartbeat is aggregated by exactly one range and
// can never be counted twice, and every range declares the periods it feeds.
func TestHistoryBoundaryRangesDisjoint(t *testing.T) {
	now := time.Date(2026, 9, 26, 21, 17, 42, 0, time.UTC)
	set := newHistoryWindowSet(now, 336)
	ranges := set.boundaryRanges()

	if len(ranges) != len(set.windows)+1 {
		t.Fatalf("%d ranges for %d windows, want one per window plus the hour in progress",
			len(ranges), len(set.windows))
	}
	if ranges[0].start != set.endHour || !ranges[0].end.IsZero() {
		t.Fatalf("the first range must be the open ended hour in progress, got [%s, %s)", ranges[0].start, ranges[0].end)
	}
	if len(ranges[0].windows) != len(set.windows) {
		t.Errorf("the hour in progress feeds %v, want every period", ranges[0].windows)
	}
	tail := ranges[0]
	for i := 1; i < len(ranges); i++ {
		if ranges[i].end.After(tail.start) {
			t.Errorf("range %d [%s, %s) reaches into the hour in progress", i, ranges[i].start, ranges[i].end)
		}
		for j := i + 1; j < len(ranges); j++ {
			if ranges[i].start.Before(ranges[j].end) && ranges[j].start.Before(ranges[i].end) {
				t.Errorf("ranges %d [%s, %s) and %d [%s, %s) overlap",
					i, ranges[i].start, ranges[i].end, j, ranges[j].start, ranges[j].end)
			}
		}
	}
	// Every window must have its partial start hour covered by one range, and by
	// a range that feeds that window and no other: a partial hour belongs to the
	// period it starts.
	for i, spec := range set.windows {
		if !spec.start.Before(spec.leadEnd) {
			continue
		}
		found := false
		for _, window := range ranges {
			if window.start.Equal(spec.start) && window.end.Equal(spec.leadEnd) {
				found = true
				if len(window.windows) != 1 || !window.serves(i) {
					t.Errorf("the start hour of the %d h window feeds %v, want only that period",
						spec.hours, window.windows)
				}
				break
			}
		}
		if !found {
			t.Errorf("no boundary range for the %d h window [%s, %s)", spec.hours, spec.start, spec.leadEnd)
		}
	}
}

// TestHistorySplitMatchesRawAggregate is the equivalence proof of the change:
// the same heartbeats, reduced into hourly rollups exactly like the upsert does
// on write, completed by the boundary ranges aggregated exactly like
// historyBoundaryAggregate does in SQL, must produce the counters and the latency
// figures of the single raw aggregate the query used to run.
func TestHistorySplitMatchesRawAggregate(t *testing.T) {
	now := time.Date(2026, 9, 26, 21, 17, 42, 0, time.UTC)
	set := newHistoryWindowSet(now, 336)

	type beat struct {
		at      time.Time
		status  models.HeartbeatStatus
		latency int64
	}
	statuses := []models.HeartbeatStatus{
		models.StatusUp, models.StatusUp, models.StatusDown,
		models.StatusUp, models.StatusPending, models.StatusMaintenance,
	}
	var beats []beat
	latency := int64(1)
	for at := now.Add(-time.Duration(models.HeartbeatRollupHorizonHours) * time.Hour); !at.After(now); at = at.Add(30 * time.Minute) {
		beats = append(beats, beat{at: at, status: statuses[len(beats)%len(statuses)], latency: latency})
		latency = latency*37%1500 + 1
	}

	// The rollup table the incremental update would have filled.
	rollups := map[time.Time]*historyRollupRow{}
	for _, b := range beats {
		bucket := models.HeartbeatBucket(b.at)
		row := rollups[bucket]
		if row == nil {
			row = &historyRollupRow{MonitorID: 1, BucketAt: bucket}
			rollups[bucket] = row
		}
		if row.Total == 0 {
			row.LatencyMin, row.LatencyMax = b.latency, b.latency
		} else {
			if b.latency < row.LatencyMin {
				row.LatencyMin = b.latency
			}
			if b.latency > row.LatencyMax {
				row.LatencyMax = b.latency
			}
		}
		row.Total++
		switch b.status {
		case models.StatusUp:
			row.Up++
			row.UpLatencySum += b.latency
			row.UpLatencyCount++
		case models.StatusDown:
			row.Down++
		case models.StatusPending:
			row.Pending++
		case models.StatusMaintenance:
			row.Maintenance++
		}
	}

	// The boundary ranges historyBoundaryHeartbeats aggregates, reduced here the
	// way the SQL of each branch does it: one row per range for the single
	// monitor, with the counters of the beats it covers.
	ranges := set.boundaryRanges()
	boundary := make([]historyBoundaryRow, 0, len(ranges))
	for i, window := range ranges {
		row := historyBoundaryRow{RangeIndex: i, MonitorID: 1}
		for _, b := range beats {
			if b.at.Before(window.start) || (!window.end.IsZero() && !b.at.Before(window.end)) {
				continue
			}
			if row.Total == 0 {
				row.LatencyMin, row.LatencyMax = b.latency, b.latency
			} else {
				if b.latency < row.LatencyMin {
					row.LatencyMin = b.latency
				}
				if b.latency > row.LatencyMax {
					row.LatencyMax = b.latency
				}
			}
			row.Total++
			switch b.status {
			case models.StatusUp:
				row.Up++
				row.UpLatencySum += b.latency
				row.UpLatencyCount++
			case models.StatusDown:
				row.Down++
			case models.StatusPending:
				row.Pending++
			case models.StatusMaintenance:
				row.Maintenance++
			}
		}
		boundary = append(boundary, row)
	}

	got := make([]historyTotals, len(set.windows))
	for _, row := range rollups {
		for i := range set.windows {
			if set.rollupInside(i, row.BucketAt) {
				got[i].addRollup(*row)
			}
		}
	}
	// Exactly the loop History runs: every range folded into the periods it
	// serves, and nothing else.
	for _, row := range boundary {
		for i := range set.windows {
			if ranges[row.RangeIndex].serves(i) {
				got[i].addRollup(row.rollup())
			}
		}
	}

	// The reference is the raw aggregate the query used to run: every heartbeat
	// at or after the start of the window, whatever hour it falls in, reduced
	// directly into the same shape.
	want := make([]historyTotals, len(set.windows))
	for _, b := range beats {
		for i, spec := range set.windows {
			if b.at.Before(spec.start) {
				continue
			}
			if !want[i].hasMinMax {
				want[i].minMS, want[i].maxMS = b.latency, b.latency
				want[i].hasMinMax = true
			} else {
				if b.latency < want[i].minMS {
					want[i].minMS = b.latency
				}
				if b.latency > want[i].maxMS {
					want[i].maxMS = b.latency
				}
			}
			want[i].total++
			switch b.status {
			case models.StatusUp:
				want[i].up++
				want[i].upLatSum += b.latency
				want[i].upLatCnt++
			case models.StatusDown:
				want[i].down++
			case models.StatusPending:
				want[i].pending++
			}
		}
	}

	for i, spec := range set.windows {
		if got[i] != want[i] {
			t.Errorf("%d h window: split totals %+v, raw aggregate %+v", spec.hours, got[i], want[i])
		}
	}

	// The shaped row must carry those counters in the API columns.
	row := set.row(1, got)
	day := got[set.indexOf[models.DefaultUptimeWindowHours]]
	week := got[set.indexOf[7*24]]
	month := got[set.indexOf[models.HeartbeatRollupHorizonHours]]
	chosen := got[set.indexOf[336]]
	if row.Up24h != day.up || row.Total24h != day.total || row.Down24h != day.down || row.Pending24h != day.pending {
		t.Errorf("24 h counters landed in the wrong columns: %+v", row)
	}
	if row.Up7d != week.up || row.Total7d != week.total {
		t.Errorf("7 d counters landed in the wrong columns: %+v", row)
	}
	if row.Up30d != month.up || row.Total30d != month.total {
		t.Errorf("30 d counters landed in the wrong columns: %+v", row)
	}
	if row.UpWindow != chosen.up || row.TotalWindow != chosen.total {
		t.Errorf("selected window counters landed in the wrong columns: %+v", row)
	}
	if day.upLatCnt == 0 {
		t.Fatal("the fixture must contain up heartbeats in the last 24 h")
	}
	// The average is rounded to the four fractional digits MySQL's AVG returned.
	wantAvg := math.Round(float64(day.upLatSum)/float64(day.upLatCnt)*10000) / 10000
	if row.AvgMS24h == nil || *row.AvgMS24h != wantAvg {
		t.Errorf("avg_ms_24h = %v, want %v", row.AvgMS24h, wantAvg)
	}
	if row.MinMS24h == nil || row.MaxMS24h == nil {
		t.Fatal("the 24 h latency range must be reported when the window has heartbeats")
	}
	if *row.MinMS24h != day.minMS || *row.MaxMS24h != day.maxMS {
		t.Errorf("min/max = %d/%d, want %d/%d", *row.MinMS24h, *row.MaxMS24h, day.minMS, day.maxMS)
	}
}
