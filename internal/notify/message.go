// Package notify wraps the shoutrrr engine and turns an internal Message into
// the payload expected by the SMTP and Webhook channels.
//
// Design notes (see docs/notifications.md):
//   - SMTP is delivered through shoutrrr's "smtp" service.
//   - Webhook is delivered through shoutrrr's "generic" service. Up renders the
//     body with a Go text/template first and hands the result over as the raw
//     request body (shoutrrr sends params["message"] verbatim when no template
//     is configured), so operators get full control over the payload while the
//     transport (method, headers, TLS) stays inside shoutrrr.
package notify

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"text/template"
	"time"

	"github.com/ivancarlosti/up/internal/models"
)

// Message is the data available to notifiers and to the webhook body template.
type Message struct {
	// Event is "down", "up" or "test".
	Event string
	// Status is the aggregated status (up, down, degraded, pending, unknown).
	Status string
	// Title is a short human readable summary, e.g. "[DOWN] API Gateway".
	Title string
	// Monitor fields.
	MonitorName        string
	MonitorType        string
	MonitorURL         string
	MonitorDescription string
	// Message is the check result message ("200 - OK", "connection refused"...).
	Message string
	// LatencyMS is the latency of the heartbeat that triggered the event.
	LatencyMS int64
	// NodeID is the cluster node that produced the heartbeat.
	NodeID string
	// Timestamp is the moment of the event (UTC).
	Timestamp time.Time
	// InstanceURL is APP_URL, used to build links inside the notifications.
	InstanceURL string
}

// NewMessage builds the message of a status change.
func NewMessage(event models.NotificationEvent, monitor *models.Monitor, status models.AggregateStatus,
	detail string, latencyMS int64, nodeID, instanceURL string) Message {
	return Message{
		Event:              string(event),
		Status:             string(status),
		Title:              fmt.Sprintf("[%s] %s", strings.ToUpper(string(event)), monitor.Name),
		MonitorName:        monitor.Name,
		MonitorType:        string(monitor.Type),
		MonitorURL:         MonitorTarget(monitor),
		MonitorDescription: monitor.Description,
		Message:            detail,
		LatencyMS:          latencyMS,
		NodeID:             nodeID,
		Timestamp:          time.Now().UTC(),
		InstanceURL:        instanceURL,
	}
}

// MonitorTarget returns the probe target in a readable form.
func MonitorTarget(monitor *models.Monitor) string {
	cfg := monitor.Config
	switch monitor.Type {
	case models.MonitorTypeHTTP, models.MonitorTypeKeyword:
		return cfg.URL
	case models.MonitorTypeTCP:
		return fmt.Sprintf("%s:%d", cfg.Host, cfg.Port)
	case models.MonitorTypeDNS:
		return fmt.Sprintf("%s %s @%s", cfg.RecordType, cfg.Hostname, cfg.ResolverServer)
	}
	return string(monitor.Type)
}

// RenderBody executes the operator provided webhook body template. An empty
// template falls back to a compact JSON payload.
func (m Message) RenderBody(bodyTemplate string) (string, error) {
	if strings.TrimSpace(bodyTemplate) == "" {
		encoded, err := json.Marshal(m.jsonPayload())
		if err != nil {
			return "", err
		}
		return string(encoded), nil
	}

	tpl, err := template.New("webhook").Option("missingkey=zero").Funcs(templateFuncs()).Parse(bodyTemplate)
	if err != nil {
		return "", fmt.Errorf("parsing the webhook body template: %w", err)
	}
	var buf bytes.Buffer
	if err := tpl.Execute(&buf, m); err != nil {
		return "", fmt.Errorf("rendering the webhook body template: %w", err)
	}
	return buf.String(), nil
}

// jsonPayload is the default webhook body.
func (m Message) jsonPayload() map[string]any {
	return map[string]any{
		"event":        m.Event,
		"monitor":      m.MonitorName,
		"type":         m.MonitorType,
		"target":       m.MonitorURL,
		"status":       m.Status,
		"message":      m.Message,
		"latency_ms":   m.LatencyMS,
		"node":         m.NodeID,
		"timestamp":    m.Timestamp.Format(time.RFC3339),
		"instance_url": m.InstanceURL,
	}
}

// templateFuncs are the helpers available inside a webhook body template.
func templateFuncs() template.FuncMap {
	return template.FuncMap{
		"json": func(value any) string {
			encoded, err := json.Marshal(value)
			if err != nil {
				return ""
			}
			return string(encoded)
		},
		"urlquery": url.QueryEscape,
		"upper":    strings.ToUpper,
		"lower":    strings.ToLower,
		"trim":     strings.TrimSpace,
		"default": func(fallback, value string) string {
			if strings.TrimSpace(value) == "" {
				return fallback
			}
			return value
		},
	}
}
