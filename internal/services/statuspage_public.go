package services

import (
	"context"
	"fmt"
	"strings"

	"gorm.io/gorm"

	"github.com/ivancarlosti/up/internal/i18n"
	"github.com/ivancarlosti/up/internal/models"
)

// SetMonitors replaces the monitor selection of a status page. The order of the
// slice defines the display order.
func (s *StatusPageService) SetMonitors(ctx context.Context, pageID uint, monitorIDs []uint) error {
	if _, err := s.Get(ctx, pageID); err != nil {
		return err
	}
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("status_page_id = ?", pageID).Delete(&models.StatusPageMonitor{}).Error; err != nil {
			return err
		}
		seen := map[uint]bool{}
		order := 0
		for _, monitorID := range monitorIDs {
			if monitorID == 0 || seen[monitorID] {
				continue
			}
			seen[monitorID] = true
			item := models.StatusPageMonitor{
				StatusPageID: pageID,
				MonitorID:    monitorID,
				SortOrder:    order,
				ShowUptime:   true,
				ShowChart:    true,
			}
			if err := tx.Create(&item).Error; err != nil {
				return err
			}
			order++
		}
		return nil
	})
	if err != nil {
		return ErrInternal(fmt.Errorf("saving the status page monitors: %w", err))
	}
	s.log.Info("status page monitors updated", "status_page_id", pageID, "monitors", len(monitorIDs))
	return nil
}

// PublicPayload builds the payload rendered by the public status page: the page
// settings plus the decorated monitors (status, uptime and heartbeat bars).
//
// The monitors come from two sources: the explicit selection and the groups
// linked to the page, so adding a monitor to a group publishes it here. The flat
// `monitors` list is the ordered union (what the badge and the API have always
// returned) and `groups` carries the same list split into sections.
func (s *StatusPageService) PublicPayload(ctx context.Context, slug string, includeHidden bool) (*models.StatusPage, error) {
	page, err := s.GetBySlug(ctx, slug)
	if err != nil {
		return nil, err
	}
	if !page.IsPublic && !includeHidden {
		return nil, ErrForbidden(i18n.CodeStatusPageHidden, "this status page is not public")
	}

	items, err := s.Items(ctx, page.ID)
	if err != nil {
		return nil, err
	}
	links, err := s.Groups(ctx, page.ID)
	if err != nil {
		return nil, err
	}

	groupIDs := make([]uint, 0, len(links))
	names := map[uint]string{}
	for _, link := range links {
		groupIDs = append(groupIDs, link.GroupID)
		names[link.GroupID] = link.GroupName
	}
	members, err := s.groupMembers(ctx, groupIDs)
	if err != nil {
		return nil, err
	}
	sections, ids := planStatusPage(items, links, members, names)

	if len(ids) == 0 || s.monitors == nil {
		page.Monitors = []*models.Monitor{}
		page.Groups = []models.StatusPageGroup{}
		page.OverallStatus = models.AggregateUnknown
		return page, nil
	}

	var monitors []*models.Monitor
	if err := s.db.WithContext(ctx).Where("id IN ?", ids).Find(&monitors).Error; err != nil {
		return nil, ErrInternal(err)
	}
	byID := map[uint]*models.Monitor{}
	for _, monitor := range monitors {
		byID[monitor.ID] = monitor
	}
	// The per page rename only applies to the explicit selection: a monitor that
	// arrives through a group keeps its own name.
	displayNames := map[uint]string{}
	for _, item := range items {
		if trimmed := strings.TrimSpace(item.DisplayName); trimmed != "" {
			displayNames[item.MonitorID] = trimmed
		}
	}

	ordered := make([]*models.Monitor, 0, len(ids))
	for _, id := range ids {
		monitor, ok := byID[id]
		if !ok {
			continue
		}
		if name, ok := displayNames[id]; ok {
			monitor.Name = name
		}
		if series, seriesErr := s.monitors.SeriesFor(ctx, monitor.ID, 30); seriesErr == nil {
			monitor.Heartbeats = series
		}
		ordered = append(ordered, monitor)
	}

	if err := s.monitors.Decorate(ctx, ordered); err != nil {
		return nil, err
	}

	byMonitorID := map[uint]*models.Monitor{}
	for _, monitor := range ordered {
		byMonitorID[monitor.ID] = monitor
	}
	groups := make([]models.StatusPageGroup, 0, len(sections))
	for _, section := range sections {
		group := models.StatusPageGroup{Name: section.Name, Monitors: []*models.Monitor{}}
		for _, id := range section.MonitorIDs {
			if monitor, ok := byMonitorID[id]; ok {
				group.Monitors = append(group.Monitors, monitor)
			}
		}
		groups = append(groups, group)
	}
	page.Groups = groups
	page.Monitors = ordered
	page.ItemCount = len(ordered)
	for _, monitor := range ordered {
		switch monitor.Status {
		case models.AggregateUp:
			page.UpMonitors++
		case models.AggregateDown, models.AggregateDegraded:
			page.DownMonitors++
		}
	}
	page.OverallStatus = overallStatus(ordered)
	return page, nil
}

// overallStatus reduces the monitor statuses into a single page banner status.
func overallStatus(monitors []*models.Monitor) models.AggregateStatus {
	if len(monitors) == 0 {
		return models.AggregateUnknown
	}
	degraded := false
	pending := false
	for _, monitor := range monitors {
		switch monitor.Status {
		case models.AggregateDown:
			return models.AggregateDown
		case models.AggregateDegraded:
			degraded = true
		case models.AggregatePending, models.AggregateUnknown:
			pending = true
		}
	}
	if degraded {
		return models.AggregateDegraded
	}
	if pending {
		return models.AggregatePending
	}
	return models.AggregateUp
}
