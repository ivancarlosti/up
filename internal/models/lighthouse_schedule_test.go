package models

import (
	"strings"
	"testing"
	"time"
)

// scheduleFixture is the default clock of the web performance job: a cadence can
// be handed to NextRun on its own, which is how a per monitor interval resolves.
func scheduleFixture() LighthouseSchedule {
	return LighthouseSchedule{
		CheckTime:     "04:15",
		CheckTimezone: "UTC",
		Interval:      DefaultLighthouseInterval,
		Weekday:       DefaultLighthouseWeekday,
	}.Normalize()
}

// scheduleCase is one (instant, expected next run) pair of the grid tests.
type scheduleCase struct {
	after time.Time
	want  time.Time
}

// TestLighthouseIntervalNormalization covers the parser of a stored cadence: an
// unknown or absent value must fall back to the documented default instead of
// leaving the job without a schedule.
func TestLighthouseIntervalNormalization(t *testing.T) {
	for _, interval := range LighthouseIntervals {
		if NormalizeLighthouseInterval(strings.ToUpper(interval)) != interval {
			t.Fatalf("%q must normalize to itself", interval)
		}
		if !ValidLighthouseInterval(interval) {
			t.Fatalf("%q must be valid", interval)
		}
	}
	for _, invalid := range []string{"", "  ", "hour", "minutely", "24h"} {
		if NormalizeLighthouseInterval(invalid) != DefaultLighthouseInterval {
			t.Fatalf("%q must fall back to %q", invalid, DefaultLighthouseInterval)
		}
	}
	if NormalizeLighthouseInterval(" weekly ") != LighthouseIntervalWeekly {
		t.Fatal("the surrounding spaces must be ignored")
	}
	if ValidLighthouseInterval("") || ValidLighthouseInterval("everyday") {
		t.Fatal("an unknown cadence must not be valid")
	}
	if got := LighthouseIntervalDuration(LighthouseIntervalEvery6H); got != 6*time.Hour {
		t.Fatalf("6h duration = %s", got)
	}
	// An unknown cadence is measured at the default pace, whatever that pace is.
	if got := LighthouseIntervalDuration("nonsense"); got != LighthouseIntervalDuration(DefaultLighthouseInterval) {
		t.Fatalf("the fallback duration = %s, want the one of %q", got, DefaultLighthouseInterval)
	}
}

// TestLighthouseWeekdayNormalization covers the weekday parser and its mapping
// onto Go's time.Weekday (index 0 is Sunday).
func TestLighthouseWeekdayNormalization(t *testing.T) {
	for index, weekday := range LighthouseWeekdays {
		if NormalizeLighthouseWeekday(weekday) != weekday {
			t.Fatalf("%q must normalize to itself", weekday)
		}
		if !ValidLighthouseWeekday(weekday) {
			t.Fatalf("%q must be valid", weekday)
		}
		if LighthouseWeekdayIndex(weekday) != time.Weekday(index) {
			t.Fatalf("%q -> %s, want %s", weekday, LighthouseWeekdayIndex(weekday), time.Weekday(index))
		}
	}
	if NormalizeLighthouseWeekday("fri") != DefaultLighthouseWeekday {
		t.Fatal("an unknown weekday must fall back to the default")
	}
	if LighthouseWeekdayIndex("nonsense") != time.Monday {
		t.Fatal("the default weekday must be Monday")
	}
	if DefaultLighthouseWeekday != LighthouseWeekdayMonday {
		t.Fatal("the documented default weekday must be Monday")
	}
}

