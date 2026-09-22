package notify

import (
	"net/url"
	"strings"
	"testing"

	"github.com/ivancarlosti/up/internal/models"
)

func sampleMessage() Message {
	return Message{
		Event:       "down",
		Status:      "down",
		Title:       "[DOWN] API health",
		MonitorName: "API health",
		MonitorType: "http",
		MonitorURL:  "https://api.example.com/health",
		Message:     "connection refused",
		LatencyMS:   42,
		NodeID:      "up-node-1",
		InstanceURL: "https://up.example.com",
	}
}

func TestBuildSMTPURL(t *testing.T) {
	config := &models.SMTPConfig{
		Host:          "smtp.example.com",
		Port:          587,
		Username:      "up@example.com",
		Password:      "p@ss:word",
		From:          "up@example.com",
		To:            "ops@example.com, oncall@example.com",
		SubjectPrefix: "[Up]",
		UseHTML:       true,
	}
	raw := buildSMTPURL(config, sampleMessage())
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("the generated URL must parse: %v (%s)", err, raw)
	}
	if parsed.Scheme != "smtp" || parsed.Host != "smtp.example.com:587" {
		t.Fatalf("unexpected endpoint: %s", raw)
	}
	if user := parsed.User.Username(); user != "up@example.com" {
		t.Fatalf("username lost: %s", raw)
	}
	if password, _ := parsed.User.Password(); password != "p@ss:word" {
		t.Fatalf("password must survive URL encoding: %s", raw)
	}
	query := parsed.Query()
	if query.Get("fromaddress") != "up@example.com" {
		t.Fatalf("fromaddress missing: %s", raw)
	}
	// The recipient order is preserved (the operator's order is intentional).
	if query.Get("toaddresses") != "ops@example.com,oncall@example.com" {
		t.Fatalf("recipients must be comma separated: %q", query.Get("toaddresses"))
	}
	if !strings.HasPrefix(query.Get("subject"), "[Up]") {
		t.Fatalf("subject prefix lost: %q", query.Get("subject"))
	}
	if query.Get("usehtml") != "yes" || query.Get("encryption") != "Auto" {
		t.Fatalf("unexpected encryption flags: %s", raw)
	}

	secure := *config
	secure.Secure = true
	if encryption := mustQuery(t, buildSMTPURL(&secure, sampleMessage())).Get("encryption"); encryption != "ExplicitTLS" {
		t.Fatalf("secure connections must use implicit TLS, got %q", encryption)
	}
}

func TestBuildWebhookURL(t *testing.T) {
	config := &models.WebhookConfig{
		URL:         "http://hooks.internal:8080/up?existing=1",
		Method:      "PUT",
		ContentType: "application/json",
		Headers: []models.Header{
			{Key: "X-Token", Value: "abc"},
			{Key: "Content-Type", Value: "application/json; charset=utf-8"},
		},
	}
	raw, err := buildWebhookURL(config)
	if err != nil {
		t.Fatalf("build webhook URL: %v", err)
	}
	parsed := mustParse(t, raw)
	if parsed.Scheme != "generic" || parsed.Host != "hooks.internal:8080" || parsed.Path != "/up" {
		t.Fatalf("unexpected service URL: %s", raw)
	}
	query := parsed.Query()
	if query.Get("method") != "PUT" {
		t.Fatalf("method lost: %s", raw)
	}
	if query.Get("disabletls") != "yes" {
		t.Fatalf("an http webhook must disable TLS: %s", raw)
	}
	if query.Get("@X-Token") != "abc" {
		t.Fatalf("custom headers must be prefixed with @: %s", raw)
	}
	if query.Get("@Content-Type") != "application/json" {
		t.Fatalf("the content type must be forced on the header, otherwise shoutrrr sends text/plain: %s", raw)
	}

	httpsConfig := *config
	httpsConfig.URL = "https://hooks.example.com/up"
	httpsURL, err := buildWebhookURL(&httpsConfig)
	if err != nil {
		t.Fatalf("build https webhook URL: %v", err)
	}
	if disable := mustParse(t, httpsURL).Query().Get("disabletls"); disable != "" {
		t.Fatalf("https webhooks must keep TLS enabled, got disabletls=%q", disable)
	}

	if _, err := buildWebhookURL(&models.WebhookConfig{URL: "not a url"}); err == nil {
		t.Fatal("an invalid webhook URL must be rejected")
	}
}

func TestRenderBody(t *testing.T) {
	message := sampleMessage()

	defaultBody, err := message.RenderBody("")
	if err != nil {
		t.Fatalf("default body: %v", err)
	}
	if !strings.Contains(defaultBody, `"event":"down"`) || !strings.Contains(defaultBody, `"latency_ms":42`) {
		t.Fatalf("unexpected default payload: %s", defaultBody)
	}

	templated, err := message.RenderBody(`{"alert":"{{.Event}}","name":"{{.MonitorName}}","upper":"{{upper .NodeID}}"}`)
	if err != nil {
		t.Fatalf("template body: %v", err)
	}
	if !strings.Contains(templated, `"alert":"down"`) || !strings.Contains(templated, `"upper":"UP-NODE-1"`) {
		t.Fatalf("template helpers failed: %s", templated)
	}

	if _, err := message.RenderBody("{{.Event"); err == nil {
		t.Fatal("an invalid template must fail instead of sending garbage")
	}
}

func TestMonitorTarget(t *testing.T) {
	cases := []struct {
		monitor models.Monitor
		want    string
	}{
		{models.Monitor{Type: models.MonitorTypeHTTP, Config: models.MonitorConfig{URL: "https://x/y"}}, "https://x/y"},
		{models.Monitor{Type: models.MonitorTypeTCP, Config: models.MonitorConfig{Host: "db", Port: 3306}}, "db:3306"},
		{models.Monitor{Type: models.MonitorTypeDNS, Config: models.MonitorConfig{RecordType: "A", Hostname: "x.com", ResolverServer: "1.1.1.1"}}, "A x.com @1.1.1.1"},
	}
	for _, tc := range cases {
		if got := MonitorTarget(&tc.monitor); got != tc.want {
			t.Errorf("MonitorTarget() = %q, want %q", got, tc.want)
		}
	}
}

func mustQuery(t *testing.T, raw string) url.Values {
	t.Helper()
	return mustParse(t, raw).Query()
}

func mustParse(t *testing.T, raw string) *url.URL {
	t.Helper()
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse %q: %v", raw, err)
	}
	return parsed
}
