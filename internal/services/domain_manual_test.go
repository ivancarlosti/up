package services

import (
	"testing"
	"time"

	"github.com/ivancarlosti/up/internal/models"
)

// manualTestNow is the fixed clock of these tests: the remaining validity is part
// of what a pass decides, so it cannot come from time.Now().
var manualTestNow = time.Date(2026, 5, 10, 12, 0, 0, 0, time.UTC)

// manualTestDate is the date an operator types in these tests.
var manualTestDate = time.Date(2028, 1, 9, 0, 0, 0, 0, time.UTC)

// manualWatcher builds a monitor of the domain that watches it.
func manualWatcher(id uint, url string, date *time.Time) *models.Monitor {
	return &models.Monitor{
		ID: id, Type: models.MonitorTypeHTTP, DomainWatch: true, DomainExpiresAt: date,
		Config: models.MonitorConfig{URL: url},
	}
}

// TestPlanManualDatesAppliesToEveryWatcherOfTheDomain is the promise of the
// feature: a date typed on ONE monitor of a domain reaches every monitor of it,
// without waiting for the daily job and without any lookup.
func TestPlanManualDatesAppliesToEveryWatcherOfTheDomain(t *testing.T) {
	monitors := []*models.Monitor{
		manualWatcher(1, "https://www.example.com", &manualTestDate),
		manualWatcher(2, "https://app.example.com", nil),
		manualWatcher(3, "https://mail.other.org", nil),
	}

	plan := planManualDates(monitors, nil, manualTestNow, "")
	if len(plan.writes) != 2 {
		t.Fatalf("writes = %v, want the two watchers of example.com", plan.writes)
	}
	for _, entry := range plan.writes {
		if entry.MonitorID != 1 && entry.MonitorID != 2 {
			t.Fatalf("unexpected write for monitor %d", entry.MonitorID)
		}
		if entry.Info == nil || entry.Info.Source != models.DomainSourceManual || entry.Info.Status != models.DomainStatusOK {
			t.Fatalf("observation = %+v", entry.Info)
		}
		if !entry.Info.ExpiresAt.Equal(manualTestDate) {
			t.Fatalf("expires_at = %s, want %s", entry.Info.ExpiresAt, manualTestDate)
		}
		if entry.Info.DaysLeft != models.DaysLeft(manualTestDate, manualTestNow) {
			t.Fatalf("days_left = %d, want %d", entry.Info.DaysLeft, models.DaysLeft(manualTestDate, manualTestNow))
		}
		// The observation names the registrable domain, so the two subdomain
		// monitors agree on what the date belongs to.
		if entry.Info.Domain != "example.com" {
			t.Fatalf("domain = %q, want example.com", entry.Info.Domain)
		}
	}
	if len(plan.drops) != 0 {
		t.Fatalf("drops = %v, want none", plan.drops)
	}
}

// TestPlanManualDatesOverwritesAStaleLookup is the "no parser" fix: a domain
// whose stored observation is an unsupported TLD is rewritten with the manual
// date instead of keeping the stale status.
func TestPlanManualDatesOverwritesAStaleLookup(t *testing.T) {
	monitors := []*models.Monitor{manualWatcher(7, "https://example.io", &manualTestDate)}
	stored := []models.MonitorDomain{{
		MonitorID: 7, Domain: "example.io",
		Status: string(models.DomainStatusUnsupported),
	}}
	plan := planManualDates(monitors, stored, manualTestNow, "")
	if len(plan.writes) != 1 || plan.writes[0].MonitorID != 7 {
		t.Fatalf("writes = %v, want the stale monitor rewritten", plan.writes)
	}
	if len(plan.drops) != 0 {
		t.Fatalf("drops = %v, want none (the write replaces the row)", plan.drops)
	}
}

// TestPlanManualDatesIsIdempotentAndAges keeps two properties at once: a pass on
// an already applied date writes nothing, and the next day the same row is aged
// (the remaining validity is part of the comparison).
func TestPlanManualDatesIsIdempotentAndAges(t *testing.T) {
	monitors := []*models.Monitor{manualWatcher(1, "https://example.com", &manualTestDate)}
	stored := []models.MonitorDomain{{
		MonitorID: 1, Domain: "example.com", Source: string(models.DomainSourceManual),
		Status: string(models.DomainStatusOK), ExpiresAt: manualTestDate,
		DaysLeft: models.DaysLeft(manualTestDate, manualTestNow),
	}}

	plan := planManualDates(monitors, stored, manualTestNow, "")
	if len(plan.writes) != 0 || len(plan.drops) != 0 {
		t.Fatalf("a second pass must be a no-op, got %v %v", plan.writes, plan.drops)
	}

	plan = planManualDates(monitors, stored, manualTestNow.AddDate(0, 0, 1), "")
	if len(plan.writes) != 1 || plan.writes[0].Info.DaysLeft != stored[0].DaysLeft-1 {
		t.Fatalf("a day later the date must age: %v", plan.writes)
	}
}

