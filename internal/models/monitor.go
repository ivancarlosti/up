package models

import (
	"encoding/json"
	"time"
)

// Monitor is a probe definition. All the type specific options live inside the
// Config JSON column; the columns kept here are the ones the scheduler, the
// cluster and the statistics always need.
type Monitor struct {
	ID          uint        `gorm:"primaryKey" json:"id"`
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

	// RunOn: all | primary | node. It decides which cluster node executes the
	// heartbeat (a "node" value is paired with NodeID).
	RunOn  string `gorm:"size:20;not null;default:all" json:"run_on"`
	NodeID string `gorm:"size:64" json:"node_id"`

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

	Config MonitorConfig `gorm:"serializer:json;type:json" json:"config"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`

	// ---------------------------------------------------------------------
	// Runtime fields: computed per request by the services and never stored
	// in the monitors table (GORM ignores them).
	// ---------------------------------------------------------------------
	Status          AggregateStatus    `gorm:"-" json:"status"`
	LastCheckAt     *time.Time         `gorm:"-" json:"last_check_at"`
	LastLatencyMS   int64              `gorm:"-" json:"last_latency_ms"`
	Uptime24h       float64            `gorm:"-" json:"uptime_24h"`
	Uptime7d        float64            `gorm:"-" json:"uptime_7d"`
	Uptime30d       float64            `gorm:"-" json:"uptime_30d"`
	Heartbeats      []HeartbeatSummary `gorm:"-" json:"heartbeats,omitempty"`
	Votes           []NodeVote         `gorm:"-" json:"votes,omitempty"`
	NotificationIDs []uint             `gorm:"-" json:"notification_ids"`
	GroupIDs        []uint             `gorm:"-" json:"group_ids"`
	// Certificate is the last TLS certificate read by a probe (only when the
	// monitor watches its certificate).
	Certificate *CertificateInfo `gorm:"-" json:"certificate,omitempty"`
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
