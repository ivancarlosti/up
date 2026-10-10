package notify

import (
	"fmt"
	"strings"
	"text/template"
	"time"
)

// Text renders the default human readable body used by the SMTP channel.
func (m Message) Text() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n\n", m.Title)
	fmt.Fprintf(&b, "Monitor : %s (%s)\n", m.MonitorName, m.MonitorType)
	fmt.Fprintf(&b, "Target  : %s\n", m.MonitorURL)
	fmt.Fprintf(&b, "Status  : %s\n", strings.ToUpper(m.Status))
	if m.Message != "" {
		fmt.Fprintf(&b, "Detail  : %s\n", m.Message)
	}
	if tags := m.TagsCSV(); tags != "" {
		fmt.Fprintf(&b, "Tags    : %s\n", tags)
	}
	if group := m.MonitorGroup; group != "" {
		fmt.Fprintf(&b, "Group   : %s\n", group)
	}
	// The registry status of a domain reminder (RDAP only): it explains a lock
	// or an "inactive" registration next to the date.
	if statuses := m.DomainStatusCSV(); statuses != "" {
		fmt.Fprintf(&b, "Registry: %s\n", statuses)
	}
	fmt.Fprintf(&b, "Latency : %d ms\n", m.LatencyMS)
	if m.NodeID != "" {
		fmt.Fprintf(&b, "Node    : %s\n", m.NodeID)
	}
	fmt.Fprintf(&b, "Time    : %s\n", m.Timestamp.Format(time.RFC1123))
	if link := m.Link(); link != "" {
		// The specific page of the event: the monitor detail for a status alert,
		// the expiry worklist for a certificate/domain reminder.
		fmt.Fprintf(&b, "\nDashboard: %s\n", link)
	}
	return b.String()
}

// HTML renders the body used when the SMTP channel is configured as HTML.
func (m Message) HTML() string {
	var b strings.Builder
	b.WriteString(`<div style="font-family:system-ui,Segoe UI,Helvetica,Arial,sans-serif;font-size:14px;color:#111">`)
	fmt.Fprintf(&b, `<h2 style="margin:0 0 12px">%s</h2>`, template.HTMLEscapeString(m.Title))
	b.WriteString(`<table cellpadding="4" style="border-collapse:collapse">`)
	rows := [][2]string{
		{"Monitor", m.MonitorName},
		{"Type", m.MonitorType},
		{"Target", m.MonitorURL},
		{"Status", strings.ToUpper(m.Status)},
		{"Detail", m.Message},
		{"Tags", m.TagsCSV()},
		{"Group", m.MonitorGroup},
		{"Registry", m.DomainStatusCSV()},
		{"Latency", fmt.Sprintf("%d ms", m.LatencyMS)},
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
