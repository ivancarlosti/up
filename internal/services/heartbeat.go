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

// heartbeatRollupUpsert folds one heartbeat into the hourly rollup the window
// statistics read. It is one statement, and it is written with the new values
// passed twice instead of the MySQL `VALUES(col)` function: VALUES() is
// deprecated in MySQL 8.0.20+ while MariaDB never grew the alias syntax, so the
// explicit form is the only one both accept.
//
// The increments are additive, so two nodes of a shared-mode cluster updating
// the same bucket simply sum; LEAST/GREATEST keep the bucket extremes. `total`
// is incremented for every heartbeat, whatever its status, exactly like the
// COUNT(*) the aggregate used to run.
const heartbeatRollupUpsert = `
	INSERT INTO heartbeat_rollups
		(monitor_id, bucket_at, up, down, pending, maintenance, total,
		 up_latency_sum, up_latency_count, latency_min, latency_max, updated_at)
	VALUES (?, ?, ?, ?, ?, ?, 1, ?, ?, ?, ?, ?)
	ON DUPLICATE KEY UPDATE
		up = up + ?,
		down = down + ?,
		pending = pending + ?,
		maintenance = maintenance + ?,
		total = total + 1,
		up_latency_sum = up_latency_sum + ?,
		up_latency_count = up_latency_count + ?,
		latency_min = LEAST(latency_min, ?),
		latency_max = GREATEST(latency_max, ?),
		updated_at = ?`

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
	// The message is diagnostic and the column holds 500 characters: a long DNS
	// answer or a verbose error must not make the insert fail, because a
	// heartbeat that is not stored also skips the status evaluation (and with it
	// the notification of an outage).
	heartbeat.Message = trimmed(heartbeat.Message, models.MaxHeartbeatMessageLen)
	if err := s.db.WithContext(ctx).Create(heartbeat).Error; err != nil {
		return fmt.Errorf("storing heartbeat: %w", err)
	}
	// The rollup only accelerates the history reads, so a failure here must not
	// turn a stored heartbeat into a lost check (the scheduler skips the status
	// evaluation when Record fails). It is logged, and the boot backfill
	// reconciles the trailing hours on the next restart.
	if err := s.rollup(ctx, heartbeat); err != nil {
		s.log.Warn("could not update the heartbeat rollup",
			"monitor_id", heartbeat.MonitorID, "error", err)
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

// rollup folds a freshly stored heartbeat into its hourly bucket.
func (s *HeartbeatService) rollup(ctx context.Context, heartbeat *models.Heartbeat) error {
	var up, down, pending, maintenance int
	switch heartbeat.Status {
	case models.StatusUp:
		up = 1
	case models.StatusDown:
		down = 1
	case models.StatusPending:
		pending = 1
	case models.StatusMaintenance:
		maintenance = 1
	}
	// The average latency is the one of the successful checks (see History), so
	// the sum and the count only move for an "up" heartbeat.
	var latencySum, latencyCount int64
	if heartbeat.Status == models.StatusUp {
		latencySum = heartbeat.LatencyMS
		latencyCount = 1
	}
	bucket := models.HeartbeatBucket(heartbeat.CreatedAt)
	now := time.Now().UTC()

	return s.db.WithContext(ctx).Exec(heartbeatRollupUpsert,
		// INSERT: the counters of this single heartbeat.
		heartbeat.MonitorID, bucket, up, down, pending, maintenance,
		latencySum, latencyCount, heartbeat.LatencyMS, heartbeat.LatencyMS, now,
		// UPDATE: the same values, added to the bucket.
		up, down, pending, maintenance,
		latencySum, latencyCount, heartbeat.LatencyMS, heartbeat.LatencyMS, now,
	).Error
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

// PurgeOlderThan deletes heartbeats older than the given time (retention job)
// together with the hourly rollups of the same period: a rollup that survives
// its heartbeats would resurrect data the retention policy dropped.
func (s *HeartbeatService) PurgeOlderThan(ctx context.Context, before time.Time) (int64, error) {
	if err := s.db.WithContext(ctx).Where("bucket_at < ?", before).
		Delete(&models.HeartbeatRollup{}).Error; err != nil {
		return 0, ErrInternal(err)
	}
	result := s.db.WithContext(ctx).Where("created_at < ?", before).Delete(&models.Heartbeat{})
	if result.Error != nil {
		return 0, ErrInternal(result.Error)
	}
	return result.RowsAffected, nil
}
