// Package notify wraps the shoutrrr engine and turns an internal Message into
// the payload expected by the SMTP, Webhook, Slack, Discord and Telegram
// channels.
//
// Design notes (see docs/notifications.md):
//   - SMTP is delivered through shoutrrr's "smtp" service.
//   - Webhook is delivered through shoutrrr's "generic" service. Up renders the
//     body with a Go text/template first and hands the result over as the raw
//     request body (shoutrrr sends params["message"] verbatim when no template
//     is configured), so operators get full control over the payload while the
//     transport (method, headers, TLS) stays inside shoutrrr.
//   - Slack, Discord and Telegram are delivered through their shoutrrr services
//     using the same plain text body as SMTP (Message.Text); the message title
//     travels in shoutrrr's "title" parameter.
package notify

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
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
	// MonitorTags is the monitor's tag list, exposed as an array (use
	// {{json .MonitorTags}} or {{range .MonitorTags}}). TagsCSV is the same
	// data as one comma separated string.
	MonitorTags []string
	// MonitorGroup is the name of the single group the monitor belongs to, and
	// MonitorGroupID is its id; both are empty when the monitor belongs to no
	// group.
	MonitorGroup   string
	MonitorGroupID uint
	// MonitorGroups is DEPRECATED: it carries the same group as a zero or one
	// element array, which is what a body template written before the single
	// group rule reads ({{range .MonitorGroups}}). GroupsCSV is the same data as
	// one comma separated string, and it stays the flat form operators already
	// have in their templates. Both go away with the next release.
	MonitorGroups []string
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
	// DashboardURL is the page of the instance the notification is about: the
	// detail page of the monitor for a status event (/monitors/<id>), or the
	// expiry worklist for a certificate/domain reminder (/admin/expiry). It is
	// empty when APP_URL is not configured; InstanceURL stays the instance root.
	DashboardURL string
}

// SPA routes the notifications link to. They mirror web/src/router/index.ts: the
// monitor detail route and the expiry worklist that lists every deduplicated
// certificate/domain target.
const (
	monitorsPathPrefix = "/monitors/"
	// ExpiryPath is the page that shows the certificates and the domains Up
	// watches, with the monitors that share each of them.
	ExpiryPath = "/admin/expiry"
)

// NewMessage builds the message of a status change.
//
// group is the single group the monitor belongs to (nil when it belongs to none),
// resolved by the caller because a Monitor carries no group name by itself on the
// notification path; it is passed here so every delivery path ends up with the same
// populated payload.
func NewMessage(event models.NotificationEvent, monitor *models.Monitor, status models.AggregateStatus,
	detail string, latencyMS int64, nodeID, instanceURL string, group *models.MonitorGroupRef) Message {
	// The deprecated array form is derived from the single group, and it stays an
	// EMPTY array when there is no group: a nil slice would render as null through
	// {{json .MonitorGroups}}, which reads like a bug on the receiving side.
	groupName := ""
	groupID := uint(0)
	groupNames := []string{}
	if group != nil {
		groupID = group.ID
		groupName = group.Name
		if groupName != "" {
			groupNames = []string{groupName}
		}
	}
	return Message{
		Event:              string(event),
		Status:             string(status),
		Title:              fmt.Sprintf("[%s] %s", strings.ToUpper(string(event)), monitor.Name),
		MonitorName:        monitor.Name,
		MonitorType:        string(monitor.Type),
		MonitorURL:         MonitorTarget(monitor),
		MonitorDescription: monitor.Description,
		MonitorTags:        monitor.TagList(),
		MonitorGroup:       groupName,
		MonitorGroupID:     groupID,
		MonitorGroups:      groupNames,
		Message:            detail,
		LatencyMS:          latencyMS,
		NodeID:             nodeID,
		Timestamp:          time.Now().UTC(),
		InstanceURL:        instanceURL,
		DashboardURL:       DashboardLink(event, monitor.ID, instanceURL),
	}
}

