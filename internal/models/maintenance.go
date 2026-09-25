package models

import "fmt"

// ---------------------------------------------------------------------------
// Runtime maintenance settings (Admin > Settings)
// ---------------------------------------------------------------------------
//
// Both values live in the shared settings table, so a cluster converges on one
// configuration and an operator can change it without restarting a node.

// History retention --------------------------------------------------------

const (
	// DefaultHeartbeatRetentionDays is the retention applied when neither the
	// setting nor HEARTBEAT_RETENTION_DAYS says anything: half a year of
	// heartbeat history. Keeping less is a deliberate act, never a silent
	// default.
	DefaultHeartbeatRetentionDays = 180
	// MaxHeartbeatRetentionDays bounds the operator input (ten years): anything
	// larger is almost certainly a typo.
	MaxHeartbeatRetentionDays = 3650
)

// NormalizeHeartbeatRetentionDays clamps a retention to the accepted range.
//
// 0 means "never purge" and is kept as is: it is the way to switch the job off,
// and a negative value (a hand edited request) reads as the documented default
// rather than as "delete everything".
func NormalizeHeartbeatRetentionDays(days int) int {
	switch {
	case days < 0:
		return DefaultHeartbeatRetentionDays
	case days > MaxHeartbeatRetentionDays:
		return MaxHeartbeatRetentionDays
	}
	return days
}

// Uptime window ------------------------------------------------------------

const (
	// DefaultUptimeWindowHours is the period the dashboard, the monitors table
	// and the heartbeat bars cover when nothing else is configured.
	DefaultUptimeWindowHours = 24
)

// UptimeWindowHoursAllowed is every period offered by Admin > Settings and by
// the per status page override: 24 h, 7 d, 14 d and 30 d.
var UptimeWindowHoursAllowed = []int{24, 168, 336, 720}

// NormalizeUptimeWindowHours returns the closest allowed window.
//
// An unknown value (a hand edited request, a stale status page setting) falls
// back to the default instead of rendering a period the UI cannot label.
func NormalizeUptimeWindowHours(hours int) int {
	if hours <= 0 {
		return DefaultUptimeWindowHours
	}
	best := DefaultUptimeWindowHours
	bestDelta := -1
	for _, allowed := range UptimeWindowHoursAllowed {
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

// UptimeWindowLabel renders a window as the short label the UI shows next to an
// uptime figure ("24h", "7d", "14d", "30d").
func UptimeWindowLabel(hours int) string {
	switch NormalizeUptimeWindowHours(hours) {
	case 24:
		return "24h"
	case 168:
		return "7d"
	case 336:
		return "14d"
	case 720:
		return "30d"
	}
	return fmt.Sprintf("%dh", hours)
}
