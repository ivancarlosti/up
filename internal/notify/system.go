package notify

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/ivancarlosti/up/internal/models"
)

// NewSystemMessage builds the message of a hardware alert.
//
// It is a sibling of NewMessage rather than a variant of it: a machine has no
// type, no tags and no probe target, and it carries the metric that crossed its
// threshold instead. The monitor fields are filled anyway, because the default
// webhook payload and the bodies keep their shape whatever the event is.
func NewSystemMessage(event models.NotificationEvent, system *models.System, rule models.SystemAlertRule,
	value float64, detail, nodeID, instanceURL string) Message {

	status := models.SystemStatusDown
	if event == models.EventSystemUp || event == models.EventSystemRecovered {
		status = models.SystemStatusUp
	}
	host := strings.TrimSpace(system.Host)
	if host == "" {
		host = strings.TrimSpace(system.Hostname)
	}
	return Message{
		Event:            string(event),
		Status:           string(status),
		Title:            fmt.Sprintf("[%s] %s", SystemEventLabel(event), system.Name),
		MonitorName:      system.Name,
		MonitorType:      "system",
		MonitorURL:       host,
		Message:          detail,
		NodeID:           nodeID,
		Timestamp:        time.Now().UTC(),
		InstanceURL:      instanceURL,
		DashboardURL:     SystemLink(system.ID, instanceURL),
		SystemName:       system.Name,
		SystemHost:       host,
		SystemMetric:     string(rule.Metric),
		SystemValue:      value,
		SystemThreshold:  rule.Threshold,
		SystemMinMinutes: rule.MinMinutes,
	}
}

// SystemEventLabel is the human form of a hardware event, used in the title:
// system_threshold becomes "SYSTEM THRESHOLD".
func SystemEventLabel(event models.NotificationEvent) string {
	return strings.ToUpper(strings.ReplaceAll(string(event), "_", " "))
}

// SystemLink returns the page of the instance that charts a machine
// (/systems/<id>). It is empty when APP_URL is not configured, and the instance
// root for the test message, which names no stored machine.
func SystemLink(systemID uint, instanceURL string) string {
	base := strings.TrimRight(strings.TrimSpace(instanceURL), "/")
	if base == "" {
		return ""
	}
	if systemID == 0 {
		return base
	}
	return base + systemsPathPrefix + strconv.FormatUint(uint64(systemID), 10)
}

// isSystem reports whether the message is about a machine.
func (m Message) isSystem() bool { return m.SystemName != "" }

// NameLabel is the label of the first line of the bodies: "System" for a
// hardware alert, "Monitor" for a probe (and for the test message).
func (m Message) NameLabel() string {
	if m.isSystem() {
		return "System"
	}
	return "Monitor"
}

// Subject is the name the alert is about.
func (m Message) Subject() string {
	if m.isSystem() {
		return m.SystemName
	}
	return m.MonitorName
}

// KindLabel is the second line: the probe type of a monitor, and nothing for a
// machine (a box is not http, tcp or dns).
func (m Message) KindLabel() string {
	if m.isSystem() {
		return ""
	}
	return m.MonitorType
}

// TargetLabel is where the subject lives: the probe target of a monitor, the
// host notes (or the hostname) of a machine.
func (m Message) TargetLabel() string {
	if m.isSystem() {
		return m.SystemHost
	}
	return m.MonitorURL
}

// MetricLine describes the rule of a hardware alert:
//
//	down  - "the machine stopped reporting"
//	cpu   - "cpu 92.4% (threshold 80%, sustained 5 min)"
//
// It is empty for every monitor event, which has no metric.
func (m Message) MetricLine() string {
	if !m.isSystem() {
		return ""
	}
	switch m.SystemMetric {
	case string(models.SystemAlertDown):
		if m.Event == string(models.EventSystemDown) {
			return "the machine stopped reporting"
		}
		return "the machine is reporting again"
	case "":
		return ""
	}
	line := fmt.Sprintf("%s %.1f%% (threshold %.0f%%", m.SystemMetric, m.SystemValue, m.SystemThreshold)
	if m.SystemMinMinutes > 0 {
		line += fmt.Sprintf(", sustained %d min", m.SystemMinMinutes)
	}
	return line + ")"
}

// latencyLabel is the latency line of the HTML body (empty when the event
// carries none, which is the case of every hardware alert).
func (m Message) latencyLabel() string {
	if m.LatencyMS <= 0 {
		return ""
	}
	return fmt.Sprintf("%d ms", m.LatencyMS)
}
