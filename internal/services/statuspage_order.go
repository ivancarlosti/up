package services

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"gorm.io/gorm"

	"github.com/ivancarlosti/up/internal/i18n"
	"github.com/ivancarlosti/up/internal/models"
)

// Ordering of the monitors of a status page (StatusPage.MonitorOrder).
//
// The page is a list of sections — the explicit monitors first, then one section per
// linked group — and the order OF THE SECTIONS belongs to the page (SetGroups): a
// setting that moved a monitor out of its group would break the headings the page
// exists for. So the setting only reorders the monitors INSIDE each section, and the
// flat `monitors` list is rebuilt from the sections, so the API payload and the page
// never disagree.
//
// Every mode ends in the page's manual order: "alphabetical" and "type" decide the
// coarse order and the positions the operator set decide the rest, which is what
// keeps two monitors of the same type in the order they were dragged into and what
// makes switching the mode back to "manual" a no-op.

// unrankedStatusPageRank is the rank of a monitor nobody positioned yet: it sorts after
// every positioned monitor of its section, and the unranked ones keep the order the
// section already had (the membership order of a group, the selection order of the
// explicit list). A monitor that joins a group after the page was ordered therefore
// appears at the end of its block instead of above monitors an operator moved up.
const unrankedStatusPageRank = int(^uint(0) >> 1)

// applyStatusPageOrder reorders the monitors of every section and returns the
// sections plus the flat monitor list in the same order.
//
// monitors holds the rendered monitors, whose Name is already the one the page shows
// (the per-page rename runs before this); ranks is the manual position of a monitor
// inside its section, absent when nobody positioned it.
func applyStatusPageOrder(sections []StatusPageSection, monitors map[uint]*models.Monitor,
	ranks map[uint]int, mode string) ([]StatusPageSection, []uint) {

	mode = models.NormalizeMonitorOrder(mode)
	rankOf := func(monitorID uint) int {
		if rank, ok := ranks[monitorID]; ok {
			return rank
		}
		return unrankedStatusPageRank
	}

	out := make([]StatusPageSection, 0, len(sections))
	flat := make([]uint, 0, len(sections))
	for _, section := range sections {
		ids := append([]uint(nil), section.MonitorIDs...)
		sort.SliceStable(ids, func(i, j int) bool {
			left, right := monitors[ids[i]], monitors[ids[j]]
			if left == nil || right == nil {
				// A monitor the payload could not load keeps its place by rank only.
				return rankOf(ids[i]) < rankOf(ids[j])
			}
			return compareStatusPageMonitors(mode, left, right, rankOf(ids[i]), rankOf(ids[j])) < 0
		})
		out = append(out, StatusPageSection{Name: section.Name, MonitorIDs: ids})
		flat = append(flat, ids...)
	}
	return out, flat
}

// compareStatusPageMonitors is the total order of one mode: the key of the mode (the
// name, or the type) first, then the manual position on the page, then the id.
//
// The id is the last resort on purpose: two nodes rendering the same page must agree on
// the pair the other keys cannot separate. Two monitors nobody positioned are equal
// instead, which is what keeps them in the order the section already had.
func compareStatusPageMonitors(mode string, a, b *models.Monitor, rankA, rankB int) int {
	switch mode {
	case models.MonitorOrderAlphabetical:
		// Case insensitive and locale independent: the page is anonymous, the
		// server has no visitor locale, and every visitor must see the same order.
		left := strings.ToLower(strings.TrimSpace(a.Name))
		right := strings.ToLower(strings.TrimSpace(b.Name))
		if left != right {
			return strings.Compare(left, right)
		}
	case models.MonitorOrderType:
		if left, right := models.MonitorTypeRank(a.Type), models.MonitorTypeRank(b.Type); left != right {
			return left - right
		}
	}
	if rankA != rankB {
		return rankA - rankB
	}
	if rankA == unrankedStatusPageRank {
		// Both monitors are unpositioned: the plan order is the order they already
		// have (the sort_order of the group membership for a group section, the
		// selection order for the explicit one), which is what the page showed
		// before anybody dragged anything. It is deterministic too.
		return 0
	}
	switch {
	case a.ID < b.ID:
		return -1
	case a.ID > b.ID:
		return 1
	}
	return 0
}

