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
		if err := writeMonitorSelection(tx, pageID, monitorIDs); err != nil {
			return err
		}
		// The selection is part of the page payload, so it is a new version of the
		// page: the revision must advance or a peer would skip the change.
		return s.touchStatusPage(ctx, tx, pageID)
	})
	if err != nil {
		return ErrInternal(fmt.Errorf("saving the status page monitors: %w", err))
	}
	s.log.Info("status page monitors updated", "status_page_id", pageID, "monitors", len(monitorIDs))
	return nil
}

// statusPageMonitorRows renders the selection rows of a plain monitor id list: both
// switches on, order taken from the slice.
//
// The create and the selection endpoint share it, so an id list always writes the
// same columns, and so creating a page with monitors is ONE version of it (the create
// used to be followed by a separate selection edit, which published the page twice).
func statusPageMonitorRows(monitorIDs []uint) []models.StatusPageMonitor {
	items := make([]models.StatusPageMonitor, 0, len(monitorIDs))
	for _, monitorID := range monitorIDs {
		items = append(items, models.StatusPageMonitor{MonitorID: monitorID, ShowUptime: true, ShowChart: true})
	}
	return items
}

// writeMonitorSelection replaces the explicit monitor selection of a page with a
// plain id list (both switches on).
func writeMonitorSelection(tx *gorm.DB, pageID uint, monitorIDs []uint) error {
	return writeStatusPageItems(tx, pageID, statusPageMonitorRows(monitorIDs))
}

// writeStatusPageItems replaces the explicit monitor selection of a page with the
// given rows, keeping their order, their section, their display name and their two
// switches.
//
// It is shared by the create, the selection endpoint and the clone, so the three
// cannot write a different set of columns.
//
// An ORDER-ONLY row is not part of the selection: it only holds the manual position
// of a group member (see StatusPageService.SetOrder), so replacing the selection
// leaves it alone. A monitor that becomes explicit loses its order-only row here, or
// the unique index (status_page_id, monitor_id) would refuse the second row.
func writeStatusPageItems(tx *gorm.DB, pageID uint, items []models.StatusPageMonitor) error {
	if err := tx.Where("status_page_id = ? AND order_only = ?", pageID, false).
		Delete(&models.StatusPageMonitor{}).Error; err != nil {
		return err
	}
	seen := map[uint]bool{}
	order := 0
	for _, item := range items {
		// A caller that hands over an order-only row (the clone copies the
		// positions verbatim through writeStatusPageOrder) never publishes it.
		if item.MonitorID == 0 || item.OrderOnly || seen[item.MonitorID] {
			continue
		}
		seen[item.MonitorID] = true
		if err := tx.Where("status_page_id = ? AND monitor_id = ?", pageID, item.MonitorID).
			Delete(&models.StatusPageMonitor{}).Error; err != nil {
			return err
		}
		row := models.StatusPageMonitor{
			StatusPageID: pageID,
			MonitorID:    item.MonitorID,
			DisplayName:  item.DisplayName,
			GroupName:    item.GroupName,
			SortOrder:    order,
			ShowUptime:   item.ShowUptime,
			ShowChart:    item.ShowChart,
		}
		showUptime, showChart := row.ShowUptime, row.ShowChart
		if err := tx.Create(&row).Error; err != nil {
			return err
		}
		// The two switches carry a `default:true` column default, so GORM inserts
		// true for a false value and rewrites the struct (see createStatusPageRow).
		// The row is corrected when either switch is off — the only case the create
		// of a plain id list never reaches.
		if !showUptime || !showChart {
			if err := tx.Model(&models.StatusPageMonitor{}).
				Where("status_page_id = ? AND monitor_id = ?", pageID, row.MonitorID).
				UpdateColumns(map[string]any{"show_uptime": showUptime, "show_chart": showChart}).Error; err != nil {
				return err
			}
		}
		order++
	}
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
		if s.subscriptions != nil {
			page.SubscriptionAvailable = page.IsPublic && s.subscriptions.PublicAvailability(ctx, page)
			// The public payload never names the SMTP channel: it is internal
			// configuration, and only the boolean above is the visitor's business.
			page.SubscriptionSMTPUUID = ""
		}
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
	for id, name := range displayNames {
		if monitor, ok := byID[id]; ok {
			monitor.Name = name
		}
	}

	// The manual position of a monitor lives in its row: the selection row of an
	// explicit monitor, the ORDER-ONLY row of a group member (see statuspage_order.go).
	ranks := make(map[uint]int, len(items))
	for _, item := range items {
		ranks[item.MonitorID] = item.SortOrder
	}
	sections, ids = applyStatusPageOrder(sections, byID, ranks, page.MonitorOrder)

	ordered := make([]*models.Monitor, 0, len(ids))
	for _, id := range ids {
		monitor, ok := byID[id]
		if !ok {
			continue
		}
		if series, seriesErr := s.monitors.SeriesFor(ctx, monitor.ID, 30); seriesErr == nil {
			monitor.Heartbeats = series
		}
		ordered = append(ordered, monitor)
	}

	if err := s.monitors.DecorateWithWindow(ctx, ordered, page.EffectiveUptimeWindow(s.monitors.uptimeWindowHours())); err != nil {
		return nil, err
	}

	// The expiry observations are published one badge at a time, each behind its
	// own switch: the payload is anonymous, and the registration date of a domain
	// is not status information. A page that only asks for the certificate must
	// not leak the domain with it.
	for _, monitor := range ordered {
		if !page.ShowCertExpiry {
			monitor.Certificate = nil
		}
		if !page.ShowDomainExpiry {
			monitor.Domain = nil
		}
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
	if s.subscriptions != nil {
		page.SubscriptionAvailable = page.IsPublic && s.subscriptions.PublicAvailability(ctx, page)
		// The public payload never names the SMTP channel (see the empty-selection
		// branch above).
		page.SubscriptionSMTPUUID = ""
	}
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
