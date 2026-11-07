package database

import (
	"encoding/json"
	"testing"

	"github.com/ivancarlosti/up/internal/models"
)

// storedLegacyBodyTemplate is the block the release before the json helper shipped
// as models.DefaultWebhookBodyTemplate: it is the one that stored the reported
// failure, and a row written by that release still carries it verbatim (Normalize
// only fills an EMPTY template). It is spelled out here rather than read from
// models so this test fails if the upgrade stops recognising it.
const storedLegacyBodyTemplate = `{
  "event": "{{.Event}}",
  "monitor": "{{.MonitorName}}",
  "type": "{{.MonitorType}}",
  "target": "{{.MonitorURL}}",
  "dashboard_url": "{{.DashboardURL}}",
  "status": "{{.Status}}",
  "message": "{{.Message}}",
  "domain_statuses": {{json .DomainStatuses}},
  "domain_statuses_csv": "{{.DomainStatusCSV}}",
  "tags": {{json .MonitorTags}},
  "group": "{{.MonitorGroup}}",
  "group_id": {{.MonitorGroupID}},
  "latency_ms": {{.LatencyMS}},
  "node": "{{.NodeID}}",
  "timestamp": "{{.Timestamp}}"
}`

// webhookConfigJSON encodes the config column of a channel the way the write path
// stores it, so the pass sees exactly what MySQL hands back.
func webhookConfigJSON(t *testing.T, template string) []byte {
	t.Helper()
	encoded, err := json.Marshal(models.NotificationConfig{Webhook: &models.WebhookConfig{
		URL:          "https://n8n.example.com/webhook/up",
		Method:       "POST",
		ContentType:  "application/json",
		BodyTemplate: template,
		Headers:      []models.Header{{Key: "X-Auth", Value: "s3cret"}},
	}})
	if err != nil {
		t.Fatalf("encoding the stored configuration: %v", err)
	}
	return encoded
}

// TestUpgradeWebhookBody pins the rule of the boot pass: a stored shipped default
// is replaced with the escaped block, while a custom template, another channel
// type and an empty column are left alone (idempotency: the second pass has
// nothing to do, which is what makes the pass safe to run on every boot).
func TestUpgradeWebhookBody(t *testing.T) {
	stored := webhookConfigJSON(t, storedLegacyBodyTemplate)
	upgraded, changed, err := upgradeWebhookBody(stored)
	if err != nil {
		t.Fatalf("upgradeWebhookBody: %v", err)
	}
	if !changed {
		t.Fatal("a stored shipped default has to be upgraded")
	}
	var config models.NotificationConfig
	if err := json.Unmarshal(upgraded, &config); err != nil {
		t.Fatalf("the upgraded configuration is not valid JSON: %v", err)
	}
	if config.Webhook == nil || config.Webhook.BodyTemplate != models.DefaultWebhookBodyTemplate {
		t.Fatalf("the template was not replaced by the escaped block: %+v", config.Webhook)
	}
	// The rest of the channel survives the round trip: a pass that dropped the
	// URL or a header would silence the alert it is trying to repair.
	if config.Webhook.URL != "https://n8n.example.com/webhook/up" || config.Webhook.Method != "POST" ||
		len(config.Webhook.Headers) != 1 || config.Webhook.Headers[0].Value != "s3cret" {
		t.Fatalf("the rest of the configuration has to survive: %+v", config.Webhook)
	}

	// A second pass finds nothing left to do, and a row it has nothing to say
	// about comes back byte for byte.
	for name, input := range map[string][]byte{
		"the upgraded row":     upgraded,
		"the current default":  webhookConfigJSON(t, models.DefaultWebhookBodyTemplate),
		"a custom template":    webhookConfigJSON(t, `{"message": {{json .Message}}, "note": "{{.MonitorName}}"}`),
		"another channel type": []byte(`{"smtp":{"host":"smtp.example.com","port":587}}`),
		"no webhook block":     []byte(`{}`),
	} {
		next, changed, err := upgradeWebhookBody(input)
		if err != nil {
			t.Fatalf("%s: upgradeWebhookBody: %v", name, err)
		}
		if changed {
			t.Errorf("%s was rewritten, it has to be left alone", name)
		}
		if string(next) != string(input) {
			t.Errorf("%s has to come back byte for byte, got:\n%s", name, next)
		}
	}

	if next, changed, err := upgradeWebhookBody(nil); err != nil || changed || len(next) != 0 {
		t.Errorf("an empty config column must be left alone: (%q, %v, %v)", next, changed, err)
	}
	if _, _, err := upgradeWebhookBody([]byte("{not json")); err == nil {
		t.Error("a corrupt config column has to be reported, not ignored")
	}
}
