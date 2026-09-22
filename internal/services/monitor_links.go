package services

import (
	"context"
	"fmt"

	"github.com/ivancarlosti/up/internal/models"
)

// NotificationIDs returns the ids of the channels linked to a monitor.
func (s *MonitorService) NotificationIDs(ctx context.Context, monitorID uint) ([]uint, error) {
	var links []models.MonitorNotification
	if err := s.db.WithContext(ctx).Where("monitor_id = ?", monitorID).Find(&links).Error; err != nil {
		return nil, ErrInternal(err)
	}
	ids := make([]uint, 0, len(links))
	for _, link := range links {
		ids = append(ids, link.NotificationID)
	}
	return ids, nil
}

// Links returns the notification links (with the on_up/on_down flags) of a
// monitor; the notifier uses them to decide who receives what.
func (s *MonitorService) Links(ctx context.Context, monitorID uint) ([]models.MonitorNotification, error) {
	var links []models.MonitorNotification
	if err := s.db.WithContext(ctx).Where("monitor_id = ?", monitorID).Find(&links).Error; err != nil {
		return nil, ErrInternal(err)
	}
	return links, nil
}

// AllLinks returns the links of every monitor.
func (s *MonitorService) AllLinks(ctx context.Context) ([]models.MonitorNotification, error) {
	var links []models.MonitorNotification
	if err := s.db.WithContext(ctx).Find(&links).Error; err != nil {
		return nil, ErrInternal(err)
	}
	return links, nil
}

// ActiveForHost returns the active monitors this node is responsible for. The
// run_on field is respected: "all" runs everywhere, "primary" only on the
// primary node and "node" only on the node whose NODE_ID matches.
func (s *MonitorService) ActiveForHost(ctx context.Context, nodeID string, isPrimary bool) ([]*models.Monitor, error) {
	var monitors []*models.Monitor
	if err := s.db.WithContext(ctx).Where("active = ?", true).Order("id ASC").Find(&monitors).Error; err != nil {
		return nil, ErrInternal(fmt.Errorf("loading active monitors: %w", err))
	}
	out := make([]*models.Monitor, 0, len(monitors))
	for _, m := range monitors {
		switch m.RunOn {
		case "primary":
			if isPrimary {
				out = append(out, m)
			}
		case "node":
			if m.NodeID == nodeID {
				out = append(out, m)
			}
		default:
			out = append(out, m)
		}
	}
	return out, nil
}

// SeriesFor returns the most recent heartbeats of a monitor (used by the
// dashboard bars and by the public status pages).
func (s *MonitorService) SeriesFor(ctx context.Context, monitorID uint, limit int) ([]models.HeartbeatSummary, error) {
	series, err := s.stats.Series(ctx, monitorID, limit)
	if err != nil {
		return nil, ErrInternal(err)
	}
	return series, nil
}

// CountActive returns how many monitors are enabled (used by the dashboard
// summary and by the status page payload).
func (s *MonitorService) CountActive(ctx context.Context) (int64, error) {
	var count int64
	err := s.db.WithContext(ctx).Model(&models.Monitor{}).Where("active = ?", true).Count(&count).Error
	if err != nil {
		return 0, ErrInternal(err)
	}
	return count, nil
}
