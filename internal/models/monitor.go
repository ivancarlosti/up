package models

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// MonitorRunsOn reports whether a node takes part in a monitor.
//
// It answers BOTH questions the cluster asks about `run_on`: which node probes the
// monitor (the scheduler) and which nodes vote for it (the evaluator). One function, so
// the two cannot drift — a node that probes a monitor nobody counts, or a node asked to
// vote on a monitor it never saw, would each look like a monitor going silently unknown.
//
// `isPrimary` is the caller's answer to "is this node the primary?", which is a shared row
// in shared mode and a derived role in federated mode: the rule does not care which.
func MonitorRunsOn(monitor *Monitor, nodeID string, isPrimary bool) bool {
	if monitor == nil {
		return false
	}
	switch monitor.RunOn {
	case "primary":
		return isPrimary
	case "node":
		return monitor.NodeID == nodeID
	case "some":
		return MonitorListedOnNode(monitor.RunOnNodes, nodeID)
	default:
		// "all", and anything a future build writes that this one does not know: taking
		// part is the safe default, because a monitor nobody probes is a monitor that
		// never alerts.
		return true
	}
}

// MonitorListedOnNode reports whether a node id appears in a comma separated list.
//
// The comparison is exact per element: a substring search would match "up-node-1" inside
// "up-node-11" and quietly start probing — and alerting — from the wrong node.
func MonitorListedOnNode(list, nodeID string) bool {
	if strings.TrimSpace(nodeID) == "" {
		return false
	}
	for _, candidate := range strings.Split(list, ",") {
		if strings.TrimSpace(candidate) == nodeID {
			return true
		}
	}
	return false
}

// NormalizeRunOnNodes cleans a node list: trimmed, deduplicated, empty entries dropped,
// in the order the operator wrote them.
func NormalizeRunOnNodes(list string) string {
	seen := map[string]bool{}
	out := make([]string, 0, 4)
	for _, candidate := range strings.Split(list, ",") {
		trimmed := strings.TrimSpace(candidate)
		if trimmed == "" || seen[trimmed] {
			continue
		}
		seen[trimmed] = true
		out = append(out, trimmed)
	}
	return strings.Join(out, ",")
}

