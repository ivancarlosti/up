package services

import (
	"strings"
	"testing"

	"github.com/ivancarlosti/up/internal/models"
)

// TestBuildMonitorPayloadKeepsLocalIdentityOffTheWire pins the one rule that
// makes the protocol safe: a local id never travels. A receiver that received
// `"id":42` would either clash with one of its own rows or, worse, silently
// overwrite an unrelated one.
func TestBuildMonitorPayloadKeepsLocalIdentityOffTheWire(t *testing.T) {
	monitor := &models.Monitor{
		ID:              42,
		UUID:            "11111111-1111-4111-8111-111111111111",
		Name:            "api",
		Type:            models.MonitorTypeHTTP,
		Active:          true,
		IntervalSeconds: 60,
		TimeoutSeconds:  10,
		OriginNodeID:    "up-node-1",
		Revision:        3,
		NotificationIDs: []uint{7, 8},
		GroupIDs:        []uint{9},
		Status:          models.AggregateDown,
		Uptime24h:       99.5,
		Votes:           []models.NodeVote{{NodeID: "up-node-1", Online: true}},
		LastLatencyMS:   120,
		Config:          models.MonitorConfig{URL: "https://example.com/health"},
	}
	payload, err := BuildMonitorPayload(monitor, []string{"group-a"}, []string{"chan-a"})
	if err != nil {
		t.Fatalf("building the payload: %v", err)
	}
	wire := string(payload)

	for _, forbidden := range []string{
		`"id":42`,
		`"status":"down"`,
		`"votes"`,
		`"uptime_24h"`,
		`"last_latency_ms"`,
		`"group_ids":[9]`,
		`"notification_ids":[7,8]`,
	} {
		if strings.Contains(wire, forbidden) {
			t.Errorf("the payload must not carry %s:\n%s", forbidden, wire)
		}
	}

	for _, required := range []string{
		`"uuid":"11111111-1111-4111-8111-111111111111"`,
		`"revision":3`,
		`"origin_node_id":"up-node-1"`,
		`"group_uuids":["group-a"]`,
		`"notification_uuids":["chan-a"]`,
		`"url":"https://example.com/health"`,
	} {
		if !strings.Contains(wire, required) {
			t.Errorf("the payload must carry %s:\n%s", required, wire)
		}
	}
}

// TestBuildMonitorTemplatePayloadDropsLocalDefaultLinks is the same rule applied
// to the one model that stores local ids inside a JSON column: the template
// defaults hold the channel/group ids that get applied to a new monitor, and
// sending those ids to another node would point at unrelated rows there.
func TestBuildMonitorTemplatePayloadDropsLocalDefaultLinks(t *testing.T) {
	template := &models.MonitorTemplate{
		ID:   5,
		UUID: "22222222-2222-4222-8222-222222222222",
		Name: "standard",
		Type: models.MonitorTypeHTTP,
		Defaults: models.TemplateDefaults{
			IntervalSeconds: 60,
			RunOn:           "all",
			NotificationIDs: []uint{7, 8},
			GroupIDs:        []uint{9},
		},
	}
	payload, err := BuildMonitorTemplatePayload(template, []string{"group-a"}, []string{"chan-a"})
	if err != nil {
		t.Fatalf("building the payload: %v", err)
	}
	wire := string(payload)

	if strings.Contains(wire, `"notification_ids":[7,8]`) || strings.Contains(wire, `"group_ids":[9]`) {
		t.Errorf("the template defaults must not carry local link ids:\n%s", wire)
	}
	if !strings.Contains(wire, `"notification_uuids":["chan-a"]`) || !strings.Contains(wire, `"group_uuids":["group-a"]`) {
		t.Errorf("the template links must travel as uuids:\n%s", wire)
	}
}

// TestBuildNotificationPayloadCarriesTheSecrets guards the deliberate decision
// behind P2-1: the node that owns the send must hold the channel, so the
// credentials do travel (over TLS) and a future refactor must not quietly strip
// them, which would make the leader alert nobody.
func TestBuildNotificationPayloadCarriesTheSecrets(t *testing.T) {
	channel := &models.Notification{
		ID:   3,
		UUID: "33333333-3333-4333-8333-333333333333",
		Name: "ops",
		Type: models.NotificationWebhook,
		Config: models.NotificationConfig{
			Webhook: &models.WebhookConfig{URL: "https://hooks.example.com/secret"},
		},
	}
	payload, err := BuildNotificationPayload(channel, []string{"monitor-a"})
	if err != nil {
		t.Fatalf("building the payload: %v", err)
	}
	wire := string(payload)

	if strings.Contains(wire, `"id":3`) {
		t.Errorf("the channel payload must not carry a local id:\n%s", wire)
	}
	if !strings.Contains(wire, "hooks.example.com/secret") {
		t.Errorf("the channel payload must carry its configuration:\n%s", wire)
	}
	if !strings.Contains(wire, `"monitor_uuids":["monitor-a"]`) {
		t.Errorf("the channel links must travel as uuids:\n%s", wire)
	}
}
