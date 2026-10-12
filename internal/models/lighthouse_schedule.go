package models

import (
	"strings"
	"time"
)

// ---------------------------------------------------------------------------
// Web performance schedule (the clock of the Lighthouse job)
// ---------------------------------------------------------------------------
//
// The job started as a daily affair: a time of day, a timezone and a day bucket
// in the settings table. The cadence is now chosen per monitor (hourly, every 6
// or 12 hours, daily, or weekly on a weekday the operator picks), which needs a
// clock that is a pure function of its inputs: given the instant of the last
// run, when is the next one due?
//
// Answering from the last run — instead of keeping a counter somewhere — is what
// makes the cadence survive a restart, a cluster of nodes and a monitor edited
// on another node: the only state is `monitor_lighthouses.ran_at`, written by
// whoever performed the measurement, and the job is restart proof by
// construction (a process that was down during a slot measures as soon as it
// comes back, exactly like the daily job used to).

// Lighthouse intervals ------------------------------------------------------

// The cadences a monitor (or the job, as the fallback) can measure a page with.
// They are a closed set on purpose: a discrete choice is validated the same way
// everywhere (API, UI, stored configuration) and reads the same in every locale.
const (
	LighthouseIntervalHourly   = "hourly"
	LighthouseIntervalEvery6H  = "6h"
	LighthouseIntervalEvery12H = "12h"
	LighthouseIntervalDaily    = "daily"
	LighthouseIntervalWeekly   = "weekly"
	// DefaultLighthouseInterval is the cadence of a monitor that does not pick
	// one: the daily measurement the feature shipped with.
	DefaultLighthouseInterval = LighthouseIntervalDaily
)

// LighthouseIntervals lists every accepted cadence (validation + API + UI).
var LighthouseIntervals = []string{
	LighthouseIntervalHourly,
	LighthouseIntervalEvery6H,
	LighthouseIntervalEvery12H,
	LighthouseIntervalDaily,
	LighthouseIntervalWeekly,
}

// NormalizeLighthouseInterval lower cases the input and falls back to the
// documented default, so a stored monitor (or an old client payload) without a
// cadence still describes a schedule.
func NormalizeLighthouseInterval(raw string) string {
	value := strings.ToLower(strings.TrimSpace(raw))
	for _, candidate := range LighthouseIntervals {
		if value == candidate {
			return candidate
		}
	}
	return DefaultLighthouseInterval
}

// ValidLighthouseInterval reports whether the raw value names a cadence.
func ValidLighthouseInterval(raw string) bool {
	value := strings.ToLower(strings.TrimSpace(raw))
	for _, candidate := range LighthouseIntervals {
		if value == candidate {
			return true
		}
	}
	return false
}

// LighthouseIntervalDuration is how much real time separates two runs of a
// cadence. A day and a week are a fixed number of hours here because the
// stepping is done in absolute time (see nextSteppedRun); the daily cadence is
// still computed in wall clock time by NextDailyRun.
func LighthouseIntervalDuration(interval string) time.Duration {
	switch NormalizeLighthouseInterval(interval) {
	case LighthouseIntervalHourly:
		return time.Hour
	case LighthouseIntervalEvery6H:
		return 6 * time.Hour
	case LighthouseIntervalEvery12H:
		return 12 * time.Hour
	case LighthouseIntervalWeekly:
		return 7 * 24 * time.Hour
	}
	return 24 * time.Hour
}

// Lighthouse weekdays -------------------------------------------------------

// The days a weekly cadence can be anchored to.
const (
	LighthouseWeekdaySunday    = "sunday"
	LighthouseWeekdayMonday    = "monday"
	LighthouseWeekdayTuesday   = "tuesday"
	LighthouseWeekdayWednesday = "wednesday"
	LighthouseWeekdayThursday  = "thursday"
	LighthouseWeekdayFriday    = "friday"
	LighthouseWeekdaySaturday  = "saturday"
	// DefaultLighthouseWeekday is the day a weekly job runs on when the
	// operator does not pick one: the first working day of the week, so a
	// weekend is measured before the week starts.
	DefaultLighthouseWeekday = LighthouseWeekdayMonday
)

// LighthouseWeekdays lists every accepted weekday, in the order of Go's
// time.Weekday (index 0 is Sunday): LighthouseWeekdayIndex relies on it.
var LighthouseWeekdays = []string{
	LighthouseWeekdaySunday,
	LighthouseWeekdayMonday,
	LighthouseWeekdayTuesday,
	LighthouseWeekdayWednesday,
	LighthouseWeekdayThursday,
	LighthouseWeekdayFriday,
	LighthouseWeekdaySaturday,
}

// NormalizeLighthouseWeekday lower cases the input and falls back to the
// documented default.
func NormalizeLighthouseWeekday(raw string) string {
	value := strings.ToLower(strings.TrimSpace(raw))
	for _, candidate := range LighthouseWeekdays {
		if value == candidate {
			return candidate
		}
	}
	return DefaultLighthouseWeekday
}

// ValidLighthouseWeekday reports whether the raw value names a weekday.
func ValidLighthouseWeekday(raw string) bool {
	value := strings.ToLower(strings.TrimSpace(raw))
	for _, candidate := range LighthouseWeekdays {
		if value == candidate {
			return true
		}
	}
	return false
}

