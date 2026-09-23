package models

import (
	"encoding/json"
	"time"
)

// Heartbeat is a single check result produced by one node.
type Heartbeat struct {
	ID        uint `gorm:"primaryKey" json:"id"`
	MonitorID uint `gorm:"not null;index:idx_hb_monitor_time,priority:1;index:idx_hb_monitor_node,priority:1" json:"monitor_id"`
	// NodeID identifies which cluster node produced the heartbeat. It is the
	// same value as the NODE_ID environment variable.
	NodeID string `gorm:"size:64;index:idx_hb_monitor_node,priority:2" json:"node_id"`

	// Status is persisted as an integer, but exposed as a lower case string:
	// see MarshalJSON below.
	Status HeartbeatStatus `gorm:"not null;index" json:"-"`

	LatencyMS  int64  `json:"latency_ms"`
	StatusCode int    `json:"status_code"`
	Message    string `gorm:"size:500" json:"message"`
	// Important is false while a monitor is still retrying, so notifications
	// are only triggered once the retries are exhausted.
	Important bool `gorm:"not null;default:false" json:"important"`

	CreatedAt time.Time `gorm:"index:idx_hb_monitor_time,priority:2;index:idx_hb_monitor_node,priority:3" json:"created_at"`
}

// MarshalJSON keeps the integer status in the database while exposing the
// readable name to every API consumer.
func (h Heartbeat) MarshalJSON() ([]byte, error) {
	type heartbeatAlias Heartbeat
	return json.Marshal(struct {
		heartbeatAlias
		Status string `json:"status"`
	}{
		heartbeatAlias: heartbeatAlias(h),
		Status:         h.Status.String(),
	})
}

// UptimeStats is the aggregation returned by the statistics endpoints.
type UptimeStats struct {
	MonitorID uint    `json:"monitor_id"`
	Hours     int     `json:"hours"`
	Up        int     `json:"up"`
	Down      int     `json:"down"`
	Pending   int     `json:"pending"`
	Total     int     `json:"total"`
	Uptime    float64 `json:"uptime"` // percentage, 0-100
	AvgMS     float64 `json:"avg_ms"`
	MinMS     int64   `json:"min_ms"`
	MaxMS     int64   `json:"max_ms"`
	P95MS     int64   `json:"p95_ms"`
}

// NotificationLog records one delivery attempt (success or failure) so the UI
// can show why a notification was not received.
type NotificationLog struct {
	ID             uint              `gorm:"primaryKey" json:"id"`
	NotificationID uint              `gorm:"not null;index" json:"notification_id"`
	MonitorID      uint              `gorm:"index" json:"monitor_id"`
	Event          NotificationEvent `gorm:"size:24;not null" json:"event"`
	Success        bool              `gorm:"not null;default:false" json:"success"`
	Error          string            `gorm:"size:1000" json:"error"`
	DurationMS     int64             `json:"duration_ms"`
	NodeID         string            `gorm:"size:64" json:"node_id"`
	CreatedAt      time.Time         `json:"created_at"`
}

// NotificationLock is the database lock used by the ANY_WITH_LOCK cluster
// strategy: the unique index on (monitor_id, event, bucket) guarantees that
// only the node that successfully inserts the row sends the notification.
type NotificationLock struct {
	ID        uint              `gorm:"primaryKey" json:"id"`
	MonitorID uint              `gorm:"not null;uniqueIndex:idx_notification_lock,priority:1" json:"monitor_id"`
	Event     NotificationEvent `gorm:"size:24;not null;uniqueIndex:idx_notification_lock,priority:2" json:"event"`
	// Bucket is the transition timestamp truncated to the lock window
	// (NotificationLockWindowSeconds).
	Bucket    int64     `gorm:"not null;uniqueIndex:idx_notification_lock,priority:3" json:"bucket"`
	NodeID    string    `gorm:"size:64" json:"node_id"`
	CreatedAt time.Time `json:"created_at"`
}

// NotificationLockWindowSeconds is the width of the de-duplication window: two
// nodes evaluating the same transition within this many seconds only produce a
// single notification.
const NotificationLockWindowSeconds = 60

// NotificationLockRetentionHours is how long a de-duplication row is kept before
// the maintenance job deletes it. The row only means something inside its own
// window (60 s for a status event, the day for a certificate reminder), so this
// is pure hygiene: before the prune existed the table grew for the whole life of
// the deployment, because the only delete was the one that runs when a monitor is
// deleted.
//
// The prune compares created_at and NOT bucket: bucket is a minute window for
// status events but a day number for certificate events, so any bucket-based
// cut-off would delete every certificate lock.
const NotificationLockRetentionHours = 24
