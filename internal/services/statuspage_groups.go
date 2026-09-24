package services

import (
	"context"
	"fmt"

	"gorm.io/gorm"

	"github.com/ivancarlosti/up/internal/i18n"
	"github.com/ivancarlosti/up/internal/models"
)

// StatusPageSection is the resolved rendering order of a status page: one section
// per group (plus the explicit monitors, which have no name) with the monitor
// ids in display order.
type StatusPageSection struct {
	Name       string
	MonitorIDs []uint
}

// planStatusPage resolves the explicit monitor selection and the linked groups
// into the ordered sections and the flat monitor list used by `page.monitors`.
//
// Rules: the explicit monitors come first, in their own order; then every linked
// group in the page order. A monitor that belongs to several groups is rendered
// only in the first group that claims it, an empty group produces no section
// (it would be an empty heading on the public page) and the flat list keeps the
// exact order of the sections, so the API payload and the page never disagree.
func planStatusPage(items []models.StatusPageMonitor, links []models.StatusPageGroupLink,
	members map[uint][]uint, names map[uint]string) ([]StatusPageSection, []uint) {

	seen := map[uint]bool{}
	flat := make([]uint, 0, len(items))
	sections := make([]StatusPageSection, 0, len(links)+1)

	explicit := StatusPageSection{Name: "", MonitorIDs: []uint{}}
	for _, item := range items {
		if item.MonitorID == 0 || seen[item.MonitorID] {
			continue
		}
		seen[item.MonitorID] = true
		explicit.MonitorIDs = append(explicit.MonitorIDs, item.MonitorID)
		flat = append(flat, item.MonitorID)
	}
	if len(explicit.MonitorIDs) > 0 {
		sections = append(sections, explicit)
	}

	for _, link := range links {
		name := link.DisplayName
		if name == "" {
			name = names[link.GroupID]
		}
		section := StatusPageSection{Name: name, MonitorIDs: []uint{}}
		for _, monitorID := range members[link.GroupID] {
			if monitorID == 0 || seen[monitorID] {
				continue
			}
			seen[monitorID] = true
			section.MonitorIDs = append(section.MonitorIDs, monitorID)
			flat = append(flat, monitorID)
		}
		if len(section.MonitorIDs) > 0 {
			sections = append(sections, section)
		}
	}
	return sections, flat
}

// Groups returns the group links of a status page (ordered) with the group name.
func (s *StatusPageService) Groups(ctx context.Context, pageID uint) ([]models.StatusPageGroupLink, error) {
	var links []models.StatusPageGroupLink
	if err := s.db.WithContext(ctx).
		Where("status_page_id = ?", pageID).
		Order("sort_order ASC, id ASC").
		Find(&links).Error; err != nil {
		return nil, ErrInternal(err)
	}
	if len(links) == 0 {
		return links, nil
	}
	ids := make([]uint, 0, len(links))
	for _, link := range links {
		ids = append(ids, link.GroupID)
	}
	var groups []models.MonitorGroup
	if err := s.db.WithContext(ctx).Where("id IN ?", ids).Find(&groups).Error; err != nil {
		return nil, ErrInternal(err)
	}
	names := map[uint]string{}
	for _, group := range groups {
		names[group.ID] = group.Name
	}
	for i := range links {
		links[i].GroupName = names[links[i].GroupID]
	}
	return links, nil
}

// SetGroups replaces the groups included in a status page. The order of the slice
// defines the order of the sections.
func (s *StatusPageService) SetGroups(ctx context.Context, pageID uint, links []models.StatusPageGroupLink) error {
	if _, err := s.Get(ctx, pageID); err != nil {
		return err
	}
	// Every group must exist: a dangling link would silently hide monitors.
	ids := make([]uint, 0, len(links))
	for _, link := range links {
		if link.GroupID != 0 {
			ids = append(ids, link.GroupID)
		}
	}
	if len(ids) > 0 {
		var found int64
		if err := s.db.WithContext(ctx).Model(&models.MonitorGroup{}).Where("id IN ?", ids).Count(&found).Error; err != nil {
			return ErrInternal(err)
		}
		if int(found) != len(uniqueIDs(ids)) {
			return ErrBadRequest(i18n.CodeMonitorGroupInvalid, "groups contains an unknown group")
		}
	}

	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("status_page_id = ?", pageID).Delete(&models.StatusPageGroupLink{}).Error; err != nil {
			return err
		}
		order := 0
		for _, link := range links {
			if link.GroupID == 0 {
				continue
			}
			row := models.StatusPageGroupLink{
				StatusPageID: pageID,
				GroupID:      link.GroupID,
				DisplayName:  link.DisplayName,
				SortOrder:    order,
			}
			if err := tx.Create(&row).Error; err != nil {
				return err
			}
			order++
		}
		// The group sections are part of the page payload, so this is a new version
		// of the page: the revision must advance or a peer would skip the change.
		return s.touchStatusPage(ctx, tx, pageID)
	})
	if err != nil {
		return ErrInternal(fmt.Errorf("saving the status page groups: %w", err))
	}
	s.log.Info("status page groups updated", "status_page_id", pageID, "groups", len(links))
	return nil
}

// groupMembers loads the members of the given groups, keyed by group id.
func (s *StatusPageService) groupMembers(ctx context.Context, groupIDs []uint) (map[uint][]uint, error) {
	out := map[uint][]uint{}
	if len(groupIDs) == 0 {
		return out, nil
	}
	var members []models.MonitorGroupMember
	if err := s.db.WithContext(ctx).
		Where("group_id IN ?", groupIDs).
		Order("group_id ASC, sort_order ASC, monitor_id ASC").
		Find(&members).Error; err != nil {
		return nil, ErrInternal(err)
	}
	for _, member := range members {
		out[member.GroupID] = append(out[member.GroupID], member.MonitorID)
	}
	return out, nil
}
