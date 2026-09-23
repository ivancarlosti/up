package services

import (
	"reflect"
	"testing"
	"time"

	"github.com/ivancarlosti/up/internal/models"
)

// TestPlanCertificateAlerts documents the hybrid cadence: every configured
// threshold alerts once when it is crossed, inside the tightest window the
// reminder repeats once a day and an expired certificate reports its own event.
func TestPlanCertificateAlerts(t *testing.T) {
	const day = int64(20000)
	warn := []int{7, 6, 5, 30}

	cases := []struct {
		name       string
		daysLeft   int
		expired    bool
		notified   []int
		lastDay    int64
		wantEvent  models.NotificationEvent
		wantMark   []int
		wantDaily  bool
		wantThresh []int
	}{
		{
			name:     "far from expiry stays silent",
			daysLeft: 45,
		},
		{
			name:       "the highest threshold alerts once",
			daysLeft:   30,
			wantEvent:  models.EventCertExpiring,
			wantMark:   []int{30},
			wantThresh: []int{30},
		},
		{
			name:     "an already alerted threshold does not repeat",
			daysLeft: 29,
			notified: []int{30},
		},
		{
			name:       "each configured day alerts",
			daysLeft:   7,
			notified:   []int{30},
			wantEvent:  models.EventCertExpiring,
			wantMark:   []int{7},
			wantThresh: []int{7},
		},
		{
			name:      "inside the window the reminder is daily",
			daysLeft:  4,
			notified:  []int{30, 7, 6, 5},
			lastDay:   day - 1,
			wantEvent: models.EventCertExpiring,
			wantDaily: true,
		},
		{
			name:     "the daily reminder does not repeat on the same day",
			daysLeft: 4,
			notified: []int{30, 7, 6, 5},
			lastDay:  day,
		},
		{
			// The process was offline while the certificate went from 31 to 4
			// days: one notification, not a burst of four.
			name:       "missed thresholds collapse into one alert",
			daysLeft:   4,
			notified:   []int{},
			wantEvent:  models.EventCertExpiring,
			wantMark:   []int{5, 6, 7, 30},
			wantThresh: []int{5, 6, 7, 30},
		},
		{
			name:      "an expired certificate reports its own event",
			daysLeft:  -3,
			expired:   true,
			wantEvent: models.EventCertExpired,
			wantMark:  []int{5, 6, 7, 30},
			wantDaily: true,
		},
		{
			name:      "the expired report is daily",
			daysLeft:  -3,
			expired:   true,
			notified:  []int{30, 7, 6, 5},
			lastDay:   day,
			wantEvent: "",
		},
		{
			name:     "without thresholds nothing is planned",
			daysLeft: 3,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			thresholds := warn
			if testCase.name == "without thresholds nothing is planned" {
				thresholds = nil
			}
			plan := planCertificateAlerts(testCase.daysLeft, testCase.expired, thresholds, testCase.notified, testCase.lastDay, day)
			if plan.Event != testCase.wantEvent {
				t.Fatalf("event = %q, want %q", plan.Event, testCase.wantEvent)
			}
			if !reflect.DeepEqual(plan.Mark, testCase.wantMark) && !(len(plan.Mark) == 0 && len(testCase.wantMark) == 0) {
				t.Fatalf("mark = %v, want %v", plan.Mark, testCase.wantMark)
			}
			if plan.Daily != testCase.wantDaily {
				t.Fatalf("daily = %t, want %t", plan.Daily, testCase.wantDaily)
			}
			if len(testCase.wantThresh) > 0 && !reflect.DeepEqual(plan.Thresholds, testCase.wantThresh) {
				t.Fatalf("thresholds = %v, want %v", plan.Thresholds, testCase.wantThresh)
			}
		})
	}
}

// TestParseCertWarnDays keeps the operator list free form: any numbers, any
// order, cleaned up internally but never rewritten.
func TestParseCertWarnDays(t *testing.T) {
	days, err := models.ParseCertWarnDays("7,6,5,30")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !reflect.DeepEqual(days, []int{5, 6, 7, 30}) {
		t.Fatalf("days = %v", days)
	}
	if days, err := models.ParseCertWarnDays(" 10 , 10 , 2 "); err != nil || !reflect.DeepEqual(days, []int{2, 10}) {
		t.Fatalf("duplicates/dashes: %v %v", days, err)
	}
	if days, err := models.ParseCertWarnDays(""); err != nil || days != nil {
		t.Fatalf("empty list must be accepted, got %v %v", days, err)
	}
	if _, err := models.ParseCertWarnDays("7,abc"); err == nil {
		t.Fatal("a non numeric value must be rejected")
	}
	if _, err := models.ParseCertWarnDays("-2"); err == nil {
		t.Fatal("a negative value must be rejected")
	}
	if _, err := models.ParseCertWarnDays("99999"); err == nil {
		t.Fatal("an absurd value must be rejected")
	}
	fallback := models.WarnDaysOrDefault("")
	if !reflect.DeepEqual(fallback, []int{1, 7, 14, 30}) {
		t.Fatalf("default list = %v", fallback)
	}
	if custom := models.WarnDaysOrDefault("3"); !reflect.DeepEqual(custom, []int{3}) {
		t.Fatalf("custom list = %v", custom)
	}
}

// TestDaysLeft fixes the arithmetic used to age a certificate.
func TestDaysLeft(t *testing.T) {
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		notAfter time.Time
		want     int
	}{
		{now.Add(48 * time.Hour), 2},
		{now.Add(12 * time.Hour), 0},
		{now.Add(-1 * time.Hour), -1},
		{now.Add(30 * 24 * time.Hour), 30},
	}
	for _, testCase := range cases {
		if got := models.DaysLeft(testCase.notAfter, now); got != testCase.want {
			t.Fatalf("DaysLeft(%s) = %d, want %d", testCase.notAfter, got, testCase.want)
		}
	}
}
