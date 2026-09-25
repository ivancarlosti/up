package services

import (
	"testing"
	"time"

	"github.com/ivancarlosti/up/internal/models"
)

// newValidationService is enough to run Validate: the service reaches the
// database only to check a template link, which these cases do not use.
func newValidationService() *MonitorService { return &MonitorService{} }

// TestValidateKeepsTheManualDateWithoutTheWatch is the regression guard of the
// reported bug: the manual date belongs to the DOMAIN, so turning the watch
// switch off (which used to clear it) must leave it stored — and with it the date
// of every sibling of the domain.
func TestValidateKeepsTheManualDateWithoutTheWatch(t *testing.T) {
	service := newValidationService()
	date := time.Date(2028, 1, 9, 0, 0, 0, 0, time.UTC)
	monitor := &models.Monitor{
		Name: "site", Type: models.MonitorTypeHTTP, IntervalSeconds: 60, TimeoutSeconds: 10,
		RunOn: "all", Config: models.MonitorConfig{URL: "https://www.example.com"},
		DomainWatch: false, DomainExpiresAt: &date, DomainWarnDays: "7",
	}
	if err := service.Validate(monitor); err != nil {
		t.Fatalf("a manual date without the watch must be accepted: %v", err)
	}
	if monitor.DomainExpiresAt == nil || !monitor.DomainExpiresAt.Equal(date) {
		t.Fatalf("the manual date must survive the watch switch, got %v", monitor.DomainExpiresAt)
	}
	// What the switch does own: nothing outlives a watch that is off.
	if monitor.DomainWarnDays != "" {
		t.Fatalf("the thresholds must be cleared, got %q", monitor.DomainWarnDays)
	}
}

// TestValidateRejectsNotifyWithoutTheWatch keeps the coherence rule the switch
// still owns: notifying about a domain nobody watches would be a silent no-op.
func TestValidateRejectsNotifyWithoutTheWatch(t *testing.T) {
	service := newValidationService()
	monitor := &models.Monitor{
		Name: "site", Type: models.MonitorTypeHTTP, IntervalSeconds: 60, TimeoutSeconds: 10,
		RunOn: "all", Config: models.MonitorConfig{URL: "https://www.example.com"},
		DomainWatch: false, DomainNotify: true,
	}
	if err := service.Validate(monitor); err == nil {
		t.Fatal("domain_notify without domain_watch must be rejected")
	}
}

// TestValidateClearsAZeroDate keeps the normalisation of the nullable column.
func TestValidateClearsAZeroDate(t *testing.T) {
	service := newValidationService()
	zero := time.Time{}
	monitor := &models.Monitor{
		Name: "site", Type: models.MonitorTypeHTTP, IntervalSeconds: 60, TimeoutSeconds: 10,
		RunOn: "all", Config: models.MonitorConfig{URL: "https://www.example.com"},
		DomainWatch: true, DomainExpiresAt: &zero,
	}
	if err := service.Validate(monitor); err != nil {
		t.Fatalf("validate: %v", err)
	}
	if monitor.DomainExpiresAt != nil {
		t.Fatalf("a zero date means no date, got %v", monitor.DomainExpiresAt)
	}
}