// TestLighthouseNextRunHourly checks the hourly grid: the cadence keeps the
// minutes of the check time and fires every hour, including the hours that
// precede the anchor of the day.
func TestLighthouseNextRunHourly(t *testing.T) {
	schedule := scheduleFixture()
	cases := []scheduleCase{
		// 01:00 is before the 04:15 anchor of the day: the next cell is 01:15.
		{time.Date(2026, 5, 10, 1, 0, 0, 0, time.UTC), time.Date(2026, 5, 10, 1, 15, 0, 0, time.UTC)},
		// On the anchor itself the current cell is not "after" it.
		{time.Date(2026, 5, 10, 4, 15, 0, 0, time.UTC), time.Date(2026, 5, 10, 5, 15, 0, 0, time.UTC)},
		{time.Date(2026, 5, 10, 4, 20, 0, 0, time.UTC), time.Date(2026, 5, 10, 5, 15, 0, 0, time.UTC)},
		// The last hour of the day rolls into the next one.
		{time.Date(2026, 5, 10, 23, 30, 0, 0, time.UTC), time.Date(2026, 5, 11, 0, 15, 0, 0, time.UTC)},
	}
	for _, tc := range cases {
		if got := schedule.NextRun(tc.after, LighthouseIntervalHourly); !got.Equal(tc.want) {
			t.Fatalf("hourly after %s = %s, want %s", tc.after, got, tc.want)
		}
	}
}

// TestLighthouseNextRunStepped covers the 6h and 12h grids: the cells repeat from
// the configured time of day, counted in real time.
func TestLighthouseNextRunStepped(t *testing.T) {
	schedule := scheduleFixture()
	cases := []struct {
		interval string
		after    time.Time
		want     time.Time
	}{
		// 6h from the 04:15 anchor: 04:15, 10:15, 16:15, 22:15 local.
		{LighthouseIntervalEvery6H, time.Date(2026, 5, 10, 1, 0, 0, 0, time.UTC), time.Date(2026, 5, 10, 4, 15, 0, 0, time.UTC)},
		{LighthouseIntervalEvery6H, time.Date(2026, 5, 10, 4, 15, 0, 0, time.UTC), time.Date(2026, 5, 10, 10, 15, 0, 0, time.UTC)},
		{LighthouseIntervalEvery6H, time.Date(2026, 5, 10, 10, 30, 0, 0, time.UTC), time.Date(2026, 5, 10, 16, 15, 0, 0, time.UTC)},
		{LighthouseIntervalEvery6H, time.Date(2026, 5, 10, 16, 15, 0, 0, time.UTC), time.Date(2026, 5, 10, 22, 15, 0, 0, time.UTC)},
		// 22:15 + 6h is 04:15 of the next day, which the grid must reach.
		{LighthouseIntervalEvery6H, time.Date(2026, 5, 10, 22, 15, 0, 0, time.UTC), time.Date(2026, 5, 11, 4, 15, 0, 0, time.UTC)},
		{LighthouseIntervalEvery6H, time.Date(2026, 5, 10, 23, 0, 0, 0, time.UTC), time.Date(2026, 5, 11, 4, 15, 0, 0, time.UTC)},
		// 12h: two cells a day, 04:15 and 16:15.
		{LighthouseIntervalEvery12H, time.Date(2026, 5, 10, 3, 0, 0, 0, time.UTC), time.Date(2026, 5, 10, 4, 15, 0, 0, time.UTC)},
		{LighthouseIntervalEvery12H, time.Date(2026, 5, 10, 5, 0, 0, 0, time.UTC), time.Date(2026, 5, 10, 16, 15, 0, 0, time.UTC)},
		{LighthouseIntervalEvery12H, time.Date(2026, 5, 10, 16, 15, 0, 0, time.UTC), time.Date(2026, 5, 11, 4, 15, 0, 0, time.UTC)},
	}
	for _, tc := range cases {
		if got := schedule.NextRun(tc.after, tc.interval); !got.Equal(tc.want) {
			t.Fatalf("%s after %s = %s, want %s", tc.interval, tc.after, got, tc.want)
		}
	}
}

