package services

import (
	"context"
	"fmt"

	"github.com/ivancarlosti/up/internal/models"
)

// TemplateLinkResult reports what a "link monitors to a template" run did.
type TemplateLinkResult struct {
	// Monitors is how many monitors the scope considered.
	Monitors int `json:"monitors"`
	// Linked is how many of them started following the template.
	Linked int `json:"linked"`
	// Updated is how many received a different value for at least one field.
	Updated int `json:"updated"`
	// Unlinked is how many monitors stopped following the template because the
	// run left them outside its scope. Only a group-scoped run detaches: the
	// "every monitor of this type" scope has no outside.
	Unlinked int `json:"unlinked"`
	// DryRun is true when nothing was written: the counts are the preview.
	DryRun bool `json:"dry_run"`
}

// LinkOptions selects the monitors a link run touches.
type LinkOptions struct {
	// TemplateID is the template every scoped monitor starts following.
	TemplateID uint
	// GroupIDs limits the run to the monitors that belong to at least one of
	// these groups. Empty means every monitor of the template type.
	GroupIDs []uint
	// DryRun computes the preview without writing anything.
	DryRun bool
}

// propagationFields is what a template pushes to the monitors that follow it.
//
// It is the applicable field list minus the one field that must never be pushed
// empty: a template without notification channels must not mute the monitors that
// follow it, so "notification_ids" only travels when the template lists channels.
// Groups and tags are not in the list at all (see models.TemplateDefaultFields):
// two monitors can follow one template and live in different groups with
// different tags.
func propagationFields(template *models.MonitorTemplate) []string {
	fields := make([]string, 0, len(models.TemplateDefaultFields()))
	for _, field := range models.TemplateDefaultFields() {
		if field == "notification_ids" && len(template.Defaults.NotificationIDs) == 0 {
			continue
		}
		fields = append(fields, field)
	}
	return fields
}

// Propagate applies the template defaults to every monitor that follows it.
//
// Only the monitors whose values actually differ are written, which is what
// keeps a federated cluster from bumping revisions back and forth: the nodes
// converge and then stop instead of each change looking like a new edit.
func (s *MonitorTemplateService) Propagate(ctx context.Context, templateID uint) (int, error) {
	if s.mono == nil {
		return 0, fmt.Errorf("the monitor service is not wired into the template service")
	}
	template, err := s.Get(ctx, templateID)
	if err != nil {
		return 0, err
	}
	var monitors []*models.Monitor
	if err := s.db.WithContext(ctx).Where("template_uuid = ?", template.UUID).
		Order("id ASC").Find(&monitors).Error; err != nil {
		return 0, ErrInternal(fmt.Errorf("listing the monitors of template %d: %w", templateID, err))
	}
	fields := propagationFields(template)
	updated := 0
	for _, monitor := range monitors {
		if monitor.Type != template.Type {
			continue // a template never reshapes a monitor of another type
		}
		if len(planApply(monitor, template, fields)) == 0 {
			continue
		}
		next, notificationIDs, groupIDs := applyTemplate(monitor, template, fields)
		next.TemplateUUID = template.UUID
		if err := s.mono.Update(ctx, next, notificationIDs, groupIDs); err != nil {
			s.log.Warn("could not propagate the template",
				"template", template.ID, "monitor_id", monitor.ID, "error", err)
			continue
		}
		updated++
	}
	return updated, nil
}

// groupScope keeps only the monitors that belong to the selected groups.
//
// A nil set means "no scope was selected" and returns the list untouched. A
// non-nil set is the resolved membership, so an empty map (a group with no
// member) correctly yields an empty scope instead of every monitor. It is pure
// so the scope rule can be unit tested without a database.
func groupScope(monitors []*models.Monitor, memberIDs map[uint]bool) []*models.Monitor {
	if memberIDs == nil {
		return monitors
	}
	scoped := make([]*models.Monitor, 0, len(monitors))
	for _, monitor := range monitors {
		if memberIDs[monitor.ID] {
			scoped = append(scoped, monitor)
		}
	}
	return scoped
}

// unlinkScope returns the monitors that follow the template but are outside the
// scope of the run, i.e. the ones a group-scoped run detaches.
//
// It is the counterpart of groupScope: that one keeps the members, this one
// keeps everybody else. Only a group scope has an outside, so the callers only
// reach it with one. It is pure so the rule can be unit tested without a
// database.
func unlinkScope(followers, scoped []*models.Monitor) []*models.Monitor {
	inScope := make(map[uint]bool, len(scoped))
	for _, monitor := range scoped {
		inScope[monitor.ID] = true
	}
	detached := make([]*models.Monitor, 0, len(followers))
	for _, follower := range followers {
		if !inScope[follower.ID] {
			detached = append(detached, follower)
		}
	}
	return detached
}

// groupMembers resolves the set of monitor ids that belong to any of the given
// groups.
func (s *MonitorTemplateService) groupMembers(ctx context.Context, groupIDs []uint) (map[uint]bool, error) {
	var ids []uint
	if err := s.db.WithContext(ctx).Model(&models.MonitorGroupMember{}).
		Where("group_id IN ?", groupIDs).Distinct().Pluck("monitor_id", &ids).Error; err != nil {
		return nil, ErrInternal(fmt.Errorf("listing the members of the selected groups: %w", err))
	}
	members := make(map[uint]bool, len(ids))
	for _, id := range ids {
		members[id] = true
	}
	return members, nil
}