// LighthouseWeekdayIndex maps a stored weekday onto time.Weekday, falling back
// to the default day when the value is unknown.
func LighthouseWeekdayIndex(raw string) time.Weekday {
	value := strings.ToLower(strings.TrimSpace(raw))
	for index, candidate := range LighthouseWeekdays {
		if value == candidate {
			return time.Weekday(index)
		}
	}
	for index, candidate := range LighthouseWeekdays {
		if candidate == DefaultLighthouseWeekday {
			return time.Weekday(index)
		}
	}
	return time.Monday
}

// Schedule ------------------------------------------------------------------

// LighthouseSchedule is the clock of one monitor (or of the job, as the fallback
// of a monitor that carries no cadence of its own).
//
// Every method is pure: the same instant always gives the same answer, which is
// what lets the admin page, the ticker and the cluster lock share one definition
// of "due" without sharing any state.
type LighthouseSchedule struct {
	// CheckTime is "HH:MM" (24h) in CheckTimezone: the time of day the runs are
	// anchored to.
	CheckTime string
	// CheckTimezone is an IANA name ("UTC", "America/Sao_Paulo").
	CheckTimezone string
	// Interval is the cadence (one of LighthouseIntervals).
	Interval string
	// Weekday anchors the weekly cadence (one of LighthouseWeekdays).
	Weekday string
}

// Normalize fills in the documented defaults.
func (s LighthouseSchedule) Normalize() LighthouseSchedule {
	if _, _, err := ParseCheckTime(s.CheckTime); err != nil {
		s.CheckTime = DefaultLighthouseCheckTime
	}
	s.CheckTimezone = strings.TrimSpace(s.CheckTimezone)
	if _, err := time.LoadLocation(s.CheckTimezone); err != nil {
		s.CheckTimezone = DefaultLighthouseTimezone
	}
	s.Interval = NormalizeLighthouseInterval(s.Interval)
	s.Weekday = NormalizeLighthouseWeekday(s.Weekday)
	return s
}

// NextRun returns the first instant `interval` fires strictly after `after`,
// anchored at the configured time of day in the configured timezone.
//
// The anchor is recomputed from the calendar day of `after`, so a daily run
// happens at 04:00 local even across a DST change, a weekly run lands on the
// configured weekday and a sub-daily grid (every hour, 6 or 12 hours) is counted
// from that time of day in real time.
func (s LighthouseSchedule) NextRun(after time.Time, interval string) time.Time {
	schedule := s.Normalize()
	switch NormalizeLighthouseInterval(interval) {
	case LighthouseIntervalWeekly:
		return nextWeeklyRun(after, schedule.Weekday, schedule.CheckTime, schedule.CheckTimezone)
	case LighthouseIntervalHourly, LighthouseIntervalEvery6H, LighthouseIntervalEvery12H:
		return nextSteppedRun(after, LighthouseIntervalDuration(interval), schedule.CheckTime, schedule.CheckTimezone)
	}
	return NextDailyRun(after, schedule.CheckTime, schedule.CheckTimezone)
}

// RunDue reports whether a monitor whose last run happened at `last` is due at
// `now`. A monitor that never ran is always due: that is how a freshly enabled
// monitor (or one whose whole history was purged) gets its first measurement.
func (s LighthouseSchedule) RunDue(now, last time.Time, interval string) bool {
	if last.IsZero() {
		return true
	}
	return !s.NextRun(last, interval).After(now)
}

// LighthouseSlot identifies the cadence slot an instant belongs to (its UTC
// hour).
//
// It is the key of the cluster lock: every node of the cluster computes the same
// value at the same moment, so only one of them performs the measurements of
// that slot and the PageSpeed Insights quota is spent once, not once per node.
func LighthouseSlot(now time.Time) int64 { return now.UTC().Unix() / 3600 }

// nextSteppedRun returns the first cell of a grid whose cells are `width` apart
// and whose origin is the configured time of day, strictly after `after`.
//
// The origin is the instant of the check time on the calendar day of `after`
// (in the configured timezone), so 04:00 with a 6 hour cadence gives 04:00,
// 10:00, 16:00 and 22:00 local, and the same grid the next day. Stepping is done
// in real time (not in wall clock arithmetic) and the origin is recomputed on
// every call: a grid shorter than a day stays honest across a DST change, and it
// re-anchors itself on the first run that follows one.
func nextSteppedRun(after time.Time, width time.Duration, checkTime, timezone string) time.Time {
	anchor := RunInstant(after, checkTime, timezone)
	if width <= 0 {
		return anchor
	}
	// Integer division truncates towards zero: the candidate lands at or before
	// `after` (one whole cell before it when `after` sits earlier in the day
	// than the anchor), and the loop walks to the first cell strictly after it.
	steps := (after.UnixNano() - anchor.UnixNano()) / int64(width)
	candidate := anchor.Add(time.Duration(steps) * width)
	for !candidate.After(after) {
		candidate = candidate.Add(width)
	}
	return candidate
}

// nextWeeklyRun returns the instant of the configured weekday at the configured
// time of day, strictly after `after`.
func nextWeeklyRun(after time.Time, weekday, checkTime, timezone string) time.Time {
	target := LighthouseWeekdayIndex(weekday)
	// Today at the check time, then one day at a time: the loop runs at most a
	// whole week and always finds the day it is looking for.
	candidate := RunInstant(after, checkTime, timezone)
	for day := 0; day < 8; day++ {
		if candidate.Weekday() == target && candidate.After(after) {
			return candidate
		}
		candidate = RunInstant(candidate.AddDate(0, 0, 1), checkTime, timezone)
	}
	return candidate
}