// TestLighthouseNextRunDailyAndWeekly covers the two calendar cadences: a day, and
// the configured weekday at the configured time of day.
func TestLighthouseNextRunDailyAndWeekly(t *testing.T) {
	schedule := scheduleFixture()
	// 2026-05-10 is a Sunday, 2026-05-11 a Monday.
	if got := schedule.NextRun(time.Date(2026, 5, 10, 1, 0, 0, 0, time.UTC), LighthouseIntervalDaily); !got.Equal(time.Date(2026, 5, 10, 4, 15, 0, 0, time.UTC)) {
		t.Fatalf("daily = %s", got)
	}
	if got := schedule.NextRun(time.Date(2026, 5, 10, 5, 0, 0, 0, time.UTC), LighthouseIntervalDaily); !got.Equal(time.Date(2026, 5, 11, 4, 15, 0, 0, time.UTC)) {
		t.Fatalf("daily (after the instant) = %s", got)
	}

	// Weekly on Monday (the default): from Sunday the run is tomorrow.
	if got := schedule.NextRun(time.Date(2026, 5, 10, 5, 0, 0, 0, time.UTC), LighthouseIntervalWeekly); !got.Equal(time.Date(2026, 5, 11, 4, 15, 0, 0, time.UTC)) {
		t.Fatalf("weekly from Sunday = %s", got)
	}
	// On the day itself, before the instant: today.
	if got := schedule.NextRun(time.Date(2026, 5, 11, 1, 0, 0, 0, time.UTC), LighthouseIntervalWeekly); !got.Equal(time.Date(2026, 5, 11, 4, 15, 0, 0, time.UTC)) {
		t.Fatalf("weekly on Monday = %s", got)
	}
	// On the instant itself, or after it: the same day next week.
	if got := schedule.NextRun(time.Date(2026, 5, 11, 4, 15, 0, 0, time.UTC), LighthouseIntervalWeekly); !got.Equal(time.Date(2026, 5, 18, 4, 15, 0, 0, time.UTC)) {
		t.Fatalf("weekly (after the instant) = %s", got)
	}
	// Another weekday moves the anchor: Saturday.
	weekly := schedule
	weekly.Weekday = LighthouseWeekdaySaturday
	if got := weekly.NextRun(time.Date(2026, 5, 11, 1, 0, 0, 0, time.UTC), LighthouseIntervalWeekly); !got.Equal(time.Date(2026, 5, 16, 4, 15, 0, 0, time.UTC)) {
		t.Fatalf("weekly on Saturday = %s", got)
	}
}

// TestLighthouseNextRunTimezone checks that the configured timezone anchors the
// grid: 04:15 in Sao Paulo is 07:15 UTC.
func TestLighthouseNextRunTimezone(t *testing.T) {
	schedule := LighthouseSchedule{
		CheckTime:     "04:15",
		CheckTimezone: "America/Sao_Paulo",
		Interval:      LighthouseIntervalDaily,
	}.Normalize()
	// 12:00 UTC is 09:00 local: today's run has passed, the next one is tomorrow.
	after := time.Date(2026, 5, 10, 12, 0, 0, 0, time.UTC)
	if got := schedule.NextRun(after, LighthouseIntervalDaily); !got.Equal(time.Date(2026, 5, 11, 7, 15, 0, 0, time.UTC)) {
		t.Fatalf("daily in Sao Paulo = %s", got.UTC())
	}
	// The hourly grid keeps the local minutes: 04:15 local is 07:15 UTC.
	if got := schedule.NextRun(after, LighthouseIntervalHourly); !got.Equal(time.Date(2026, 5, 10, 12, 15, 0, 0, time.UTC)) {
		t.Fatalf("hourly in Sao Paulo = %s", got.UTC())
	}
}

// TestLighthouseNextRunDST checks that a sub-daily grid survives a DST change: the
// anchor is recomputed from the local day of the instant it is asked about, so the
// grid keeps firing at the same local minute.
func TestLighthouseNextRunDST(t *testing.T) {
	schedule := LighthouseSchedule{
		CheckTime:     "04:00",
		CheckTimezone: "America/New_York",
		Interval:      LighthouseIntervalEvery6H,
	}.Normalize()
	// 2026-03-08 is the spring forward in New York (02:00 -> 03:00 local): the 6h
	// grid anchored at 04:00 local keeps its cells (04:00, 10:00, 16:00, 22:00).
	after := time.Date(2026, 3, 8, 10, 30, 0, 0, time.UTC) // 06:30 local (EDT)
	next := schedule.NextRun(after, LighthouseIntervalEvery6H).In(time.FixedZone("EDT", -4*3600))
	if next.Hour() != 10 || next.Minute() != 0 {
		t.Fatalf("next run = %s local, want 10:00 local", next.Format("2006-01-02 15:04"))
	}
}

