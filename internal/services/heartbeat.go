package services

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"gorm.io/gorm"

	"github.com/ivancarlosti/up/internal/config"
	"github.com/ivancarlosti/up/internal/models"
)

// HeartbeatService stores the individual check results produced by the
// scheduler of every node.
type HeartbeatService struct {
	db  *gorm.DB
	cfg *config.Config
	log *slog.Logger
	hub EventPublisher
}

// NewHeartbeatService builds the heartbeat service.
func NewHeartbeatService(db *gorm.DB, cfg *config.Config, log *slog.Logger) *HeartbeatService {
	return &HeartbeatService{db: db, cfg: cfg, log: log}
}

// SetPublisher injects the real time publisher.
func (s *HeartbeatService) SetPublisher(p EventPublisher) { s.hub = p }

// Record persists a heartbeat and broadcasts it over WebSocket. The node_id is
// filled with NODE_ID when the caller did not provide one, which is what makes
// the per node voting possible.
func (s *HeartbeatService) Record(ctx context.Context, heartbeat *models.Heartbeat) error {
	if heartbeat.NodeID == "" {
		heartbeat.NodeID = s.cfg.NodeID
	}
	if heartbeat.CreatedAt.IsZero() {
		heartbeat.CreatedAt = time.Now().UTC()
	}
	if err := s.db.WithContext(ctx).Create(heartbeat).Error; err != nil {
		return fmt.Errorf("storing heartbeat: %w", err)
	}
	if s.hub != nil {
		s.hub.Publish("heartbeat", map[string]any{
			"monitor_id":  heartbeat.MonitorID,
			"node_id":     heartbeat.NodeID,
			"status":      heartbeat.Status,
			"latency_ms":  heartbeat.LatencyMS,
			"status_code": heartbeat.StatusCode,
			"message":     heartbeat.Message,
			"important":   heartbeat.Important,
			"created_at":  heartbeat.CreatedAt,
		})
	}
	return nil
}

// Recent returns the newest heartbeats of a monitor.
func (s *HeartbeatService) Recent(ctx context.Context, monitorID uint, limit int) ([]models.Heartbeat, error) {
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	var heartbeats []models.Heartbeat
	err := s.db.WithContext(ctx).
		Where("monitor_id = ?", monitorID).
		Order("created_at DESC").
		Limit(limit).
		Find(&heartbeats).Error
	if err != nil {
		return nil, ErrInternal(err)
	}
	return heartbeats, nil
}

// PurgeOlderThan deletes heartbeats older than the given time (retention job).
func (s *HeartbeatService) PurgeOlderThan(ctx context.Context, before time.Time) (int64, error) {
	result := s.db.WithContext(ctx).Where("created_at < ?", before).Delete(&models.Heartbeat{})
	if result.Error != nil {
		return 0, ErrInternal(result.Error)
	}
	return result.RowsAffected, nil
}

// PingStats summarises how many heartbeats were stored recently, which is handy
// to verify that the scheduler is running.
func (s *HeartbeatService) PingStats(ctx context.Context) (int64, error) {
	var count int64
	err := s.db.WithContext(ctx).Model(&models.Heartbeat{}).
		Where("created_at >= ?", time.Now().UTC().Add(-time.Hour)).
		Count(&count).Error
	if err != nil {
		return 0, ErrInternal(err)
	}
	return count, nil
}
