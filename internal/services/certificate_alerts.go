package services

import (
	"sort"
	"strconv"
	"strings"

	"github.com/ivancarlosti/up/internal/models"
)

// CertPlan is the decision of the certificate watcher for one monitor.
type CertPlan struct {
	// Event is empty when there is nothing to send.
	Event models.NotificationEvent
	// DaysLeft is the remaining validity used to build the message.
	DaysLeft int
	// Thresholds are the configured values that triggered this notification (empty
	// for the daily reinforcement).
	Thresholds []int
	// Daily is true when the notification is the daily reminder.
	Daily bool
	// Mark lists the thresholds to remember as already alerted.
	Mark []int
}

// planCertificateAlerts is the pure core of the certificate watcher.
//
// The cadence is hybrid (see docs/monitors.md):
//
//   - every configured threshold (any free form list, e.g. "7,6,5,30") is
//     alerted once, when it is crossed — even if the process was down on the
//     exact day, because the thresholds are compared against what was already
//     alerted and not against a calendar;
//   - while less than min(thresholds) days are left, the reminder is repeated
//     once per day (that is the "1x por dia" part);
//   - an expired certificate reports cert_expired (also once per day, until it is
//     renewed) — this is the only signal when the monitor ignores TLS errors.
//
// All the crossed thresholds are marked at once, so a monitor that was offline
// while the certificate went from 31 to 4 days left produces a single
// notification ("expires in 4 days") instead of a burst of four.
func planCertificateAlerts(daysLeft int, expired bool, warn []int, notified []int, lastNotifiedDay, today int64) CertPlan {
	plan := CertPlan{DaysLeft: daysLeft}
	if len(warn) == 0 {
		return plan
	}
	sorted := uniqueSorted(warn)
	minimum := sorted[0]

	alreadyNotified := map[int]bool{}
	for _, value := range notified {
		alreadyNotified[value] = true
	}
	crossed := []int{}
	for _, threshold := range sorted {
		if daysLeft <= threshold && !alreadyNotified[threshold] {
			crossed = append(crossed, threshold)
		}
	}

	if expired {
		plan.Event = models.EventCertExpired
		plan.Mark = crossed
		plan.Daily = true
		if lastNotifiedDay == today {
			// Already told today: the plan is empty (and the thresholds, if any,
			// stay marked so they neither pile up nor get lost).
			plan.Event = ""
			plan.Mark = nil
			plan.Daily = false
		}
		return plan
	}

	if len(crossed) > 0 {
		plan.Event = models.EventCertExpiring
		plan.Thresholds = append([]int{}, crossed...)
		plan.Mark = crossed
		return plan
	}

	// Daily reinforcement once the certificate is inside the tightest window.
	if daysLeft <= minimum && lastNotifiedDay != today {
		plan.Event = models.EventCertExpiring
		plan.Daily = true
	}
	return plan
}

// uniqueSorted returns the thresholds without duplicates, ascending.
func uniqueSorted(values []int) []int {
	seen := map[int]bool{}
	out := make([]int, 0, len(values))
	for _, value := range values {
		if value < 0 || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	sort.Ints(out)
	return out
}

// certWarnDays returns the thresholds of a monitor (its own list or the default).
func certWarnDays(monitor *models.Monitor) []int {
	if monitor == nil {
		return models.WarnDaysOrDefault("")
	}
	return models.WarnDaysOrDefault(monitor.CertWarnDays)
}

// certThresholdsLabel renders the notified thresholds for the database ("7,30").
func certThresholdsLabel(values []int) string {
	sorted := uniqueSorted(values)
	parts := make([]string, 0, len(sorted))
	for _, value := range sorted {
		parts = append(parts, strconv.Itoa(value))
	}
	return strings.Join(parts, ",")
}
