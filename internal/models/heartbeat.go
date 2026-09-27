package models

import (
	"encoding/json"
	"time"
)

// MaxHeartbeatMessageLen is the width of heartbeats.message. A probe message is
// clipped to it before the insert so a verbose result (a long DNS answer, an
// exotic TLS error) can never be lost.
const MaxHeartbeatMessageLen = 500

// Heartbeat is a single check result produced by one node.
//
// The indexes are chosen around the two reads the application actually makes.
// idx_hb_monitor_time serves the newest-first listings; idx_hb_monitor_node
// serves the per-node voting (MAX(id) per monitor and node); idx_hb_stats is a
// covering index for the window statistics (monitor_id + created_at select the
// rows, status and latency_ms are read from the index without touching the
// clustered row), which is what used to cost one random row fetch per heartbeat
// on every dashboard load.
type Heartbeat struct {
	ID        uint `gorm:"primaryKey" json:"id"`
	MonitorID uint `gorm:"not null;index:idx_hb_monitor_time,priority:1;index:idx_hb_monitor_node,priority:1;index:idx_hb_stats,priority:1" json:"monitor_id"`
	// NodeID identifies which cluster node produced the heartbeat. It is the
	// same value as the NODE_ID environment variable.
	NodeID string `gorm:"size:64;index:idx_hb_monitor_node,priority:2" json:"node_id"`

	// Status is persisted as an integer, but exposed as a lower case string:
	// see MarshalJSON below. It is the third column of idx_hb_stats: the old
	// standalone index on status was never used (every query filters on a
	// monitor and a time range first) and only taxed the inserts.
	Status HeartbeatStatus `gorm:"not null;index:idx_hb_stats,priority:3" json:"-"`

	LatencyMS  int64  `gorm:"index:idx_hb_stats,priority:4" json:"latency_ms"`
	StatusCode int    `json:"status_code"`
	Message    string `gorm:"size:500" json:"message"`
	// Important is false while a monitor is still retrying, so notifications
	// are only triggered once the retries are exhausted.
	Important bool `gorm:"not null;default:false" json:"important"`

	CreatedAt time.Time `gorm:"index:idx_hb_monitor_time,priority:2;index:idx_hb_monitor_node,priority:3;index:idx_hb_stats,priority:2" json:"created_at"`
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

// HeartbeatRollupHorizonHours is the widest period the window statistics can
// report (30 days, the largest value of UptimeWindowHoursAllowed). Rollups older
// than it are never read, so they can be pruned together with the heartbeats
// they were computed from.
const HeartbeatRollupHorizonHours = 30 * 24

// HeartbeatBucket truncates a heartbeat timestamp to the hour its rollup row
// aggregates. Every timestamp in Up is UTC (see the loc=UTC of the DSN), so the
// same instant maps to the same bucket on every node of a cluster.
func HeartbeatBucket(t time.Time) time.Time {
	return t.UTC().Truncate(time.Hour)
}

// HeartbeatRollup is the hourly aggregate of one monitor's heartbeats.
//
// It exists because the window statistics used to scan every heartbeat of the
// last 30 days per request: a monitor checked every minute stores ~43 200 rows
// in that range (518 400 at a 5 s interval), and each one had to be fetched from
// the clustered index to read status and latency_ms. The same numbers are now
// read from at most 720 rows per monitor, maintained incrementally on write and
// reconciled at boot (see internal/database/rollup.go).
//
// The row is the exact reduction of the heartbeats of the hour, so the
// statistics it produces are identical to the ones the raw aggregate returned:
// Up/Down/Pending/Maintenance count the statuses, Total counts every heartbeat
// (including a status a future release may add), UpLatencySum/UpLatencyCount are
// the inputs of the AVG over the "up" checks only, and LatencyMin/LatencyMax are
// the MIN/MAX over every heartbeat of the bucket.
//
// The primary key (monitor_id, bucket_at) makes the incremental update a single
// upsert, which is also what keeps two nodes of a shared-mode cluster correct:
// both increment the same row.
type HeartbeatRollup struct {
	MonitorID uint      `gorm:"primaryKey;autoIncrement:false" json:"monitor_id"`
	BucketAt  time.Time `gorm:"primaryKey;autoIncrement:false;index:idx_rollup_bucket" json:"bucket_at"`

	Up          int64 `gorm:"not null;default:0" json:"up"`
	Down        int64 `gorm:"not null;default:0" json:"down"`
	Pending     int64 `gorm:"not null;default:0" json:"pending"`
	Maintenance int64 `gorm:"not null;default:0" json:"maintenance"`
	Total       int64 `gorm:"not null;default:0" json:"total"`

	UpLatencySum   int64 `gorm:"not null;default:0" json:"up_latency_sum"`
	UpLatencyCount int64 `gorm:"not null;default:0" json:"up_latency_count"`
	LatencyMin     int64 `gorm:"not null;default:0" json:"latency_min"`
	LatencyMax     int64 `gorm:"not null;default:0" json:"latency_max"`

	UpdatedAt time.Time `json:"updated_at"`
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
