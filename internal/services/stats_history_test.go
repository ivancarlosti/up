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
	// The widest period is the 30 day one, and its aggregate starts at the first
	// whole hour inside it: the hour it starts in is read from the raw table.
	if got, want := selected.windows[selected.indexOf[models.HeartbeatRollupHorizonHours]].leadEnd,
		time.Date(2026, 8, 27, 22, 0, 0, 0, time.UTC); !got.Equal(want) {
		t.Errorf("30 d leadEnd = %s, want %s", got, want)
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
	horizon := func(set historyWindowSet) time.Time {
		return set.windows[set.indexOf[models.HeartbeatRollupHorizonHours]].leadEnd
	}
	if got, want := horizon(fourteen), horizon(selected); !got.Equal(want) {
		t.Errorf("336 h selected: the 30 d period starts at %s, want %s", got, want)
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

// TestListingWindowSetKeepsThePeriodsItReports pins the property
// newListingWindowSet relies on: dropping the 7 day and 30 day periods must not
// move the periods that stay, or the dashboard would show a 24 h percentage
// computed over another range than the one the detail page shows.
func TestListingWindowSetKeepsThePeriodsItReports(t *testing.T) {
	now := time.Date(2026, 10, 30, 9, 41, 12, 0, time.UTC)
	for _, selected := range []int{
		models.DefaultUptimeWindowHours, 7 * 24, 336, models.HeartbeatRollupHorizonHours,
	} {
		full := newHistoryWindowSet(now, selected)
		listing := newListingWindowSet(now, selected)

		// The selected window and the fixed 24 h one, deduplicated.
		want := 2
		if selected == models.DefaultUptimeWindowHours {
			want = 1
		}
		if len(listing.windows) != want {
			t.Fatalf("%d h selected: %d periods in the listing set, want %d", selected, len(listing.windows), want)
		}
		for _, hours := range []int{models.DefaultUptimeWindowHours, selected} {
			if _, ok := listing.indexOf[hours]; !ok {
				t.Fatalf("%d h selected: the listing set dropped the %d h period", selected, hours)
			}
			kept := listing.windows[listing.indexOf[hours]]
			reference := full.windows[full.indexOf[hours]]
			if kept.hours != reference.hours || !kept.start.Equal(reference.start) || !kept.leadEnd.Equal(reference.leadEnd) {
				t.Errorf("%d h selected: the %d h period moved: %+v, want %+v", selected, hours, kept, reference)
			}
		}
		if _, ok := listing.indexOf[7*24]; ok && selected != 7*24 {
			t.Errorf("%d h selected: the listing set kept the 7 d period", selected)
		}
		if _, ok := listing.indexOf[models.HeartbeatRollupHorizonHours]; ok && selected != models.HeartbeatRollupHorizonHours {
			t.Errorf("%d h selected: the listing set kept the 30 d period", selected)
		}
		// Every aggregate the listing runs starts at the first whole hour inside its
		// period, and never before the oldest period it reports: with the 24 h window
		// selected that is one day of rollups, not the 30 day horizon.
		oldest := listing.windows[listing.indexOf[models.DefaultUptimeWindowHours]].leadEnd
		for _, spec := range listing.windows {
			if spec.leadEnd.Before(oldest) {
				oldest = spec.leadEnd
			}
		}
		for _, spec := range listing.windows {
			if spec.leadEnd.Before(oldest) || spec.leadEnd.After(listing.endHour) {
				t.Errorf("%d h selected: the %d h aggregate reads [%s, %s)",
					selected, spec.hours, spec.leadEnd, listing.endHour)
			}
		}
		if selected == models.DefaultUptimeWindowHours && !oldest.Equal(
			full.windows[full.indexOf[models.DefaultUptimeWindowHours]].leadEnd) {
			t.Errorf("24 h selected: the listing reads from %s, want one day of rollups (%s)",
				oldest, full.windows[full.indexOf[models.DefaultUptimeWindowHours]].leadEnd)
		}
		if !listing.endHour.Equal(full.endHour) {
			t.Errorf("%d h selected: endHour = %s, want %s", selected, listing.endHour, full.endHour)
		}
	}
}

// TestListingRowReportsNoLongWindows pins what a narrowed listing reports: the
// columns of the periods it computed, and zeros - not the 24 h counters, which is
// what an index lookup missing from the map would have returned before totalsOf -
// for the 7 day and 30 day percentages. A listing whose selected window *is* the
// 7 day one keeps reporting it, because it is the same period.
func TestListingRowReportsNoLongWindows(t *testing.T) {
	now := time.Date(2026, 10, 30, 9, 41, 12, 0, time.UTC)
	listing := newListingWindowSet(now, 336)
	day := historyTotals{up: 10, down: 1, pending: 2, total: 13, upLatSum: 130, upLatCnt: 10}
	day.observe(5, 40)
	fortnight := historyTotals{up: 90, down: 3, total: 100, upLatSum: 900, upLatCnt: 90}
	fortnight.observe(4, 41)

	row := listing.row(7, []historyTotals{day, fortnight})
	if row.MonitorID != 7 || row.Up24h != 10 || row.Total24h != 13 || row.Down24h != 1 || row.Pending24h != 2 {
		t.Errorf("24 h columns = %+v, want the day counters", row)
	}
	if row.UpWindow != 90 || row.TotalWindow != 100 {
		t.Errorf("selected window columns = %d/%d, want 90/100", row.UpWindow, row.TotalWindow)
	}
	if row.Up7d != 0 || row.Total7d != 0 || row.Up30d != 0 || row.Total30d != 0 {
		t.Errorf("7 d/30 d columns = %d/%d and %d/%d, want zeros",
			row.Up7d, row.Total7d, row.Up30d, row.Total30d)
	}
	if row.AvgMS24h == nil || *row.AvgMS24h != 13 || row.MinMS24h == nil || *row.MinMS24h != 5 ||
		row.MaxMS24h == nil || *row.MaxMS24h != 40 {
		t.Errorf("24 h latency = %v/%v/%v, want 13/5/40", row.AvgMS24h, row.MinMS24h, row.MaxMS24h)
	}

	// A seven day selection is the 7 d column: it must be reported, not zeroed.
	week := newListingWindowSet(now, 7*24).row(8, []historyTotals{
		{up: 10, total: 13},
		{up: 90, total: 100},
	})
	if week.Up7d != 90 || week.Total7d != 100 || week.UpWindow != 90 || week.TotalWindow != 100 {
		t.Errorf("7 d selection: columns = %d/%d (window %d/%d), want 90/100",
			week.Up7d, week.Total7d, week.UpWindow, week.TotalWindow)
	}
	if week.Up30d != 0 || week.Total30d != 0 {
		t.Errorf("7 d selection: 30 d columns = %d/%d, want zeros", week.Up30d, week.Total30d)
	}
}

// TestCountedPeriods pins the rule that decides whether a monitor is reported at
// all: a monitor whose periods hold no heartbeat has nothing to show.
func TestCountedPeriods(t *testing.T) {
	if counted(nil) {
		t.Error("no periods at all must not count as data")
	}
	if counted([]historyTotals{{}, {}}) {
		t.Error("periods without a heartbeat must not count as data")
	}
	if !counted([]historyTotals{{}, {down: 2, total: 2}}) {
		t.Error("a failed heartbeat is data: the period must count")
	}
}