// TestPlanManualDatesDropsClearedDates documents the other half of the rule: a
// date that was cleared takes its manual observation away, so the next job can
// resolve the domain again instead of showing a date nobody stored.
func TestPlanManualDatesDropsClearedDates(t *testing.T) {
	monitors := []*models.Monitor{manualWatcher(1, "https://example.com", nil)}
	stored := []models.MonitorDomain{{
		MonitorID: 1, Domain: "example.com", Source: string(models.DomainSourceManual),
		Status: string(models.DomainStatusOK), ExpiresAt: manualTestDate,
		DaysLeft: models.DaysLeft(manualTestDate, manualTestNow),
	}}

	plan := planManualDates(monitors, stored, manualTestNow, "")
	if len(plan.writes) != 0 {
		t.Fatalf("writes = %v, want none", plan.writes)
	}
	if len(plan.drops) != 1 || plan.drops[0] != 1 {
		t.Fatalf("drops = %v, want monitor 1", plan.drops)
	}
}

// TestPlanManualDatesReadsTheDateFromEveryMonitorOfTheDomain covers the reported
// inconsistency: the date only lives on one monitor (the others never watched, or
// never were saved again), and the newest value is the operator's last word.
func TestPlanManualDatesReadsTheDateFromEveryMonitorOfTheDomain(t *testing.T) {
	older := manualTestDate.AddDate(-1, 0, 0)
	monitors := []*models.Monitor{
		// A monitor that carries the date without watching the domain: it is not
		// reported, but its date is still part of the domain's truth.
		{
			ID: 1, Type: models.MonitorTypeSSL, DomainExpiresAt: &older,
			Config: models.MonitorConfig{Host: "www.example.com"},
		},
		manualWatcher(2, "https://api.example.com", &manualTestDate),
		// A bare monitor of the domain: neither a date nor the watch.
		{ID: 3, Type: models.MonitorTypeHTTP, Config: models.MonitorConfig{URL: "https://static.example.com"}},
	}

	plan := planManualDates(monitors, nil, manualTestNow, "")
	if len(plan.writes) != 1 || plan.writes[0].MonitorID != 2 {
		t.Fatalf("writes = %v, want only the watcher of the domain", plan.writes)
	}
	if !plan.writes[0].Info.ExpiresAt.Equal(manualTestDate) {
		t.Fatalf("expires_at = %s, want the newest date %s", plan.writes[0].Info.ExpiresAt, manualTestDate)
	}
}

// TestPlanManualDatesRestrictsOneDomain covers the write path: a save applies the
// date of its own domain and leaves the others to the daily pass.
func TestPlanManualDatesRestrictsOneDomain(t *testing.T) {
	monitors := []*models.Monitor{
		manualWatcher(1, "https://www.example.com", &manualTestDate),
		manualWatcher(2, "https://www.other.org", &manualTestDate),
	}

	plan := planManualDates(monitors, nil, manualTestNow, "example.com")
	if len(plan.writes) != 1 || plan.writes[0].MonitorID != 1 {
		t.Fatalf("writes = %v, want only example.com", plan.writes)
	}
}

// TestPlanManualDatesIsDeterministic keeps the logs and the writes stable: map
// iteration order must not leak into the plan.
func TestPlanManualDatesIsDeterministic(t *testing.T) {
	monitors := []*models.Monitor{
		manualWatcher(3, "https://c.example.com", &manualTestDate),
		manualWatcher(1, "https://a.example.com", &manualTestDate),
		manualWatcher(2, "https://b.example.com", &manualTestDate),
	}
	first := planManualDates(monitors, nil, manualTestNow, "")
	for attempt := 0; attempt < 10; attempt++ {
		again := planManualDates(monitors, nil, manualTestNow, "")
		for i := range first.writes {
			if first.writes[i].MonitorID != again.writes[i].MonitorID {
				t.Fatalf("order changed: %v then %v", first.writes, again.writes)
			}
		}
	}
	if first.writes[0].MonitorID != 1 || first.writes[2].MonitorID != 3 {
		t.Fatalf("writes = %v, want them ordered by monitor id", first.writes)
	}
}

// TestManualRowCurrent fixes what "already applied" means, including the ageing.
func TestManualRowCurrent(t *testing.T) {
	date := manualTestDate
	current := &models.MonitorDomain{
		Source: string(models.DomainSourceManual), Status: string(models.DomainStatusOK),
		ExpiresAt: date, DaysLeft: models.DaysLeft(date, manualTestNow),
	}
	if !manualRowCurrent(current, date, manualTestNow) {
		t.Fatal("a row carrying the manual date with the right days left is current")
	}
	if manualRowCurrent(nil, date, manualTestNow) {
		t.Fatal("a missing row is never current")
	}
	other := *current
	other.DaysLeft = current.DaysLeft + 1
	if manualRowCurrent(&other, date, manualTestNow) {
		t.Fatal("a row with a stale days_left must be rewritten")
	}
	rdap := *current
	rdap.Source = string(models.DomainSourceRDAP)
	if manualRowCurrent(&rdap, date, manualTestNow) {
		t.Fatal("a lookup must not pass for a manual observation")
	}
	unsupported := *current
	unsupported.Status = string(models.DomainStatusUnsupported)
	if manualRowCurrent(&unsupported, date, manualTestNow) {
		t.Fatal("an unsupported row must be rewritten with the manual date")
	}
	older := *current
	older.ExpiresAt = date.AddDate(-1, 0, 0)
	if manualRowCurrent(&older, date, manualTestNow) {
		t.Fatal("a different date must be rewritten")
	}
}
