package models

// ---------------------------------------------------------------------------
// Hardware monitoring retention (Admin > Settings)
// ---------------------------------------------------------------------------

const (
	// DefaultSystemRetentionDays is how much hardware history is kept when
	// nothing else is configured: a month of one sample per minute is the
	// period the charts offer, and it keeps the tables small (about 43k rows
	// per system).
	DefaultSystemRetentionDays = 30
	// MaxSystemRetentionDays bounds the operator input (ten years), like the
	// other retentions.
	MaxSystemRetentionDays = 3650
)

// NormalizeSystemRetentionDays clamps a retention to the accepted range.
//
// 0 means "never purge" and is kept as is (the way to switch the job off); a
// negative value reads as the documented default rather than as "delete
// everything".
func NormalizeSystemRetentionDays(days int) int {
	switch {
	case days < 0:
		return DefaultSystemRetentionDays
	case days > MaxSystemRetentionDays:
		return MaxSystemRetentionDays
	}
	return days
}

// SystemChartWindows are the periods the hardware charts offer, in hours. They
// are also the buckets of the downsampling: a 24 h chart asks for one point
// every five minutes, a 30 d chart for one every two hours, so the payload of a
// chart never depends on how much history exists.
var SystemChartWindows = []int{1, 6, 24, 168, 720}

// NormalizeSystemChartWindow returns the closest offered window.
func NormalizeSystemChartWindow(hours int) int {
	if hours <= 0 {
		return 24
	}
	best := SystemChartWindows[0]
	bestDelta := -1
	for _, allowed := range SystemChartWindows {
		delta := allowed - hours
		if delta < 0 {
			delta = -delta
		}
		if bestDelta < 0 || delta < bestDelta {
			best = allowed
			bestDelta = delta
		}
	}
	return best
}
