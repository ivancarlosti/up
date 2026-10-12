package models

import (
	"fmt"
	"strings"
	"time"
)

// ---------------------------------------------------------------------------
// Lighthouse web performance (Google PageSpeed Insights)
// ---------------------------------------------------------------------------
//
// Up scores the page of an HTTP/Keyword monitor through the PageSpeed Insights
// API and keeps the answer twice: the last observation of each monitor (so the
// lists and the monitor detail can show it without reading a history) and one
// row per run (so the admin page can chart the trend). The two tables carry the
// same measurements on purpose: what the list shows and what the chart plots
// can never disagree.

// Lighthouse strategies -----------------------------------------------------

// The strategies an operator can pick: a mobile run, a desktop run, or both of
// them.
const (
	LighthouseStrategyMobile  = "mobile"
	LighthouseStrategyDesktop = "desktop"
	// LighthouseStrategyBoth is not a run PageSpeed Insights accepts: it means
	// "the two of them", so a monitor that picks it measures its page twice and
	// keeps one row per run (see LighthouseStrategyTargets).
	LighthouseStrategyBoth = "both"
	// DefaultLighthouseStrategy is what a monitor measures when it does not
	// say: the mobile score is the harder one and the one Lighthouse reports by
	// default.
	DefaultLighthouseStrategy = LighthouseStrategyMobile
)

// LighthouseStrategies lists every strategy an operator can pick (validation and
// the pickers of the UI).
var LighthouseStrategies = []string{LighthouseStrategyMobile, LighthouseStrategyDesktop, LighthouseStrategyBoth}

// LighthouseRuns lists the runs PageSpeed Insights really accepts, in the order
// they are performed for LighthouseStrategyBoth.
var LighthouseRuns = []string{LighthouseStrategyMobile, LighthouseStrategyDesktop}

// NormalizeLighthouseStrategy lower cases the input and falls back to the
// documented default, so a stored monitor (or an old client payload) without a
// strategy still describes a run.
func NormalizeLighthouseStrategy(raw string) string {
	value := strings.ToLower(strings.TrimSpace(raw))
	for _, strategy := range LighthouseStrategies {
		if value == strategy {
			return strategy
		}
	}
	return DefaultLighthouseStrategy
}

// ValidLighthouseStrategy reports whether the raw value names a strategy.
func ValidLighthouseStrategy(raw string) bool {
	for _, strategy := range LighthouseStrategies {
		if raw == strategy {
			return true
		}
	}
	return false
}

// LighthouseStrategyTargets expands a strategy into the runs it stands for: the
// run itself for mobile or desktop, both of them (in a stable order, so the
// daily job and a manual run plan exactly the same work) for "both".
func LighthouseStrategyTargets(strategy string) []string {
	if NormalizeLighthouseStrategy(strategy) == LighthouseStrategyBoth {
		return append([]string{}, LighthouseRuns...)
	}
	return []string{NormalizeLighthouseStrategy(strategy)}
}

// Lighthouse statuses -------------------------------------------------------

// The two outcomes of a run: a scaffold with the scores, or a failure with the
// message the API returned. The status is part of the persisted schema.
const (
	LighthouseStatusOK    = "ok"
	LighthouseStatusError = "error"
)

// Lighthouse settings -------------------------------------------------------

const (
	// DefaultLighthouseCheckTime is the time of day the daily job runs, in
	// DefaultLighthouseTimezone. It sits after the expiry job (03:00) so the two
	// outbound jobs of a node never start together.
	DefaultLighthouseCheckTime = "04:00"
	// DefaultLighthouseTimezone is the timezone of DefaultLighthouseCheckTime.
	DefaultLighthouseTimezone = "UTC"
	// DefaultLighthouseRateLimitMS is the minimum delay between two PageSpeed
	// Insights calls. The anonymous quota (no API key) is harsh and a Lighthouse
	// run costs the API tens of seconds of real work: one call a minute is
	// polite, and an operator with a key lowers it on the settings page.
	DefaultLighthouseRateLimitMS = 60000
	// DefaultLighthouseTimeoutSeconds bounds a single API call. A throttled
	// mobile run regularly needs 30 to 45 seconds.
	DefaultLighthouseTimeoutSeconds = 90
)

// LighthouseSettings is the admin managed configuration of the daily web
// performance job: when it runs, how it paces itself and which key it uses.
type LighthouseSettings struct {
	// Enabled switches the whole job off without touching the monitors.
	Enabled bool `json:"enabled"`
	// CheckTime is "HH:MM" (24h) in CheckTimezone.
	CheckTime string `json:"check_time"`
	// CheckTimezone is an IANA name ("UTC", "America/Sao_Paulo").
	CheckTimezone string `json:"check_timezone"`
	// Strategy is the strategy used when a monitor does not carry its own.
	Strategy string `json:"strategy"`
	// Interval is the cadence of a monitor that does not carry its own: one of
	// LighthouseIntervals (hourly, 6h, 12h, daily, weekly).
	Interval string `json:"interval"`
	// Weekday anchors the weekly cadence (one of LighthouseWeekdays). It is
	// ignored by every other interval, exactly like the check time of a job that
	// never runs.
	Weekday string `json:"weekday"`
	// RateLimitMS is the minimum delay between two API calls (0 = no delay).
	RateLimitMS int `json:"rate_limit_ms"`
	// TimeoutSeconds bounds a single API call.
	TimeoutSeconds int `json:"timeout_seconds"`
	// APIKey is the Google API key sent as the `key` parameter. It is empty on
	// the anonymous quota, which is what the environment variable seeds.
	APIKey string `json:"api_key"`
}

