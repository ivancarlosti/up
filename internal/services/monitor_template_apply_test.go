package services

import (
	"strings"
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
	if change, ok := byField["notification_ids"]; !ok || change.From != "1" || change.To != "1,2" {
		t.Fatalf("notification_ids change = %+v", change)
	}
	// Groups and tags belong to the monitor, never to the template: two monitors
	// can follow the same template and live in different groups with different
	// tags, so neither is ever part of the diff.
	for _, field := range []string{"tags", "group_ids"} {
		if _, ok := byField[field]; ok {
			t.Fatalf("%s must not be part of the template fields", field)
		}
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

	updated, notificationIDs, groupIDs := applyTemplate(monitor, template, []string{"config", "interval_seconds", "notification_ids"})
	if updated.Config.URL != "https://api.example.com/health" {
		t.Fatalf("the target changed to %q", updated.Config.URL)
	}
	if updated.Config.Method != "POST" {
		t.Fatalf("the template config was not applied, method = %q", updated.Config.Method)
	}
	if updated.IntervalSeconds != 30 {
		t.Fatalf("interval = %d", updated.IntervalSeconds)
	}
	if len(notificationIDs) != 1 || groupIDs != nil {
		t.Fatalf("links: notifications=%v groups=%v (the groups must be left alone)", notificationIDs, groupIDs)
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
	// SSL keeps its own address too: the template port (993 for IMAPS) is the
	// fallback of the bulk importer, never a silent repoint of a monitor. The SNI
	// and the TLS switches are probe options of the template and do apply.
	sslMonitor := &models.Monitor{Type: models.MonitorTypeSSL, Config: models.MonitorConfig{Host: "mail.example.com", Port: 465}}
	sslUpdated, _, _ := applyTemplate(sslMonitor, &models.MonitorTemplate{
		Type:   models.MonitorTypeSSL,
		Config: models.MonitorConfig{Port: 993, ServerName: "smtp.example.com", IgnoreTLS: true},
	}, []string{"config"})
	if sslUpdated.Config.Host != "mail.example.com" || sslUpdated.Config.Port != 465 {
		t.Fatalf("the ssl target changed: %s:%d", sslUpdated.Config.Host, sslUpdated.Config.Port)
	}
	if sslUpdated.Config.ServerName != "smtp.example.com" || !sslUpdated.Config.IgnoreTLS {
		t.Fatalf("the ssl probe options were not applied: %+v", sslUpdated.Config)
	}
}

// TestConfigChangesCoversEveryType is the safety net of the dry run: a preview
// that reports "no change" while the apply would rewrite the probe is worse than
// no preview at all (which is what the tcp/dns/ssl options used to do).
func TestConfigChangesCoversEveryType(t *testing.T) {
	tcp := configChanges(
		&models.Monitor{Type: models.MonitorTypeTCP, Config: models.MonitorConfig{Host: "db.internal", Port: 3306, Send: "PING", Expect: "PONG"}},
		&models.MonitorTemplate{Type: models.MonitorTypeTCP, Config: models.MonitorConfig{Send: "HELO", Expect: "250"}},
	)
	byField := map[string]FieldChange{}
	for _, change := range tcp {
		byField[change.Field] = change
	}
	if change, ok := byField["config.send"]; !ok || change.From != "PING" || change.To != "HELO" {
		t.Fatalf("config.send change = %+v", change)
	}
	if change, ok := byField["config.expect"]; !ok || change.From != "PONG" || change.To != "250" {
		t.Fatalf("config.expect change = %+v", change)
	}
	// The target stays out of the diff even though both configurations carry one.
	if _, ok := byField["config.host"]; ok {
		t.Fatal("the target must never appear in the diff")
	}

	dns := configChanges(
		&models.Monitor{Type: models.MonitorTypeDNS, Config: models.MonitorConfig{Hostname: "example.com", RecordType: "A", ExpectedValue: "93.184", InvertCheck: true}},
		&models.MonitorTemplate{Type: models.MonitorTypeDNS, Config: models.MonitorConfig{RecordType: "AAAA", ExpectedValue: "2606", InvertCheck: false}},
	)
	seen := map[string]bool{}
	for _, change := range dns {
		seen[change.Field] = true
	}
	for _, field := range []string{"config.record_type", "config.expected_value", "config.invert_check"} {
		if !seen[field] {
			t.Errorf("%s is missing from the dns diff: %+v", field, dns)
		}
	}

	ssl := configChanges(
		&models.Monitor{Type: models.MonitorTypeSSL, Config: models.MonitorConfig{Host: "mail.example.com", Port: 465}},
		&models.MonitorTemplate{Type: models.MonitorTypeSSL, Config: models.MonitorConfig{ServerName: "smtp.example.com", IgnoreTLS: true}},
	)
	seen = map[string]bool{}
	for _, change := range ssl {
		seen[change.Field] = true
	}
	if !seen["config.server_name"] || !seen["config.ignore_tls"] {
		t.Errorf("the ssl options are missing from the diff: %+v", ssl)
	}

	// The credentials never travel into a preview.
	secret := &models.MonitorTemplate{
		Type:   models.MonitorTypeHTTP,
		Config: models.MonitorConfig{BasicPass: "secret", BearerToken: "token", BasicUser: "operator"},
	}
	for _, change := range configChanges(&models.Monitor{Type: models.MonitorTypeHTTP}, secret) {
		if strings.Contains(change.Field, "pass") || strings.Contains(change.Field, "token") {
			t.Fatalf("a secret leaked into the diff: %+v", change)
		}
	}
}
