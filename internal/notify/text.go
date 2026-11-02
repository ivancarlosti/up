package notify

import (
	"fmt"
	"strings"
	"text/template"
	"time"
)

// Text renders the default human readable body used by the SMTP channel.
func (m Message) Text() string {
	if m.TextBody != "" {
		return m.TextBody
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n\n", m.Title)
	// The first block is the subject of the alert: a machine or a monitor.
	// MetricLine is empty for a monitor, and the Type line is empty for a
	// machine (a box is not one of the six probe types).
	fmt.Fprintf(&b, "%-8s: %s\n", m.NameLabel(), m.Subject())
	if kind := m.KindLabel(); kind != "" {
		fmt.Fprintf(&b, "%-8s: %s\n", "Type", kind)
	}
	if target := m.TargetLabel(); target != "" {
		fmt.Fprintf(&b, "%-8s: %s\n", "Target", target)
	}
	if metric := m.MetricLine(); metric != "" {
		fmt.Fprintf(&b, "%-8s: %s\n", "Metric", metric)
	}
	fmt.Fprintf(&b, "%-8s: %s\n", "Status", strings.ToUpper(m.Status))
	if m.Message != "" {
		fmt.Fprintf(&b, "%-8s: %s\n", "Detail", m.Message)
	}
	if tags := m.TagsCSV(); tags != "" {
		fmt.Fprintf(&b, "%-8s: %s\n", "Tags", tags)
	}
	if group := m.MonitorGroup; group != "" {
		fmt.Fprintf(&b, "%-8s: %s\n", "Group", group)
	}
	// The registry status of a domain reminder (RDAP only): it explains a lock
	// or an "inactive" registration next to the date.
	if statuses := m.DomainStatusCSV(); statuses != "" {
		fmt.Fprintf(&b, "%-8s: %s\n", "Registry", statuses)
	}
	if m.LatencyMS > 0 {
		fmt.Fprintf(&b, "%-8s: %d ms\n", "Latency", m.LatencyMS)
	}
	if m.NodeID != "" {
		fmt.Fprintf(&b, "%-8s: %s\n", "Node", m.NodeID)
	}
	fmt.Fprintf(&b, "%-8s: %s\n", "Time", m.Timestamp.Format(time.RFC1123))
	if link := m.Link(); link != "" {
		// The specific page of the event: the monitor detail for a status alert,
		// the machine charts for a hardware alert, the expiry worklist for a
		// certificate/domain reminder.
		fmt.Fprintf(&b, "\nDashboard: %s\n", link)
	}
	return b.String()
}

// HTML renders the body used when the SMTP channel is configured as HTML.
func (m Message) HTML() string {
	if m.HTMLBody != "" {
		return m.HTMLBody
	}
	var b strings.Builder
	b.WriteString(`<div style="font-family:system-ui,Segoe UI,Helvetica,Arial,sans-serif;font-size:14px;color:#111">`)
	fmt.Fprintf(&b, `<h2 style="margin:0 0 12px">%s</h2>`, template.HTMLEscapeString(m.Title))
	b.WriteString(`<table cellpadding="4" style="border-collapse:collapse">`)
	rows := [][2]string{
		{m.NameLabel(), m.Subject()},
		{"Type", m.KindLabel()},
		{"Target", m.TargetLabel()},
		{"Metric", m.MetricLine()},
		{"Status", strings.ToUpper(m.Status)},
		{"Detail", m.Message},
		{"Tags", m.TagsCSV()},
		{"Group", m.MonitorGroup},
		{"Registry", m.DomainStatusCSV()},
		{"Latency", m.latencyLabel()},
		{"Node", m.NodeID},
		{"Time", m.Timestamp.Format(time.RFC1123)},
	}
	for _, row := range rows {
		if row[1] == "" {
			continue
		}
		fmt.Fprintf(&b, `<tr><td style="color:#666">%s</td><td><strong>%s</strong></td></tr>`,
			template.HTMLEscapeString(row[0]), template.HTMLEscapeString(row[1]))
	}
	b.WriteString(`</table>`)
	if link := m.Link(); link != "" {
		fmt.Fprintf(&b, `<p><a href="%s">%s</a></p>`,
			template.HTMLEscapeString(link), template.HTMLEscapeString(link))
	}
	b.WriteString(`</div>`)
	return b.String()
}

// Body returns the e-mail body honouring the HTML setting.
func (m Message) Body(useHTML bool) string {
	if useHTML {
		return m.HTML()
	}
	return m.Text()
}
