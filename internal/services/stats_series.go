package services

import (
	"context"
	"fmt"
	"time"

	"github.com/ivancarlosti/up/internal/models"
)

// percentile95 samples the up heartbeats of the window and computes the 95th
// percentile in Go: MySQL and MariaDB have no portable percentile function.
func (s *StatsService) percentile95(ctx context.Context, monitorID uint, since time.Time) int64 {
	const sampleSize = 500
	var latencies []int64
	if err := s.db.WithContext(ctx).
		Model(&models.Heartbeat{}).
		Where("monitor_id = ? AND status = ? AND created_at >= ?", monitorID, models.StatusUp, since).
		Order("created_at DESC").
		Limit(sampleSize).
		Pluck("latency_ms", &latencies).Error; err != nil || len(latencies) == 0 {
		return 0
	}
	// Insertion sort is fine for the small sample window used here.
	for i := 1; i < len(latencies); i++ {
		for j := i; j > 0 && latencies[j] < latencies[j-1]; j-- {
			latencies[j], latencies[j-1] = latencies[j-1], latencies[j]
		}
	}
	idx := int(float64(len(latencies)) * 0.95)
	if idx >= len(latencies) {
		idx = len(latencies) - 1
	}
	return latencies[idx]
}

// Series returns the most recent heartbeats of a monitor, oldest first, ready
// to be rendered as the status bars of the dashboard.
func (s *StatsService) Series(ctx context.Context, monitorID uint, limit int) ([]models.HeartbeatSummary, error) {
	if limit <= 0 || limit > 1000 {
		limit = 60
	}
	var heartbeats []models.Heartbeat
	if err := s.db.WithContext(ctx).
		Where("monitor_id = ?", monitorID).
		Order("created_at DESC").
		Limit(limit).
		Find(&heartbeats).Error; err != nil {
		return nil, err
	}
	out := make([]models.HeartbeatSummary, 0, len(heartbeats))
	for i := len(heartbeats) - 1; i >= 0; i-- {
		hb := heartbeats[i]
		out = append(out, models.HeartbeatSummary{
			Status:    hb.Status,
			LatencyMS: hb.LatencyMS,
			CreatedAt: hb.CreatedAt,
			NodeID:    hb.NodeID,
		})
	}
	return out, nil
}

// LatestPerMonitor returns the newest heartbeat of every monitor.
func (s *StatsService) LatestPerMonitor(ctx context.Context, monitorIDs []uint) (map[uint]*models.Heartbeat, error) {
	out := map[uint]*models.Heartbeat{}
	if len(monitorIDs) == 0 {
		return out, nil
	}
	var heartbeats []models.Heartbeat
	query := `
		SELECT h.* FROM heartbeats h
		JOIN (
			SELECT monitor_id, MAX(id) AS max_id
			FROM heartbeats
			WHERE monitor_id IN ?
			GROUP BY monitor_id
		) latest ON latest.max_id = h.id`
	if err := s.db.WithContext(ctx).Raw(query, monitorIDs).Scan(&heartbeats).Error; err != nil {
		return nil, fmt.Errorf("loading the latest heartbeats: %w", err)
	}
	for i := range heartbeats {
		hb := heartbeats[i]
		out[hb.MonitorID] = &hb
	}
	return out, nil
}

// LatestPerNode returns the newest heartbeat of every (monitor, node) pair. It
// feeds the cluster voting logic and the per-node breakdown in the UI.
func (s *StatsService) LatestPerNode(ctx context.Context, monitorIDs []uint) ([]models.Heartbeat, error) {
	if len(monitorIDs) == 0 {
		return nil, nil
	}
	var heartbeats []models.Heartbeat
	query := `
		SELECT h.* FROM heartbeats h
		JOIN (
			SELECT monitor_id, node_id, MAX(id) AS max_id
			FROM heartbeats
			WHERE monitor_id IN ?
			GROUP BY monitor_id, node_id
		) latest ON latest.max_id = h.id
		ORDER BY h.monitor_id, h.node_id`
	if err := s.db.WithContext(ctx).Raw(query, monitorIDs).Scan(&heartbeats).Error; err != nil {
		return nil, fmt.Errorf("loading per node heartbeats: %w", err)
	}
	return heartbeats, nil
}

// List returns the heartbeats of a monitor inside a time window (newest first).
func (s *StatsService) List(ctx context.Context, monitorID uint, hours, limit int) ([]models.Heartbeat, error) {
	if limit <= 0 || limit > 5000 {
		limit = 500
	}
	if hours <= 0 {
		hours = 24
	}
	var heartbeats []models.Heartbeat
	err := s.db.WithContext(ctx).
		Where("monitor_id = ? AND created_at >= ?", monitorID, time.Now().UTC().Add(-time.Duration(hours)*time.Hour)).
		Order("created_at DESC").
		Limit(limit).
		Find(&heartbeats).Error
	return heartbeats, err
}

// Purge deletes heartbeats older than the given date (retention job).
func (s *StatsService) Purge(ctx context.Context, before time.Time) (int64, error) {
	result := s.db.WithContext(ctx).
		Where("created_at < ?", before).
		Delete(&models.Heartbeat{})
	return result.RowsAffected, result.Error
}

// HeartbeatWindowStats is the operator facing summary of the history table used
// by Admin > Settings: what is stored right now.
type HeartbeatWindowStats struct {
	Total  int64      `json:"total"`
	Oldest *time.Time `json:"oldest"`
	Newest *time.Time `json:"newest"`
}