// TestLighthouseRunDue covers the decision the ticker makes: a monitor that never
// ran is due, one whose interval has not elapsed is not.
func TestLighthouseRunDue(t *testing.T) {
	schedule := scheduleFixture()
	now := time.Date(2026, 5, 10, 10, 30, 0, 0, time.UTC)
	if !schedule.RunDue(now, time.Time{}, LighthouseIntervalDaily) {
		t.Fatal("a monitor that never ran must be due")
	}
	// Ran at 10:15: the next hourly cell is 11:15, still in the future.
	if schedule.RunDue(now, time.Date(2026, 5, 10, 10, 15, 0, 0, time.UTC), LighthouseIntervalHourly) {
		t.Fatal("an hourly monitor measured 15 minutes ago must not be due")
	}
	// Ran more than an hour ago: due.
	if !schedule.RunDue(now, time.Date(2026, 5, 10, 9, 15, 0, 0, time.UTC), LighthouseIntervalHourly) {
		t.Fatal("an hourly monitor measured 75 minutes ago must be due")
	}
	// A weekly monitor measured six days ago is still inside its week...
	sixDays := now.AddDate(0, 0, -6)
	if schedule.RunDue(now, sixDays, LighthouseIntervalWeekly) {
		t.Fatal("a weekly monitor measured six days ago must not be due")
	}
	// ... and one measured eight days ago is overdue.
	eightDays := now.AddDate(0, 0, -8)
	if !schedule.RunDue(now, eightDays, LighthouseIntervalWeekly) {
		t.Fatal("a weekly monitor measured eight days ago must be due")
	}
	// A cadence change takes effect immediately: the same last run that is inside
	// a week is overdue on the daily grid.
	if !schedule.RunDue(now, sixDays, LighthouseIntervalDaily) {
		t.Fatal("a daily monitor measured six days ago must be due")
	}
}

// TestLighthouseSlot checks the cluster lock key: one value per UTC hour, which is
// what lets a single node of a cluster own a slot.
func TestLighthouseSlot(t *testing.T) {
	first := time.Date(2026, 5, 10, 10, 0, 0, 0, time.UTC)
	last := first.Add(59*time.Minute + 59*time.Second)
	if LighthouseSlot(first) != LighthouseSlot(last) {
		t.Fatal("the same hour must be the same slot")
	}
	if LighthouseSlot(first) == LighthouseSlot(first.Add(time.Hour)) {
		t.Fatal("the next hour must be another slot")
	}
	// The slot is computed in UTC, so two nodes in different zones agree.
	offset := first.In(time.FixedZone("+05", 5*3600))
	if LighthouseSlot(offset) != LighthouseSlot(first) {
		t.Fatal("the slot must not depend on the zone of the node")
	}
}

// TestLighthouseSettingsSchedule checks the projection of the job settings onto the
// clock, including the fallbacks of a payload written before the fields existed.
func TestLighthouseSettingsSchedule(t *testing.T) {
	schedule := DefaultLighthouseSettings().Schedule()
	if schedule.Interval != DefaultLighthouseInterval {
		t.Fatalf("default interval = %q", schedule.Interval)
	}
	if schedule.Weekday != DefaultLighthouseWeekday {
		t.Fatalf("default weekday = %q", schedule.Weekday)
	}
	empty := LighthouseSettings{CheckTime: "04:00", CheckTimezone: "UTC"}.Schedule()
	if empty.Interval != DefaultLighthouseInterval || empty.Weekday != DefaultLighthouseWeekday {
		t.Fatalf("an empty cadence must fall back to the defaults: %+v", empty)
	}

	settings := DefaultLighthouseSettings()
	settings.Interval = ""
	settings.Weekday = ""
	if problem := settings.Validate(); problem != "" {
		t.Fatalf("an omitted cadence must be valid: %s", problem)
	}
	settings.Interval = "minutely"
	if problem := settings.Validate(); problem == "" {
		t.Fatal("an unknown cadence must be rejected")
	}
	settings.Interval = DefaultLighthouseInterval
	settings.Weekday = "someday"
	if problem := settings.Validate(); problem == "" {
		t.Fatal("an unknown weekday must be rejected")
	}
	settings.Weekday = DefaultLighthouseWeekday
	if problem := settings.Validate(); problem != "" {
		t.Fatalf("the defaults must be valid: %s", problem)
	}
}