// DashboardLink returns the page of the instance that shows what a notification
// is about, so an alert links to its own subject instead of the instance root:
//
//   - a status event (down, up) links to the detail page of its monitor
//     (/monitors/<id>), which is where the statistics, the event log and the
//     heartbeats of the alerting monitor live whatever its type is (http, dns,
//     tcp, ...);
//   - a certificate or domain reminder links to the expiry worklist
//     (/admin/expiry), because the notification is about a host or a domain and
//     not about one monitor: the worklist deduplicates the targets, so the same
//     certificate can back many monitors and no single detail page shows them
//     all;
//   - a test message is rendered for a sample monitor that is not stored (id 0),
//     so it keeps the dashboard root - /monitors/0 would be a dead link.
//
// instanceURL is APP_URL; its trailing slash, if any, is tolerated. An empty
// instanceURL yields an empty link rather than a path a receiver could not
// resolve.
func DashboardLink(event models.NotificationEvent, monitorID uint, instanceURL string) string {
	base := strings.TrimRight(strings.TrimSpace(instanceURL), "/")
	if base == "" {
		return ""
	}
	switch event {
	case models.EventCertExpiring, models.EventCertExpired,
		models.EventDomainExpiring, models.EventDomainExpired:
		return base + ExpiryPath
	case models.EventTest:
		return base
	}
	if monitorID == 0 {
		return base
	}
	return base + monitorsPathPrefix + strconv.FormatUint(uint64(monitorID), 10)
}

// Link is the URL a notification should print: the specific page of the event
// when it could be built, the instance root otherwise. It is empty when APP_URL
// is not configured.
func (m Message) Link() string {
	if m.DashboardURL != "" {
		return m.DashboardURL
	}
	return m.InstanceURL
}

// TagsCSV is the monitor's tags as one comma separated string, the flat form of
// MonitorTags ({{.TagsCSV}} in a body template).
func (m Message) TagsCSV() string {
	return joinCSV(m.MonitorTags)
}

// GroupsCSV is the monitor's groups as one comma separated string, the flat form
// of MonitorGroups ({{.GroupsCSV}} in a body template).
func (m Message) GroupsCSV() string {
	return joinCSV(m.MonitorGroups)
}

// joinCSV trims and drops the empty values but keeps the caller's order, so the
// flat form lists tags and groups exactly like the array form does. It does not
// use models.JoinList on purpose: that helper sorts its items, which would lose
// the group sort order and make the two forms disagree.
func joinCSV(items []string) string {
	cleaned := make([]string, 0, len(items))
	for _, item := range items {
		if value := strings.TrimSpace(item); value != "" {
			cleaned = append(cleaned, value)
		}
	}
	return strings.Join(cleaned, ",")
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
	case models.MonitorTypeSSL:
		port := cfg.Port
		if port <= 0 {
			port = models.DefaultSSLPort
		}
		return fmt.Sprintf("%s:%d", cfg.Host, port)
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

// jsonPayload is the default webhook body. Tags and groups travel both as
// arrays (what a consumer parses) and as comma separated strings (what a human
// reads in a log line).
func (m Message) jsonPayload() map[string]any {
	return map[string]any{
		"event":   m.Event,
		"monitor": m.MonitorName,
		"type":    m.MonitorType,
		"target":  m.MonitorURL,
		"status":  m.Status,
		"message": m.Message,
		// group and group_id are the single group of the monitor ("" and 0 when
		// it belongs to none). groups and groups_csv are DEPRECATED mirrors of
		// the same value: they keep a body written before the single group rule
		// working, and they go away with the next release.
		"group":        m.MonitorGroup,
		"group_id":     m.MonitorGroupID,
		"tags":         m.MonitorTags,
		"groups":       m.MonitorGroups,
		"tags_csv":     m.TagsCSV(),
		"groups_csv":   m.GroupsCSV(),
		"latency_ms":   m.LatencyMS,
		"node":         m.NodeID,
		"timestamp":    m.Timestamp.Format(time.RFC3339),
		"instance_url": m.InstanceURL,
		// dashboard_url is the page of the instance the alert is about: the
		// monitor detail for a status event, the expiry worklist for a
		// certificate/domain reminder. Empty when APP_URL is not configured.
		"dashboard_url": m.DashboardURL,
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