// unlinkOutOfScope detaches the monitors that follow the template and fall
// outside the scope of a group-scoped run, which is what makes the scope of the
// run the scope of the template. The "every monitor of this type" scope never
// reaches this method: everything of the type is inside it.
//
// A detach is an edit of a monitor column (the link), so it goes through
// MonitorService.Update: the revision advances and the cluster is told, instead
// of a silent UPDATE that every peer would keep applying the template from. The
// rows are written one by one because that is what validates and publishes them;
// a follower whose row no longer validates keeps following the template, and the
// failure is logged instead of counted. With DryRun the monitors are only
// counted, so the dialog can warn before the link disappears.
func (s *MonitorTemplateService) unlinkOutOfScope(ctx context.Context, template *models.MonitorTemplate, scoped []*models.Monitor, dryRun bool) (int, error) {
	var followers []*models.Monitor
	if err := s.db.WithContext(ctx).Where("template_uuid = ?", template.UUID).
		Order("id ASC").Find(&followers).Error; err != nil {
		return 0, ErrInternal(fmt.Errorf("listing the monitors of template %d: %w", template.ID, err))
	}
	detached := unlinkScope(followers, scoped)
	if dryRun || len(detached) == 0 {
		return len(detached), nil
	}
	cleared := 0
	for _, monitor := range detached {
		// A copy, exactly like applyTemplate does for the attach: the follower is
		// what it was, minus the link. The channels and the groups are passed as
		// nil (keep them): they are never part of the template.
		next := *monitor
		next.TemplateUUID = ""
		if err := s.mono.Update(ctx, &next, nil, nil); err != nil {
			s.log.Warn("could not detach the monitor from the template",
				"template", template.ID, "monitor_id", monitor.ID, "error", err)
			continue
		}
		cleared++
	}
	if cleared > 0 {
		s.log.Info("monitors detached from a template", "template", template.ID, "monitors", cleared)
	}
	return cleared, nil
}

// LinkAll attaches the monitors in scope to the template and applies the
// defaults, which is how an existing installation is gathered under a template
// without clicking through every monitor. The scope is either every monitor of
// the template type or the monitors of the selected groups. With DryRun it only
// reports what would happen, so the UI can ask for a confirmation first.
//
// The scope of a run is the scope of the template: a run that selects groups
// also detaches the monitors that follow the template from outside the selection
// (see unlinkOutOfScope). The type scope never detaches anybody, because it
// covers every monitor of the type.
func (s *MonitorTemplateService) LinkAll(ctx context.Context, opts LinkOptions) (*TemplateLinkResult, error) {
	if s.mono == nil {
		return nil, fmt.Errorf("the monitor service is not wired into the template service")
	}
	template, err := s.Get(ctx, opts.TemplateID)
	if err != nil {
		return nil, err
	}
	var monitors []*models.Monitor
	if err := s.db.WithContext(ctx).Where("type = ?", template.Type).
		Order("id ASC").Find(&monitors).Error; err != nil {
		return nil, ErrInternal(fmt.Errorf("listing the %s monitors: %w", template.Type, err))
	}
	members := map[uint]bool(nil)
	if len(opts.GroupIDs) > 0 {
		if members, err = s.groupMembers(ctx, opts.GroupIDs); err != nil {
			return nil, err
		}
	}
	monitors = groupScope(monitors, members)
	fields := propagationFields(template)
	result := &TemplateLinkResult{Monitors: len(monitors), DryRun: opts.DryRun}
	for _, monitor := range monitors {
		linked := monitor.TemplateUUID == template.UUID
		differs := !linked || len(planApply(monitor, template, fields)) > 0
		if opts.DryRun {
			if !linked {
				result.Linked++
			}
			if differs {
				result.Updated++
			}
			continue
		}
		if !differs {
			continue
		}
		next, notificationIDs, groupIDs := applyTemplate(monitor, template, fields)
		next.TemplateUUID = template.UUID
		if err := s.mono.Update(ctx, next, notificationIDs, groupIDs); err != nil {
			s.log.Warn("could not link the monitor to the template",
				"template", template.ID, "monitor_id", monitor.ID, "error", err)
			continue
		}
		if !linked {
			result.Linked++
		}
		result.Updated++
	}
	// The scope of the run replaces the scope of the template: a group-scoped run
	// detaches the monitors that follow the template from outside the selected
	// groups, so the operator ends up with exactly the monitors the dialog
	// previewed. The type scope has no outside and never detaches.
	if len(opts.GroupIDs) > 0 {
		unlinked, err := s.unlinkOutOfScope(ctx, template, monitors, opts.DryRun)
		if err != nil {
			return nil, err
		}
		result.Unlinked = unlinked
	}
	if !opts.DryRun {
		s.log.Info("monitors linked to a template",
			"template", template.ID, "name", template.Name, "type", template.Type,
			"groups", len(opts.GroupIDs), "monitors", result.Monitors,
			"linked", result.Linked, "updated", result.Updated, "unlinked", result.Unlinked)
		s.publish("monitor.template.linked", map[string]any{
			"template_id": template.ID, "linked": result.Linked,
			"updated": result.Updated, "unlinked": result.Unlinked,
		})
	}
	return result, nil
}
