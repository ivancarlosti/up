package services

import (
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ivancarlosti/up/internal/models"
)

// ExpiryPlan is the decision of an expiry watcher for one monitor.
//
// It is shared by the certificate and the domain watchers on purpose: "the dates
// when the operator is notified" and "the smallest one repeats daily" must mean
// exactly the same thing for both.
type ExpiryPlan struct {
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

// CertPlan is the certificate flavour of ExpiryPlan (kept as an alias so the
// certificate watcher keeps reading naturally).
type CertPlan = ExpiryPlan

// planExpiryAlerts is the pure core of both expiry watchers.
//
// The cadence is hybrid (see docs/monitors.md):
//
//   - every configured threshold (any free form list, e.g. "7,6,5,30") is
//     alerted once, when it is crossed — even if the process was down on the
//     exact day, because the thresholds are compared against what was already
//     alerted and not against a calendar;
//   - while less than min(thresholds) days are left, the reminder is repeated
//     once per day (that is the "1x por dia" part);
//   - an already expired target reports the expired event (also once per day,
//     until it is renewed).
//
// All the crossed thresholds are marked at once, so a monitor that was offline
// while the certificate went from 31 to 4 days left produces a single
// notification ("expires in 4 days") instead of a burst of four.
func planExpiryAlerts(daysLeft int, expired bool, warn []int, notified []int, lastNotifiedDay, today int64,
	expiringEvent, expiredEvent models.NotificationEvent) ExpiryPlan {
	plan := ExpiryPlan{DaysLeft: daysLeft}
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
		plan.Event = expiredEvent
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
		plan.Event = expiringEvent
		plan.Thresholds = append([]int{}, crossed...)
		plan.Mark = crossed
		return plan
	}

	// Daily reinforcement once the target is inside the tightest window.
	if daysLeft <= minimum && lastNotifiedDay != today {
		plan.Event = expiringEvent
		plan.Daily = true
	}
	return plan
}

// planCertificateAlerts is the certificate watcher of planExpiryAlerts.
func planCertificateAlerts(daysLeft int, expired bool, warn []int, notified []int, lastNotifiedDay, today int64) CertPlan {
	return planExpiryAlerts(daysLeft, expired, warn, notified, lastNotifiedDay, today,
		models.EventCertExpiring, models.EventCertExpired)
}

// planDomainAlerts is the domain watcher of planExpiryAlerts.
func planDomainAlerts(daysLeft int, expired bool, warn []int, notified []int, lastNotifiedDay, today int64) ExpiryPlan {
	return planExpiryAlerts(daysLeft, expired, warn, notified, lastNotifiedDay, today,
		models.EventDomainExpiring, models.EventDomainExpired)
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

// domainWarnDays returns the domain thresholds of a monitor (its own list or the
// default, which is the same list the certificate watcher uses).
func domainWarnDays(monitor *models.Monitor) []int {
	if monitor == nil {
		return models.DomainWarnDaysOrDefault("")
	}
	return models.DomainWarnDaysOrDefault(monitor.DomainWarnDays)
}

// thresholdsLabel renders the notified thresholds for the database ("7,30").
func thresholdsLabel(values []int) string {
	sorted := uniqueSorted(values)
	parts := make([]string, 0, len(sorted))
	for _, value := range sorted {
		parts = append(parts, strconv.Itoa(value))
	}
	return strings.Join(parts, ",")
}

// dayBucket is the UTC day number of a moment (0 for a zero time). It is what
// keeps the stored expiry rows written at most once a day.
func dayBucket(moment time.Time) int64 {
	if moment.IsZero() {
		return 0
	}
	return moment.UTC().Unix() / 86400
}
