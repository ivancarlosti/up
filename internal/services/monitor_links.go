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

// GroupRefsByMonitor returns the single group of every monitor, keyed by monitor
// id, as the id and the name the payload reports (used by Decorate: the dashboard
// and the monitor list show the group of each row, and the notification bodies
// read its name).
//
// A monitor belongs to at most one group, so the map holds one ref per monitor at
// most. The first row per monitor wins, which keeps the read deterministic on a
// database that still holds a duplicate (the boot consolidation removes them).
func (s *MonitorService) GroupRefsByMonitor(ctx context.Context) (map[uint]models.MonitorGroupRef, error) {
	var rows []struct {
		MonitorID uint   `gorm:"column:monitor_id"`
		GroupID   uint   `gorm:"column:group_id"`
		Name      string `gorm:"column:name"`
	}
	if err := s.db.WithContext(ctx).
		Table("monitor_group_members AS m").
		Select("m.monitor_id, m.group_id, g.name").
		Joins("JOIN monitor_groups AS g ON g.id = m.group_id").
		Order("m.monitor_id ASC, g.sort_order ASC, g.id ASC").
		Scan(&rows).Error; err != nil {
		return nil, ErrInternal(err)
	}
	out := make(map[uint]models.MonitorGroupRef, len(rows))
	for _, row := range rows {
		if _, seen := out[row.MonitorID]; seen {
			continue
		}
		out[row.MonitorID] = models.MonitorGroupRef{ID: row.GroupID, Name: row.Name}
	}
	return out, nil
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
