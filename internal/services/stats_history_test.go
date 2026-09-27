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
	if !selected.rawInside(day, time.Date(2026, 9, 25, 21, 30, 0, 0, time.UTC)) {
		t.Error("a heartbeat in the partial start hour must be counted raw")
	}
	if selected.rawInside(day, time.Date(2026, 9, 25, 22, 30, 0, 0, time.UTC)) {
		t.Error("a heartbeat in a whole hour must not be counted raw")
	}
	if !selected.rawInside(day, now) {
		t.Error("a heartbeat in the hour in progress must be counted raw")
	}
	if !selected.rawInside(day, selected.windows[day].start) {
		t.Error("a heartbeat exactly at the start of the window must be counted")
	}
	if selected.rawInside(day, time.Date(2026, 9, 25, 21, 0, 0, 0, time.UTC)) {
		t.Error("a heartbeat before the window start must not be counted")
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
// raw ranges never overlap, so the OR of conditions cannot return a heartbeat
// twice and double its counter.
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
	// Every window must have its partial start hour covered by one range.
	for _, spec := range set.windows {
		if !spec.start.Before(spec.leadEnd) {
			continue
		}
		found := false
		for _, window := range ranges {
			if window.start.Equal(spec.start) && window.end.Equal(spec.leadEnd) {
				found = true
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
// on write and completed by the raw boundary hours, must produce the counters
// and the latency figures of the single raw aggregate the query used to run.
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

	got := make([]historyTotals, len(set.windows))
	for _, row := range rollups {
		for i := range set.windows {
			if set.rollupInside(i, row.BucketAt) {
				got[i].addRollup(*row)
			}
		}
	}
	for _, b := range beats {
		raw := historyRawRow{MonitorID: 1, Status: int(b.status), LatencyMS: b.latency, CreatedAt: b.at}
		for i := range set.windows {
			if set.rawInside(i, b.at) {
				got[i].addRaw(raw)
			}
		}
	}

	// The reference is the raw aggregate: every heartbeat at or after the start
	// of the window, whatever hour it falls in.
	want := make([]historyTotals, len(set.windows))
	for _, b := range beats {
		raw := historyRawRow{MonitorID: 1, Status: int(b.status), LatencyMS: b.latency, CreatedAt: b.at}
		for i, spec := range set.windows {
			if b.at.Before(spec.start) {
				continue
			}
			want[i].addRaw(raw)
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
