package services

import (
	"context"
	"log/slog"
	"time"

	"gorm.io/gorm"

	"github.com/ivancarlosti/up/internal/config"
	"github.com/ivancarlosti/up/internal/database"
	"github.com/ivancarlosti/up/internal/models"
)

// ClusterService owns everything related to the multi node setup: node
// registry and liveness, the join/leave handshake, the shared strategy settings
// and the aggregation of the per node heartbeats into a single monitor status.
//
// Key architectural point: every node of a cluster points to the SAME external
// database. That is what synchronises the monitor list, the aggregated state and
// the notification lock without any message broker.
type ClusterService struct {
	db            *gorm.DB
	cfg           *config.Config
	log           *slog.Logger
	settings      *SettingService
	stats         *StatsService
	monitors      *MonitorService
	notifications *NotificationService
	hub           EventPublisher
}

// NewClusterService builds the cluster service.
func NewClusterService(db *gorm.DB, cfg *config.Config, log *slog.Logger, settings *SettingService, stats *StatsService) *ClusterService {
	return &ClusterService{db: db, cfg: cfg, log: log, settings: settings, stats: stats}
}

// SetMonitorService injects the monitor service (used by the evaluator).
func (s *ClusterService) SetMonitorService(m *MonitorService) { s.monitors = m }

// SetNotificationService injects the notification service (used to dispatch).
func (s *ClusterService) SetNotificationService(n *NotificationService) { s.notifications = n }

// SetPublisher injects the real time publisher.
func (s *ClusterService) SetPublisher(p EventPublisher) { s.hub = p }

// SetStatsService injects the statistics service (wired by the container).
func (s *ClusterService) SetStatsService(stats *StatsService) { s.stats = stats }

// EnsureSelf registers this node and guarantees that a primary node exists.
//
// The claim lives in database.ClaimSelf so that the boot path (Seed) and this
// runtime path - called by Ping when the node row has disappeared - share one
// implementation, one lock and one repair of a duplicated primary.
func (s *ClusterService) EnsureSelf(ctx context.Context) error {
	if _, err := database.ClaimSelf(ctx, s.db, s.cfg, s.log); err != nil {
		return ErrInternal(err)
	}
	return nil
}

// Ping refreshes the liveness timestamp of this node.
func (s *ClusterService) Ping(ctx context.Context) error {
	now := time.Now().UTC()
	result := s.db.WithContext(ctx).Model(&models.Node{}).
		Where("node_id = ?", s.cfg.NodeID).
		Updates(map[string]any{
			"last_heartbeat": now,
			"status":         models.NodeStatusOnline,
			"api_url":        s.cfg.AppURL,
			"name":           s.cfg.NodeName,
			"updated_at":     now,
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return s.EnsureSelf(ctx)
	}
	return nil
}

func (s *ClusterService) publish(event string, payload any) {
	if s.hub != nil {
		s.hub.Publish(event, payload)
	}
}
