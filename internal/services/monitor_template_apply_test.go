package services

import (
	"testing"

	"github.com/ivancarlosti/up/internal/models"
)

// TestPlanApply documents what a bulk apply would change: the diff is what the UI
// shows before the operator confirms, so it must not report noise (an identical
// value is not a change) and it must never include the probe target.
func TestPlanApply(t *testing.T) {
	monitor := &models.Monitor{
		Name:                   "API",
		Type:                   models.MonitorTypeHTTP,
		Active:                 true,
		Description:            "old description",
		IntervalSeconds:        300,
		TimeoutSeconds:         10,
		Retries:                0,
		RetriesIntervalSeconds: 60,
		ResendIntervalSeconds:  0,
		RunOn:                  "all",
		Tags:                   "old",
		Config: models.MonitorConfig{
			URL:    "https://api.example.com/health",
			Method: "GET",
			Headers: []models.Header{
				{Key: "X-Old", Value: "1"},
			},
		},
		NotificationIDs: []uint{1},
		GroupIDs:        []uint{},
	}
	template := &models.MonitorTemplate{
		Type: models.MonitorTypeHTTP,
		Config: models.MonitorConfig{
			Method:              "HEAD",
			AcceptedStatusCodes: "200-299",
		},
		Defaults: models.TemplateDefaults{
			IntervalSeconds: 60,
			TimeoutSeconds:  10, // same as the monitor: no change
			RunOn:           "all",
			Tags:            "new",
			NotificationIDs: []uint{1, 2},
			GroupIDs:        []uint{7},
		},
	}

	changes := planApply(monitor, template, models.TemplateDefaultFields())
	byField := map[string]FieldChange{}
	for _, change := range changes {
		byField[change.Field] = change
	}

	if change, ok := byField["interval_seconds"]; !ok || change.From != "300" || change.To != "60" {
		t.Fatalf("interval_seconds change = %+v", change)
	}
	if _, ok := byField["timeout_seconds"]; ok {
		t.Fatal("an identical value must not be reported as a change")
	}
	if change, ok := byField["tags"]; !ok || change.To != "new" {
		t.Fatalf("tags change = %+v", change)
	}
	if change, ok := byField["notification_ids"]; !ok || change.From != "1" || change.To != "1,2" {
		t.Fatalf("notification_ids change = %+v", change)
	}
	if change, ok := byField["group_ids"]; !ok || change.From != "" || change.To != "7" {
		t.Fatalf("group_ids change = %+v", change)
	}
	if change, ok := byField["config.method"]; !ok || change.From != "GET" || change.To != "HEAD" {
		t.Fatalf("config.method change = %+v", change)
	}
	if change, ok := byField["config.headers"]; !ok || change.From != "1" || change.To != "0" {
		t.Fatalf("config.headers change = %+v", change)
	}
	for field := range byField {
		if field == "config.url" || field == "config.host" || field == "config.hostname" {
			t.Fatalf("the target must never be part of the diff, got %s", field)
		}
	}
	// A field that is not selected is not planned.
	onlyInterval := planApply(monitor, template, []string{"interval_seconds"})
	if len(onlyInterval) != 1 || onlyInterval[0].Field != "interval_seconds" {
		t.Fatalf("selected fields = %+v", onlyInterval)
	}
}

// TestApplyTemplateKeepsTarget is the safety net of the bulk edit: applying the
// configuration of a template must never repoint a monitor at another address.
func TestApplyTemplateKeepsTarget(t *testing.T) {
	monitor := &models.Monitor{
		Name: "API",
		Type: models.MonitorTypeHTTP,
		Config: models.MonitorConfig{
			URL:    "https://api.example.com/health",
			Method: "GET",
		},
	}
	template := &models.MonitorTemplate{
		Type:   models.MonitorTypeHTTP,
		Config: models.MonitorConfig{Method: "POST", AcceptedStatusCodes: "200"},
		Defaults: models.TemplateDefaults{
			IntervalSeconds: 30,
			NotificationIDs: []uint{1},
			GroupIDs:        []uint{2, 3},
		},
	}

	updated, notificationIDs, groupIDs := applyTemplate(monitor, template, []string{"config", "interval_seconds", "notification_ids", "group_ids"})
	if updated.Config.URL != "https://api.example.com/health" {
		t.Fatalf("the target changed to %q", updated.Config.URL)
	}
	if updated.Config.Method != "POST" {
		t.Fatalf("the template config was not applied, method = %q", updated.Config.Method)
	}
	if updated.IntervalSeconds != 30 {
		t.Fatalf("interval = %d", updated.IntervalSeconds)
	}
	if len(notificationIDs) != 1 || len(groupIDs) != 2 {
		t.Fatalf("links not applied: notifications=%v groups=%v", notificationIDs, groupIDs)
	}
	// The original monitor is not mutated (the diff was computed from it).
	if monitor.Config.Method != "GET" || monitor.IntervalSeconds != 0 {
		t.Fatalf("the source monitor was mutated: %+v", monitor)
	}
	// TCP and DNS keep their own target as well.
	tcpMonitor := &models.Monitor{Type: models.MonitorTypeTCP, Config: models.MonitorConfig{Host: "db.internal", Port: 3306}}
	tcpUpdated, _, _ := applyTemplate(tcpMonitor, &models.MonitorTemplate{Type: models.MonitorTypeTCP, Config: models.MonitorConfig{Port: 5432}}, []string{"config"})
	if tcpUpdated.Config.Host != "db.internal" || tcpUpdated.Config.Port != 3306 {
		t.Fatalf("the tcp target changed: %s:%d", tcpUpdated.Config.Host, tcpUpdated.Config.Port)
	}
	dnsMonitor := &models.Monitor{Type: models.MonitorTypeDNS, Config: models.MonitorConfig{Hostname: "example.com"}}
	dnsUpdated, _, _ := applyTemplate(dnsMonitor, &models.MonitorTemplate{Type: models.MonitorTypeDNS, Config: models.MonitorConfig{RecordType: "AAAA"}}, []string{"config"})
	if dnsUpdated.Config.Hostname != "example.com" || dnsUpdated.Config.RecordType != "AAAA" {
		t.Fatalf("the dns target changed: %+v", dnsUpdated.Config)
	}
}