// HeartbeatStats reads the size and the age range of the heartbeat history with
// a single aggregate query (COUNT/MIN/MAX over the primary key and the index on
// created_at).
func (s *StatsService) HeartbeatStats(ctx context.Context) (HeartbeatWindowStats, error) {
	row := struct {
		Total  int64      `gorm:"column:total"`
		Oldest *time.Time `gorm:"column:oldest"`
		Newest *time.Time `gorm:"column:newest"`
	}{}
	query := "SELECT COUNT(*) AS total, MIN(created_at) AS oldest, MAX(created_at) AS newest FROM heartbeats"
	if err := s.db.WithContext(ctx).Raw(query).Scan(&row).Error; err != nil {
		return HeartbeatWindowStats{}, fmt.Errorf("summarising the heartbeat history: %w", err)
	}
	return HeartbeatWindowStats{Total: row.Total, Oldest: row.Oldest, Newest: row.Newest}, nil
}

// CountOlderThan counts the heartbeats a purge would delete: it is the preview
// shown before the operator confirms the destructive action.
func (s *StatsService) CountOlderThan(ctx context.Context, before time.Time) (int64, error) {
	var count int64
	if err := s.db.WithContext(ctx).Model(&models.Heartbeat{}).
		Where("created_at < ?", before).
		Count(&count).Error; err != nil {
		return 0, fmt.Errorf("counting the heartbeat history: %w", err)
	}
	return count, nil
}

// HeartbeatBarSlots is how many slots the compact heartbeat bar of the monitors
// table has, whatever the selected window: a fixed number keeps the column the
// same width and the query bounded by monitors x slots rows.
const HeartbeatBarSlots = 30

// heartbeatBucketRow is one (monitor, bucket) aggregate of RecentBars.
type heartbeatBucketRow struct {
	MonitorID    uint     `gorm:"column:monitor_id"`
	Bucket       int      `gorm:"column:bucket"`
	Downs        int64    `gorm:"column:downs"`
	Pendings     int64    `gorm:"column:pendings"`
	Maintenances int64    `gorm:"column:maintenances"`
	Total        int64    `gorm:"column:total"`
	Latency      *float64 `gorm:"column:latency"`
}

// status reduces a bucket to the status that deserves attention: a single
// failed check marks the slot as down even when the bucket also saw successes.
func (r heartbeatBucketRow) status() string {
	switch {
	case r.Downs > 0:
		return models.StatusDown.String()
	case r.Pendings > 0:
		return models.StatusPending.String()
	case r.Maintenances > 0:
		return models.StatusMaintenance.String()
	case r.Total > 0:
		return models.StatusUp.String()
	}
	return ""
}

// RecentBars returns the compact, bucketed heartbeat history of a set of
// monitors: one status slot per bucket, oldest first, empty when the bucket saw
// no heartbeat (a paused monitor, or a gap in the history).
//
// It is ONE grouped query over the heartbeats inside the window, so the cost is
// bounded by monitors x slots instead of by the size of the history. That is
// what makes the column affordable on every dashboard load; the value is a
// snapshot, never a live feed.
func (s *StatsService) RecentBars(ctx context.Context, monitorIDs []uint, windowHours, slots int) (map[uint][]string, error) {
	out := map[uint][]string{}
	if len(monitorIDs) == 0 {
		return out, nil
	}
	if slots <= 0 || slots > 120 {
		slots = HeartbeatBarSlots
	}
	windowHours = models.NormalizeUptimeWindowHours(windowHours)
	since := time.Now().UTC().Add(-time.Duration(windowHours) * time.Hour)
	bucketSeconds := int64(windowHours) * 3600 / int64(slots)
	if bucketSeconds < 1 {
		bucketSeconds = 1
	}

	// The bucket index is computed from TIMESTAMPDIFF(SECOND, since, created_at)
	// and not from UNIX_TIMESTAMP: every stored timestamp is UTC, and
	// TIMESTAMPDIFF compares two DATETIME values without converting them through
	// the session timezone.
	query := `
		SELECT monitor_id,
		       FLOOR(TIMESTAMPDIFF(SECOND, ?, created_at) / ?) AS bucket,
		       SUM(CASE WHEN status = 0 THEN 1 ELSE 0 END)     AS downs,
		       SUM(CASE WHEN status = 2 THEN 1 ELSE 0 END)     AS pendings,
		       SUM(CASE WHEN status = 3 THEN 1 ELSE 0 END)     AS maintenances,
		       COUNT(*)                                        AS total,
		       AVG(latency_ms)                                 AS latency
		FROM heartbeats
		WHERE created_at >= ? AND monitor_id IN ?
		GROUP BY monitor_id, bucket`

	var rows []heartbeatBucketRow
	if err := s.db.WithContext(ctx).Raw(query, since, bucketSeconds, since, monitorIDs).Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("computing the heartbeat bars: %w", err)
	}
	for _, row := range rows {
		bucket := row.Bucket
		if bucket < 0 || bucket > slots {
			continue
		}
		if bucket == slots {
			// A heartbeat exactly on the upper boundary lands one past the last
			// slot (the window is inclusive at both ends): fold it into it.
			bucket = slots - 1
		}
		bars, ok := out[row.MonitorID]
		if !ok {
			bars = make([]string, slots)
			out[row.MonitorID] = bars
		}
		bars[bucket] = row.status()
	}
	return out, nil
}