// DefaultLighthouseSettings returns the configuration used when nothing is
// stored.
func DefaultLighthouseSettings() LighthouseSettings {
	return LighthouseSettings{
		Enabled:        true,
		CheckTime:      DefaultLighthouseCheckTime,
		CheckTimezone:  DefaultLighthouseTimezone,
		Strategy:       DefaultLighthouseStrategy,
		Interval:       DefaultLighthouseInterval,
		Weekday:        DefaultLighthouseWeekday,
		RateLimitMS:    DefaultLighthouseRateLimitMS,
		TimeoutSeconds: DefaultLighthouseTimeoutSeconds,
	}
}

// Normalize applies the defaults and clamps the bounds of the settings.
func (l *LighthouseSettings) Normalize() {
	if _, _, err := ParseCheckTime(l.CheckTime); err != nil {
		l.CheckTime = DefaultLighthouseCheckTime
	}
	l.CheckTimezone = strings.TrimSpace(l.CheckTimezone)
	if _, err := time.LoadLocation(l.CheckTimezone); err != nil {
		l.CheckTimezone = DefaultLighthouseTimezone
	}
	l.Strategy = NormalizeLighthouseStrategy(l.Strategy)
	l.Interval = NormalizeLighthouseInterval(l.Interval)
	l.Weekday = NormalizeLighthouseWeekday(l.Weekday)
	if l.RateLimitMS < 0 {
		l.RateLimitMS = 0
	}
	if l.RateLimitMS > 3600000 {
		l.RateLimitMS = 3600000
	}
	if l.TimeoutSeconds < 5 {
		l.TimeoutSeconds = DefaultLighthouseTimeoutSeconds
	}
	if l.TimeoutSeconds > 300 {
		l.TimeoutSeconds = 300
	}
	l.APIKey = strings.TrimSpace(l.APIKey)
}

// Validate checks the operator input (an empty string means "valid").
//
// The cadence and the weekday are the exception to that rule: they are optional
// on the wire (an older client that only moves the clock), so an empty value is
// accepted and Normalize falls back to the documented default.
func (l *LighthouseSettings) Validate() string {
	if _, _, err := ParseCheckTime(l.CheckTime); err != nil {
		return "check_time must be a time of day like 04:00"
	}
	if _, err := time.LoadLocation(strings.TrimSpace(l.CheckTimezone)); err != nil {
		return fmt.Sprintf("check_timezone %q is not a known IANA timezone", l.CheckTimezone)
	}
	if !ValidLighthouseStrategy(strings.ToLower(strings.TrimSpace(l.Strategy))) {
		return fmt.Sprintf("strategy must be one of %s", strings.Join(LighthouseStrategies, ", "))
	}
	if raw := strings.TrimSpace(l.Interval); raw != "" && !ValidLighthouseInterval(raw) {
		return fmt.Sprintf("interval must be one of %s", strings.Join(LighthouseIntervals, ", "))
	}
	if raw := strings.TrimSpace(l.Weekday); raw != "" && !ValidLighthouseWeekday(raw) {
		return fmt.Sprintf("weekday must be one of %s", strings.Join(LighthouseWeekdays, ", "))
	}
	if l.RateLimitMS < 0 || l.RateLimitMS > 3600000 {
		return "rate_limit_ms must be between 0 and 3600000"
	}
	if l.TimeoutSeconds < 5 || l.TimeoutSeconds > 300 {
		return "timeout_seconds must be between 5 and 300"
	}
	return ""
}

// Schedule projects the job configuration onto the clock of the job: the time of
// day, the timezone and the fallback cadence of a monitor that carries none.
func (l LighthouseSettings) Schedule() LighthouseSchedule {
	return LighthouseSchedule{
		CheckTime:     l.CheckTime,
		CheckTimezone: l.CheckTimezone,
		Interval:      l.Interval,
		Weekday:       l.Weekday,
	}.Normalize()
}

// Lighthouse measurements ---------------------------------------------------

// LighthouseMeasurements is what one run measured: the four category scores and
// the Core Web Vitals.
//
// A score is a pointer because the API omits a category now and then (a page
// that cannot be audited for SEO still has a performance score): "not measured"
// must stay distinguishable from a zero. The vitals keep their native unit
// (milliseconds, and a unitless layout shift).
type LighthouseMeasurements struct {
	Performance   *int `json:"performance"`
	Accessibility *int `json:"accessibility"`
	BestPractices *int `json:"best_practices"`
	SEO           *int `json:"seo"`

	FirstContentfulPaintMS   float64 `json:"first_contentful_paint_ms"`
	LargestContentfulPaintMS float64 `json:"largest_contentful_paint_ms"`
	TotalBlockingTimeMS      float64 `json:"total_blocking_time_ms"`
	CumulativeLayoutShift    float64 `json:"cumulative_layout_shift"`
	SpeedIndexMS             float64 `json:"speed_index_ms"`
}

