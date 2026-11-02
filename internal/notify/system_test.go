package notify

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/ivancarlosti/up/internal/models"
)

// machine is the system a hardware alert is about.
func machine() *models.System {
	return &models.System{ID: 7, Name: "web-01", Host: "10.0.0.7", Hostname: "web-01.local"}
}

// TestNewSystemMessagePinsTheTitleAndTheLink pins the parts a receiver routes on:
// the event label, the page the alert links to and the machine it names.
func TestNewSystemMessagePinsTheTitleAndTheLink(t *testing.T) {
	rule := models.SystemAlertRule{Metric: models.SystemAlertCPU, Enabled: true, Threshold: 80, MinMinutes: 5}
	msg := NewSystemMessage(models.EventSystemThreshold, machine(), rule, 92.4,
		"average 92.4% over the last 5 minutes (threshold 80%)", "up-node-1", "https://up.example/")

	if want := "[SYSTEM THRESHOLD] web-01"; msg.Title != want {
		t.Errorf("Title = %q, want %q", msg.Title, want)
	}
	if msg.Status != string(models.SystemStatusDown) {
		t.Errorf("Status = %q, want down for an alert", msg.Status)
	}
	if want := "https://up.example/systems/7"; msg.DashboardURL != want {
		t.Errorf("DashboardURL = %q, want %q", msg.DashboardURL, want)
	}
	if msg.Link() != msg.DashboardURL {
		t.Errorf("Link() = %q, want the dashboard url", msg.Link())
	}
	if !strings.Contains(msg.Text(), "System  : web-01") {
		t.Errorf("the text body does not name the machine:\n%s", msg.Text())
	}
	if !strings.Contains(msg.Text(), "Metric  : cpu 92.4% (threshold 80%, sustained 5 min)") {
		t.Errorf("the text body does not describe the rule:\n%s", msg.Text())
	}
	// A machine is not a probe: its body must not claim a monitor type.
	if strings.Contains(msg.Text(), "Type    : system") {
		t.Errorf("the text body prints a probe type for a machine:\n%s", msg.Text())
	}
}

// TestNewSystemMessageRecoveryIsUp pins the status of the recovery edges.
func TestNewSystemMessageRecoveryIsUp(t *testing.T) {
	rule := models.SystemAlertRule{Metric: models.SystemAlertDown, Enabled: true, MinMinutes: 5}
	for _, event := range []models.NotificationEvent{models.EventSystemUp, models.EventSystemRecovered} {
		msg := NewSystemMessage(event, machine(), rule, 0, "recovered", "up-node-1", "")
		if msg.Status != string(models.SystemStatusUp) {
			t.Errorf("%s: Status = %q, want up", event, msg.Status)
		}
		// No APP_URL: the link is empty, never a bare path.
		if msg.DashboardURL != "" || msg.Link() != "" {
			t.Errorf("%s: link = %q, want empty", event, msg.Link())
		}
	}
}

// TestSystemMessageWebhookPayload pins the extra fields of the JSON body and that
// a monitor event keeps its shape.
func TestSystemMessageWebhookPayload(t *testing.T) {
	rule := models.SystemAlertRule{Metric: models.SystemAlertDisk, Enabled: true, Threshold: 85, MinMinutes: 5}
	msg := NewSystemMessage(models.EventSystemThreshold, machine(), rule, 91, "over", "up-node-1", "")

	body, err := msg.RenderBody("")
	if err != nil {
		t.Fatalf("RenderBody: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		t.Fatalf("the default body is not JSON: %v (%s)", err, body)
	}
	if payload["system"] != "web-01" || payload["metric"] != "disk" {
		t.Errorf("payload = %v, want the system and the metric", payload)
	}
	if payload["value"] != 91.0 || payload["threshold"] != 85.0 {
		t.Errorf("payload values = %v/%v", payload["value"], payload["threshold"])
	}

	// The down rule has no percentage, so the payload must not invent one.
	down := NewSystemMessage(models.EventSystemDown, machine(),
		models.SystemAlertRule{Metric: models.SystemAlertDown, Enabled: true, MinMinutes: 5},
		0, "silent", "up-node-1", "")
	downBody, err := down.RenderBody("")
	if err != nil {
		t.Fatalf("RenderBody: %v", err)
	}
	var downPayload map[string]any
	if err := json.Unmarshal([]byte(downBody), &downPayload); err != nil {
		t.Fatalf("the default body is not JSON: %v", err)
	}
	if _, ok := downPayload["threshold"]; ok {
		t.Errorf("the down payload carries a threshold: %v", downPayload)
	}

	// A monitor message keeps exactly the shape it had: no hardware fields.
	monitor := &models.Monitor{ID: 3, Name: "API", Type: models.MonitorTypeHTTP}
	plain := NewMessage(models.EventDown, monitor, models.AggregateDown, "500", 12, "up-node-1", "", nil)
	plainBody, err := plain.RenderBody("")
	if err != nil {
		t.Fatalf("RenderBody: %v", err)
	}
	var plainPayload map[string]any
	if err := json.Unmarshal([]byte(plainBody), &plainPayload); err != nil {
		t.Fatalf("the default body is not JSON: %v", err)
	}
	for _, field := range []string{"system", "metric", "value", "threshold", "min_minutes"} {
		if _, ok := plainPayload[field]; ok {
			t.Errorf("a monitor payload carries %q: %v", field, plainPayload)
		}
	}
}

// TestSystemLink covers the shapes of the link: no instance, no machine, and the
// trailing slash of APP_URL.
func TestSystemLink(t *testing.T) {
	cases := []struct {
		id         uint
		base, want string
	}{
		{7, "https://up.example", "https://up.example/systems/7"},
		{7, "https://up.example/", "https://up.example/systems/7"},
		{7, "", ""},
		{0, "https://up.example", "https://up.example"},
	}
	for _, tc := range cases {
		if got := SystemLink(tc.id, tc.base); got != tc.want {
			t.Errorf("SystemLink(%d, %q) = %q, want %q", tc.id, tc.base, got, tc.want)
		}
	}
}

// TestSystemMessageTimestampIsNow keeps the clock field non-zero (a zero time
// renders 0001-01-01 in every body).
func TestSystemMessageTimestampIsNow(t *testing.T) {
	msg := NewSystemMessage(models.EventSystemDown, machine(),
		models.SystemAlertRule{Metric: models.SystemAlertDown}, 0, "", "", "")
	if time.Since(msg.Timestamp) > time.Minute {
		t.Errorf("Timestamp = %s, want now", msg.Timestamp)
	}
}
