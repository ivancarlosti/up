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

// TestValidateRejectsLighthouseOnAnUnsupportedType keeps the switch where
// PageSpeed Insights can actually audit a page: a tcp, dns, ssl or websocket
// monitor has no page to score, so accepting the flag would only ever produce a
// measurement that fails.
func TestValidateRejectsLighthouseOnAnUnsupportedType(t *testing.T) {
	service := newValidationService()
	for _, monitorType := range []models.MonitorType{
		models.MonitorTypeTCP, models.MonitorTypeDNS, models.MonitorTypeSSL, models.MonitorTypeWebSocket,
	} {
		monitor := &models.Monitor{
			Name: "site", Type: monitorType, IntervalSeconds: 60, TimeoutSeconds: 10, RunOn: "all",
			LighthouseEnabled: true, Config: models.MonitorConfig{},
		}
		switch monitorType {
		case models.MonitorTypeTCP, models.MonitorTypeSSL:
			monitor.Config = models.MonitorConfig{Host: "example.com", Port: 443}
		case models.MonitorTypeDNS:
			monitor.Config = models.MonitorConfig{Hostname: "example.com", RecordType: "A", ResolverServer: "1.1.1.1"}
		case models.MonitorTypeWebSocket:
			monitor.Config = models.MonitorConfig{URL: "wss://example.com/socket"}
		}
		if err := service.Validate(monitor); err == nil {
			t.Fatalf("lighthouse_enabled on a %s monitor must be rejected", monitorType)
		}
	}
}

// TestValidateClearsTheStrategyWithoutTheSwitch keeps the strategy tied to the
// measurement: a monitor that does not measure keeps no strategy, so a stale
// "desktop" cannot silently apply to the next time the switch is turned on.
func TestValidateClearsTheStrategyWithoutTheSwitch(t *testing.T) {
	service := newValidationService()
	monitor := &models.Monitor{
		Name: "site", Type: models.MonitorTypeHTTP, IntervalSeconds: 60, TimeoutSeconds: 10,
		RunOn: "all", Config: models.MonitorConfig{URL: "https://www.example.com", LighthouseStrategy: "desktop"},
	}
	if err := service.Validate(monitor); err != nil {
		t.Fatalf("validate: %v", err)
	}
	if monitor.Config.LighthouseStrategy != "" {
		t.Fatalf("the strategy must not outlive the switch, got %q", monitor.Config.LighthouseStrategy)
	}

	// With the switch on the strategy is kept (and normalized).
	monitor.LighthouseEnabled = true
	monitor.Config.LighthouseStrategy = "Desktop"
	if err := service.Validate(monitor); err != nil {
		t.Fatalf("validate: %v", err)
	}
	if monitor.Config.LighthouseStrategy != models.LighthouseStrategyDesktop {
		t.Fatalf("strategy = %q", monitor.Config.LighthouseStrategy)
	}
}

// TestValidateClearsTheCadenceWithoutTheSwitch keeps the cadence tied to the
// measurement, exactly like the strategy: a monitor that does not measure keeps
// no cadence, so a stale "hourly" cannot silently apply to the next time the
// switch is turned on.
func TestValidateClearsTheCadenceWithoutTheSwitch(t *testing.T) {
	service := newValidationService()
	monitor := &models.Monitor{
		Name: "site", Type: models.MonitorTypeHTTP, IntervalSeconds: 60, TimeoutSeconds: 10,
		RunOn: "all", Config: models.MonitorConfig{URL: "https://www.example.com", LighthouseInterval: "hourly"},
	}
	if err := service.Validate(monitor); err != nil {
		t.Fatalf("validate: %v", err)
	}
	if monitor.Config.LighthouseInterval != "" {
		t.Fatalf("the cadence must not outlive the switch, got %q", monitor.Config.LighthouseInterval)
	}

	// With the switch on the cadence is kept (and normalized).
	monitor.LighthouseEnabled = true
	monitor.Config.LighthouseInterval = " 6H "
	if err := service.Validate(monitor); err != nil {
		t.Fatalf("validate: %v", err)
	}
	if monitor.Config.LighthouseInterval != models.LighthouseIntervalEvery6H {
		t.Fatalf("cadence = %q", monitor.Config.LighthouseInterval)
	}

	// An absent cadence is normalized to the documented default instead of
	// leaving the monitor without a schedule.
	monitor.Config.LighthouseInterval = ""
	if err := service.Validate(monitor); err != nil {
		t.Fatalf("validate: %v", err)
	}
	if monitor.Config.LighthouseInterval != models.DefaultLighthouseInterval {
		t.Fatalf("cadence = %q", monitor.Config.LighthouseInterval)
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