// Monitor is a probe definition. All the type specific options live inside the
// Config JSON column; the columns kept here are the ones the scheduler, the
// cluster and the statistics always need.
type Monitor struct {
	ID uint `gorm:"primaryKey" json:"id"`

	// --- Sync identity (federated clustering) -----------------------------
	//
	// UUID is the global identity of the row: the auto-increment ID is only
	// meaningful inside one database, while the synchronisation protocol
	// addresses rows by uuid (docs/clustering-modes.md).
	//
	// The column is NULLABLE on purpose. AutoMigrate adds it to a table that
	// already has rows, and MySQL refuses a unique index over several empty
	// strings: every pre-existing row would hold "". NULL values are not
	// compared by a unique index, so the migration succeeds and
	// database.Backfill fills the column right after it.
	UUID string `gorm:"size:36;uniqueIndex" json:"uuid"`
	// OriginNodeID is the node that created the row. It is kept even after the
	// row is edited elsewhere, and it stays empty on a single node installation
	// that predates federated mode.
	OriginNodeID string `gorm:"size:64" json:"origin_node_id"`
	// Revision is incremented by the node that performs an edit. It is the
	// primary component of the last-writer-wins merge order, so a wrong clock
	// cannot make an old edit win.
	Revision int64 `gorm:"not null;default:1" json:"revision"`

	Name        string      `gorm:"size:200;not null" json:"name"`
	Type        MonitorType `gorm:"size:20;not null;index" json:"type"`
	Active      bool        `gorm:"not null;default:true" json:"active"`
	Description string      `gorm:"size:500" json:"description"`

	// Scheduling
	IntervalSeconds        int `gorm:"not null;default:60" json:"interval_seconds"`
	Retries                int `gorm:"not null;default:0" json:"retries"`
	RetriesIntervalSeconds int `gorm:"not null;default:60" json:"retries_interval_seconds"`
	TimeoutSeconds         int `gorm:"not null;default:10" json:"timeout_seconds"`
	// ResendIntervalSeconds: 0 means "notify only on transitions", a positive
	// value re-notifies while the monitor stays in the same status.
	ResendIntervalSeconds int `gorm:"not null;default:0" json:"resend_interval_seconds"`

	// UpsideDown inverts the meaning of the probe (an unreachable target is
	// considered up, e.g. a firewall rule that must keep blocking).
	UpsideDown bool `gorm:"not null;default:false" json:"upside_down"`

	// RunOn: all | primary | node | some. It decides which cluster nodes take part in
	// the monitor: every node, the current primary, one named node ("node" is paired
	// with NodeID), or the subset listed in RunOnNodes.
	RunOn  string `gorm:"size:20;not null;default:all" json:"run_on"`
	NodeID string `gorm:"size:64" json:"node_id"`
	// RunOnNodes is the subset used by run_on=some: a comma separated list of node ids,
	// like Tags. It is deliberately a string and not a join table: the list is small,
	// it travels as one field on the wire, and a node computes membership with a
	// substring-free comparison (see MonitorRunsOn).
	RunOnNodes string `gorm:"size:500" json:"run_on_nodes"`

	// Free form tags, comma separated (used for grouping and filters).
	Tags string `gorm:"size:255" json:"tags"`

	// CertWatch turns on the TLS certificate capture for this monitor (http and
	// keyword monitors capture the certificate of their https target, a ssl
	// monitor is nothing but that).
	CertWatch bool `gorm:"not null;default:false" json:"cert_watch"`
	// CertNotify allows the certificate events to reach the notification
	// channels: a monitor can show its certificate without filling the inbox.
	CertNotify bool `gorm:"not null;default:false" json:"cert_notify"`
	// CertWarnDays is the free form list of "days before expiry" that trigger a
	// reminder ("7,6,5,30"); empty means models.DefaultCertWarnDays.
	CertWarnDays string `gorm:"size:120" json:"cert_warn_days"`

	// DomainWatch turns on the registry expiration watch for the registrable
	// domain of this monitor's target (the domain counterpart of CertWatch).
	DomainWatch bool `gorm:"not null;default:false" json:"domain_watch"`
	// DomainNotify allows the domain events to reach the notification channels.
	DomainNotify bool `gorm:"not null;default:false" json:"domain_notify"`
	// DomainWarnDays is the free form list of "days before expiry" for the
	// domain; empty means models.DefaultDomainWarnDays.
	DomainWarnDays string `gorm:"size:120" json:"domain_warn_days"`
	// DomainExpiresAt is the manually typed expiration date. When set it wins
	// over RDAP and WHOIS: some TLDs simply do not publish the date, and the
	// operator still wants the same reminders.
	DomainExpiresAt *time.Time `json:"domain_expires_at"`

	// TemplateUUID is the template this monitor follows (empty when it follows
	// none). The uuid is used instead of the local id because the row is
	// synchronised: an auto-increment id is only meaningful inside one database.
	TemplateUUID string `gorm:"size:36;index" json:"template_uuid"`

	Config MonitorConfig `gorm:"serializer:json;type:json" json:"config"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`

	// ---------------------------------------------------------------------
	// Runtime fields: computed per request by the services and never stored
	// in the monitors table (GORM ignores them).
	// ---------------------------------------------------------------------
	Status        AggregateStatus `gorm:"-" json:"status"`
	LastCheckAt   *time.Time      `gorm:"-" json:"last_check_at"`
	LastLatencyMS int64           `gorm:"-" json:"last_latency_ms"`
	Uptime24h     float64         `gorm:"-" json:"uptime_24h"`
	Uptime7d      float64         `gorm:"-" json:"uptime_7d"`
	Uptime30d     float64         `gorm:"-" json:"uptime_30d"`
	// Uptime and UptimeHours are the percentage over the configured window
	// (Admin > Settings, or the per status page override) and the window itself:
	// they are what the dashboard, the monitors table and the status pages read.
	// The fixed 24h/7d/30d fields above stay for the public API.
	Uptime      float64            `gorm:"-" json:"uptime"`
	UptimeHours int                `gorm:"-" json:"uptime_hours"`
	Heartbeats  []HeartbeatSummary `gorm:"-" json:"heartbeats,omitempty"`
	// HeartbeatBars is the compact, bucketed history drawn in the monitors
	// table: one status per slot, oldest first, "" for a slot without data.
	HeartbeatBars   []string   `gorm:"-" json:"heartbeat_bars,omitempty"`
	Votes           []NodeVote `gorm:"-" json:"votes,omitempty"`
	NotificationIDs []uint     `gorm:"-" json:"notification_ids"`
	GroupIDs        []uint     `gorm:"-" json:"group_ids"`
	// Certificate is the last TLS certificate read by a probe (only when the
	// monitor watches its certificate).
	Certificate *CertificateInfo `gorm:"-" json:"certificate,omitempty"`
	// Domain is the last registry expiration read for the monitor's registrable
	// domain (only when the monitor watches its domain).
	Domain *DomainInfo `gorm:"-" json:"domain,omitempty"`
	// TemplateName is the name of the template the monitor follows (filled by the
	// decoration, never stored).
	TemplateName string `gorm:"-" json:"template_name,omitempty"`
}

