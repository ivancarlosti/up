package services

import (
	"testing"
	"time"

	"github.com/ivancarlosti/up/internal/models"
)

// TestPlanDomainAlerts documents that the domain reminders follow exactly the
// certificate cadence: every configured threshold alerts once when crossed, and
// below the smallest one the reminder repeats once a day.
func TestPlanDomainAlerts(t *testing.T) {
	const day = int64(20000)
	warn := []int{7, 6, 5, 30}

	cases := []struct {
		name      string
		daysLeft  int
		expired   bool
		notified  []int
		lastDay   int64
		wantEvent models.NotificationEvent
		wantMark  []int
		wantDaily bool
	}{
		{name: "far from expiry stays silent", daysLeft: 45},
		{
			name:      "a crossed threshold alerts once",
			daysLeft:  30,
			wantEvent: models.EventDomainExpiring,
			wantMark:  []int{30},
		},
		{
			name:     "an already alerted threshold does not repeat",
			daysLeft: 29,
			notified: []int{30},
		},
		{
			name:      "inside the window the reminder is daily",
			daysLeft:  4,
			notified:  []int{30, 7, 6, 5},
			lastDay:   day - 1,
			wantEvent: models.EventDomainExpiring,
			wantDaily: true,
		},
		{
			name:     "the daily reminder does not repeat on the same day",
			daysLeft: 4,
			notified: []int{30, 7, 6, 5},
			lastDay:  day,
		},
		{
			name:      "an expired domain reports its own event",
			daysLeft:  -2,
			expired:   true,
			wantEvent: models.EventDomainExpired,
			wantMark:  []int{5, 6, 7, 30},
			wantDaily: true,
		},
		{
			name:     "the expired report is daily",
			daysLeft: -2,
			expired:  true,
			notified: []int{30, 7, 6, 5},
			lastDay:  day,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			plan := planDomainAlerts(testCase.daysLeft, testCase.expired, warn, testCase.notified, testCase.lastDay, day)
			if plan.Event != testCase.wantEvent {
				t.Fatalf("event = %q, want %q", plan.Event, testCase.wantEvent)
			}
			if len(plan.Mark) != len(testCase.wantMark) {
				t.Fatalf("mark = %v, want %v", plan.Mark, testCase.wantMark)
			}
			for i := range plan.Mark {
				if plan.Mark[i] != testCase.wantMark[i] {
					t.Fatalf("mark = %v, want %v", plan.Mark, testCase.wantMark)
				}
			}
			if plan.Daily != testCase.wantDaily {
				t.Fatalf("daily = %t, want %t", plan.Daily, testCase.wantDaily)
			}
		})
	}
}

// TestDomainWarnDays checks the monitor level fallback.
func TestDomainWarnDays(t *testing.T) {
	fallback := domainWarnDays(&models.Monitor{})
	if len(fallback) != 4 {
		t.Fatalf("fallback = %v", fallback)
	}
	custom := domainWarnDays(&models.Monitor{DomainWarnDays: "3,10"})
	if len(custom) != 2 || custom[0] != 3 || custom[1] != 10 {
		t.Fatalf("custom = %v", custom)
	}
}

// TestDomainMessage covers the readable payload of the notification.
func TestDomainMessage(t *testing.T) {
	row := &models.MonitorDomain{
		Domain:    "example.com",
		Registrar: "ACME Registrar",
		ExpiresAt: time.Date(2027, 5, 1, 0, 0, 0, 0, time.UTC),
	}
	message := domainMessage(row, 12, false, ExpiryPlan{})
	if message != "domain example.com expires in 12 days, on 2027-05-01 (registrar: ACME Registrar)" {
		t.Fatalf("message = %q", message)
	}
	expired := domainMessage(row, -2, true, ExpiryPlan{})
	if expired != "domain example.com EXPIRED on 2027-05-01 (registrar: ACME Registrar)" {
		t.Fatalf("expired message = %q", expired)
	}
	daily := domainMessage(row, 3, false, ExpiryPlan{Daily: true})
	if daily != "domain example.com still expires in 3 days, on 2027-05-01 (registrar: ACME Registrar)" {
		t.Fatalf("daily message = %q", daily)
	}
}

// TestDayBucket keeps the "written at most once a day" rule honest.
func TestDayBucket(t *testing.T) {
	morning := time.Date(2026, 5, 10, 6, 0, 0, 0, time.UTC)
	evening := time.Date(2026, 5, 10, 23, 59, 0, 0, time.UTC)
	nextDay := time.Date(2026, 5, 11, 0, 1, 0, 0, time.UTC)
	if dayBucket(morning) != dayBucket(evening) {
		t.Fatal("the same UTC day must share a bucket")
	}
	if dayBucket(morning) == dayBucket(nextDay) {
		t.Fatal("a different day must have a different bucket")
	}
	if dayBucket(time.Time{}) != 0 {
		t.Fatal("a zero time has no bucket")
	}
}
