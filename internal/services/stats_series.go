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