// BeforeCreate fills the sync identity of a new row: the UUID is the global id
// and the revision starts at 1 (the pattern MonitorGroup and MonitorTemplate
// already use).
func (m *Monitor) BeforeCreate(tx *gorm.DB) error {
	if m.UUID == "" {
		m.UUID = uuid.NewString()
	}
	if m.Revision == 0 {
		m.Revision = 1
	}
	return nil
}

// TagList returns the comma separated tags as a slice.
func (m *Monitor) TagList() []string {
	return SplitList(m.Tags)
}

// HeartbeatSummary is a trimmed heartbeat used by the dashboard bars.
type HeartbeatSummary struct {
	Status    HeartbeatStatus `json:"-"` // exposed as a string, see MarshalJSON
	LatencyMS int64           `json:"latency_ms"`
	CreatedAt time.Time       `json:"created_at"`
	NodeID    string          `json:"node_id,omitempty"`
}

// MarshalJSON mirrors Heartbeat.MarshalJSON: the database stores the numeric
// status but every consumer of the API (the dashboard bars, the public status
// pages) expects the readable name. Without this the bars received 0/1 and
// painted every slot as "unknown".
func (h HeartbeatSummary) MarshalJSON() ([]byte, error) {
	type summaryAlias HeartbeatSummary
	return json.Marshal(struct {
		summaryAlias
		Status string `json:"status"`
	}{
		summaryAlias: summaryAlias(h),
		Status:       h.Status.String(),
	})
}

// NodeVote is the latest opinion of one cluster node about a monitor.
type NodeVote struct {
	NodeID    string          `json:"node_id"`
	NodeName  string          `json:"node_name"`
	Status    AggregateStatus `json:"status"`
	LatencyMS int64           `json:"latency_ms"`
	Message   string          `json:"message"`
	CheckedAt *time.Time      `json:"checked_at"`
	Online    bool            `json:"online"`
}

// MonitorNotification links a monitor to a notification channel.
type MonitorNotification struct {
	MonitorID      uint `gorm:"primaryKey" json:"monitor_id"`
	NotificationID uint `gorm:"primaryKey" json:"notification_id"`
	OnDown         bool `gorm:"not null;default:true" json:"on_down"`
	OnUp           bool `gorm:"not null;default:true" json:"on_up"`
}

// MonitorState stores the aggregated status of a monitor as seen by the whole
// cluster. It is what makes transition detection (and therefore notification
// de-duplication) work across nodes, since every node reads and writes the
// same row.
type MonitorState struct {
	MonitorID   uint            `gorm:"primaryKey" json:"monitor_id"`
	Status      AggregateStatus `gorm:"size:20;not null;default:unknown" json:"status"`
	ChangedAt   time.Time       `json:"changed_at"`
	Notified    AggregateStatus `gorm:"size:20" json:"notified"`
	NotifiedAt  *time.Time      `json:"notified_at"`
	LastMessage string          `gorm:"size:500" json:"last_message"`
	LastLatency int64           `json:"last_latency"`
	LastCheckAt *time.Time      `json:"last_check_at"`
	UpdatedAt   time.Time       `json:"updated_at"`
}
