package notify

import (
	"fmt"
	"strings"
	"time"

	"github.com/ivancarlosti/up/internal/models"
)

// BodyTemplateProblem reports why a webhook body template cannot be stored, or
// "" when it can. It is the save-time half of the delivery guard
// (checkWebhookBody): a template that cannot render a parseable payload is
// refused with a 400 that names the cause, instead of being stored and failing
// every delivery afterwards with the same sentence in the log.
//
// The template is rendered with webhookProbeMessage, whose every free-text field
// carries a quote, a backslash, a tab and a newline - the values a status event
// really produces (`keyword "hello" matched`, `dial tcp ...: connection refused`)
// and the ones that break a raw `"{{.Field}}"`. A template is therefore accepted
// only if it is valid JSON for a hostile value too, which is exactly the rule the
// send path enforces. A body that is not declared as JSON is not checked, and
// neither is an empty template: Normalize fills that one with the default.
func BodyTemplateProblem(contentType, bodyTemplate string) string {
	if strings.TrimSpace(bodyTemplate) == "" {
		return ""
	}
	body, err := webhookProbeMessage().RenderBody(bodyTemplate)
	if err != nil {
		return fmt.Sprintf("config.webhook.body_template is refused: %v "+
			"(the placeholders and the helpers are documented in docs/notifications.md)", err)
	}
	if err := checkWebhookBody(contentType, body); err != nil {
		return fmt.Sprintf("config.webhook.body_template is refused: %v "+
			"(it was rendered with a check detail that carries a quote, a backslash and a "+
			"newline, so every value has to go through the json helper)", err)
	}
	return ""
}

// webhookProbeMessage is the message BodyTemplateProblem renders a template with.
//
// Every field a template can reach is set - including the hardware ones, because
// a channel serves monitor and system alerts alike - and every free-text one
// carries the characters that used to break the payload: a double quote, a
// backslash, a tab and a newline. The timestamp is fixed so a failure is
// reproducible.
func webhookProbeMessage() Message {
	const hostile = "quo\"te\\back\tslash\nnewline"
	return Message{
		Event:              "down",
		Status:             "down",
		Title:              `[DOWN] ` + hostile,
		MonitorName:        hostile,
		MonitorType:        string(models.MonitorTypeKeyword),
		MonitorURL:         `https://api.example.com/health?q="x"`,
		MonitorDescription: hostile,
		MonitorTags:        []string{hostile, "web"},
		DomainStatuses:     []string{"client transfer prohibited", hostile},
		MonitorGroup:       hostile,
		MonitorGroupID:     3,
		Message:            `keyword "hello" matched` + "\n" + "(200 - OK)\tback\\slash",
		LatencyMS:          12,
		NodeID:             hostile,
		Timestamp:          time.Date(2026, 11, 6, 21, 4, 5, 0, time.UTC),
		InstanceURL:        "https://up.example.com",
		DashboardURL:       "https://up.example.com/monitors/7",
		SystemName:         hostile,
		SystemHost:         hostile,
		SystemMetric:       string(models.SystemAlertCPU),
		SystemValue:        91.5,
		SystemThreshold:    90,
		SystemMinMinutes:   5,
	}
}