// statusPageSections resolves the rendering plan of a page from the database: its
// sections in page order, each with the monitors it renders.
//
// It is what the ordering endpoint validates a payload against, so the two cannot
// disagree on what "this page renders" means.
func (s *StatusPageService) statusPageSections(ctx context.Context, pageID uint) ([]StatusPageSection, error) {
	items, err := s.Items(ctx, pageID)
	if err != nil {
		return nil, err
	}
	links, err := s.Groups(ctx, pageID)
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
	sections, _ := planStatusPage(items, links, members, names)
	return sections, nil
}

// SetOrder stores the manual order of the monitors of a page.
//
// The list names every monitor the page renders, in the order it must show them,
// section after section (the dialog sends them concatenated, the way the page reads).
// An id the page does not render refuses the whole request: an order that silently
// dropped half of its ids would look saved and change nothing.
//
// The position of an explicit monitor is the sort_order of its selection row; the
// position of a group member is the sort_order of an ORDER-ONLY row, created on
// demand. Group members are the reason this is a page setting and not a column of the
// selection: an operator must be able to order a section the page inherits.
func (s *StatusPageService) SetOrder(ctx context.Context, pageID uint, monitorIDs []uint) error {
	if _, err := s.Get(ctx, pageID); err != nil {
		return err
	}
	sections, err := s.statusPageSections(ctx, pageID)
	if err != nil {
		return err
	}

	// A rank is only ever compared inside its own section, so every section numbers
	// its monitors from zero (see StatusPageMonitor.SortOrder).
	sectionOf := map[uint]int{}
	for index, section := range sections {
		for _, monitorID := range section.MonitorIDs {
			if _, ok := sectionOf[monitorID]; !ok {
				sectionOf[monitorID] = index
			}
		}
	}
	positions := make(map[uint]int, len(monitorIDs))
	next := make([]int, len(sections))
	for _, monitorID := range monitorIDs {
		section, ok := sectionOf[monitorID]
		if !ok {
			return ErrBadRequest(i18n.CodeValidation,
				fmt.Sprintf("monitor %d is not on this status page", monitorID))
		}
		if _, seen := positions[monitorID]; seen {
			continue
		}
		positions[monitorID] = next[section]
		next[section]++
	}
	// A monitor the list omits (added to a group by another tab while the dialog was
	// open) keeps a place at the end of its section instead of jumping to the top.
	for index, section := range sections {
		for _, monitorID := range section.MonitorIDs {
			if _, ok := positions[monitorID]; ok {
				continue
			}
			positions[monitorID] = next[index]
			next[index]++
		}
	}

	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := writeStatusPageOrder(tx, pageID, positions); err != nil {
			return err
		}
		// The manual order is part of the page payload, so it is a new version of
		// the page: the revision must advance or a peer would skip the change.
		return s.touchStatusPage(ctx, tx, pageID)
	})
	if err != nil {
		return ErrInternal(fmt.Errorf("saving the status page order: %w", err))
	}
	s.log.Info("status page order updated", "status_page_id", pageID, "monitors", len(positions))
	return nil
}

// writeStatusPageOrder stores the manual position of every monitor of a page.
//
// An explicit monitor keeps its row (only sort_order moves: the display name and the
// two switches stay), a group member gets — or updates — an ORDER-ONLY row. The rows
// of monitors the page no longer renders are dropped, so a stale position can never
// resurrect a monitor nobody publishes.
func writeStatusPageOrder(tx *gorm.DB, pageID uint, positions map[uint]int) error {
	var rows []models.StatusPageMonitor
	if err := tx.Where("status_page_id = ?", pageID).Find(&rows).Error; err != nil {
		return err
	}
	existing := make(map[uint]models.StatusPageMonitor, len(rows))
	for _, row := range rows {
		if _, ok := positions[row.MonitorID]; !ok {
			if err := tx.Delete(&models.StatusPageMonitor{}, row.ID).Error; err != nil {
				return err
			}
			continue
		}
		existing[row.MonitorID] = row
	}
	for monitorID, position := range positions {
		if row, ok := existing[monitorID]; ok {
			if row.SortOrder == position {
				continue
			}
			if err := tx.Model(&models.StatusPageMonitor{}).Where("id = ?", row.ID).
				UpdateColumn("sort_order", position).Error; err != nil {
				return err
			}
			continue
		}
		row := models.StatusPageMonitor{
			StatusPageID: pageID,
			MonitorID:    monitorID,
			SortOrder:    position,
			OrderOnly:    true,
			// The two switches carry a `default:true` column default, which the
			// engine applies when the struct carries false: they mean nothing on an
			// order-only row and are written as the default they are.
			ShowUptime: true,
			ShowChart:  true,
		}
		if err := tx.Create(&row).Error; err != nil {
			return err
		}
	}
	return nil
}