// LighthouseResult is one observation of a page: the measurements plus how and
// when they were read.
type LighthouseResult struct {
	// Status is LighthouseStatusOK or LighthouseStatusError. A failed run
	// carries Message and no measurement.
	Status  string `json:"status"`
	Message string `json:"message,omitempty"`
	// Strategy is the run that produced the scores (mobile or desktop).
	Strategy string `json:"strategy"`
	// FinalURL is the URL Lighthouse actually audited after the redirects.
	FinalURL string `json:"final_url,omitempty"`
	// RanAt is when Up stored the observation, FetchTime when Lighthouse
	// rendered the page.
	RanAt     time.Time  `json:"ran_at"`
	FetchTime *time.Time `json:"fetch_time,omitempty"`
	// RanByNode is the node that called the API.
	RanByNode string `json:"ran_by_node,omitempty"`
	// Interval is the cadence this observation was measured with and NextRun is
	// when the next measurement of the monitor is due. Both are COMPUTED for the
	// listing (the job clock and the instant of the last run are enough to derive
	// them): nothing is stored, so there is no second source of truth to keep in
	// sync with the monitor configuration.
	Interval string     `json:"interval,omitempty"`
	NextRun  *time.Time `json:"next_run,omitempty"`

	LighthouseMeasurements
}

// Current observation ------------------------------------------------------

// MonitorLighthouse is the last Lighthouse observation of a monitor for ONE run.
//
// One row per (monitor, strategy), like monitor_certificates holds one row per
// monitor: the table is the decoration source of the listings, so it never grows
// with the number of runs. A monitor that measures both runs has two rows (see
// LighthouseStrategyTargets).
type MonitorLighthouse struct {
	MonitorID uint `gorm:"primaryKey" json:"monitor_id"`
	// Strategy is the run that produced the scores: mobile or desktop, never
	// "both" (which means two runs, and therefore two rows).
	Strategy string `gorm:"primaryKey;size:20;not null" json:"strategy"`

	// Status is ok or error; a failed run carries Message instead of scores.
	Status  string `gorm:"size:20;not null;default:ok" json:"status"`
	Message string `gorm:"size:500" json:"message"`
	// FinalURL is the page Lighthouse actually audited (after the redirects).
	FinalURL string `gorm:"size:500" json:"final_url"`
	// RanAt is when the API call happened, FetchTime when Lighthouse rendered
	// the page.
	RanAt     time.Time  `json:"ran_at"`
	FetchTime *time.Time `json:"fetch_time"`
	RanByNode string     `gorm:"size:64" json:"ran_by_node"`

	LighthouseMeasurements `gorm:"embedded"`
}

// TableName keeps the table name stable.
func (MonitorLighthouse) TableName() string { return "monitor_lighthouses" }

// Info converts the row into the payload exposed to the API and the UI.
func (l *MonitorLighthouse) Info() *LighthouseResult {
	if l == nil {
		return nil
	}
	return &LighthouseResult{
		Status:                 l.Status,
		Message:                l.Message,
		Strategy:               l.Strategy,
		FinalURL:               l.FinalURL,
		RanAt:                  l.RanAt,
		FetchTime:              l.FetchTime,
		RanByNode:              l.RanByNode,
		LighthouseMeasurements: l.LighthouseMeasurements,
	}
}

// History ------------------------------------------------------------------

// LighthouseRun is one point of the history the web performance page charts.
//
// It keeps the same measurements as MonitorLighthouse plus the identity of the
// run, so the chart never has to read the current row to label an old point.
type LighthouseRun struct {
	ID        uint `gorm:"primaryKey" json:"id"`
	MonitorID uint `gorm:"not null;index:idx_lighthouse_runs_monitor,priority:1" json:"monitor_id"`
	// RanAt is when the API call happened (the chart's x axis).
	RanAt time.Time `gorm:"not null;index:idx_lighthouse_runs_monitor,priority:2" json:"ran_at"`
	// Strategy, Status and Message describe the run itself.
	Strategy  string `gorm:"size:20" json:"strategy"`
	Status    string `gorm:"size:20" json:"status"`
	Message   string `gorm:"size:500" json:"message"`
	RanByNode string `gorm:"size:64" json:"ran_by_node"`

	LighthouseMeasurements `gorm:"embedded"`
}

// TableName keeps the table name stable.
func (LighthouseRun) TableName() string { return "lighthouse_runs" }

// LighthouseScoreBand labels a category score the way the Lighthouse report
// does: 90 and above is good, 50 and above average, below that poor. It lives
// with the model so the API and the UI cannot colour the same score differently.
func LighthouseScoreBand(score int) string {
	switch {
	case score >= 90:
		return "good"
	case score >= 50:
		return "average"
	}
	return "poor"
}
