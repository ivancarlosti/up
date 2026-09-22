package models

import "time"

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
}

// TagList returns the comma separated tags as a slice.
func (m *Monitor) TagList() []string {
	return SplitList(m.Tags)
}

// HeartbeatSummary is a trimmed heartbeat used by the dashboard bars.
type HeartbeatSummary struct {
	Status    HeartbeatStatus `json:"status"`
	LatencyMS int64           `json:"latency_ms"`
	CreatedAt time.Time       `json:"created_at"`
	NodeID    string          `json:"node_id,omitempty"`
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
