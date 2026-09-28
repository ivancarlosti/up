package services

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"gorm.io/gorm"

	"github.com/ivancarlosti/up/internal/i18n"
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
	// run left them outside its scope. Only a scope with an outside (groups,
	// tags) detaches: "every monitor of this type" has none.
	Unlinked int `json:"unlinked"`
	// DryRun is true when nothing was written: the counts are the preview.
	DryRun bool `json:"dry_run"`
	// Scope is the scope the run applied, with the group uuids and tags
	// normalized and pruned of anything that no longer exists.
	//
	// A dry run reports the scope it *would* store, so the dialog can show the
	// operator exactly what the next real run is going to remember (and detach).
	Scope models.TemplateLinkScope `json:"scope"`
}

// LinkOptions selects the monitors a link run touches.
type LinkOptions struct {
	// TemplateID is the template every scoped monitor starts following.
	TemplateID uint
	// Scope is the selection of the run: the whole type, some groups (by uuid)
	// or some tags. It is persisted on the template once the run succeeds, so
	// the dialog reopens on the same decision.
	//
	// A zero scope (kind empty) means "the request did not decide": GroupIDs is
	// then honoured for compatibility, otherwise the type scope applies.
	Scope models.TemplateLinkScope
	// GroupIDs limits the run to the monitors that belong to at least one of
	// these groups, by local id.
	//
	// Deprecated: it is the pre-scope API of the endpoint (the id of a group is
	// not a cluster-wide identity, see TemplateLinkScope). It keeps working so an
	// old client does not silently link everything, and it is translated to the
	// uuid form before the run so both entry points share one implementation.
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
// scope of the run, i.e. the ones a run with an outside detaches.
//
// It is the counterpart of groupScope: that one keeps the members, this one
// keeps everybody else. Only a scope with an outside (groups, tags) reaches it,
// so a type-scoped run never loses a follower. It is pure so the rule can be unit
// tested without a database.
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

// scopeSelection is the resolved scope of a run: the group membership and the
// tags the run selects, after the database said which of them still exist.
type scopeSelection struct {
	// members is the resolved group membership. A nil map means "the scope is
	// not a group scope", so no membership query had to run.
	members map[uint]bool
	// tags is the tag selection. Empty means "the scope is not a tag scope".
	tags []string
}

// scopedMonitors keeps the monitors the selection covers.
//
// It is the single place where a scope becomes a list of monitors, so the group
// rule and the tag rule cannot drift apart. It is pure: everything that needs the
// database is resolved before the call.
func scopedMonitors(monitors []*models.Monitor, selection scopeSelection) []*models.Monitor {
	if selection.members != nil {
		return groupScope(monitors, selection.members)
	}
	if len(selection.tags) == 0 {
		return monitors
	}
	tagged := make([]*models.Monitor, 0, len(monitors))
	for _, monitor := range monitors {
		// Exact, case insensitive, one tag at a time: a substring match would
		// pull "production" into a run scoped to "prod" (see models.HasAnyTag).
		if models.HasAnyTag(monitor.Tags, selection.tags) {
			tagged = append(tagged, monitor)
		}
	}
	return tagged
}

// groupMembersByUUID resolves the set of monitor ids that belong to any of the
// groups named by uuid.
//
// The uuid is what a scope stores (models.TemplateLinkScope): the same selection
// has to mean the same groups on every node of a cluster, where the local ids of
// the groups differ.
func (s *MonitorTemplateService) groupMembersByUUID(ctx context.Context, uuids []string) (map[uint]bool, error) {
	var ids []uint
	if err := s.db.WithContext(ctx).Model(&models.MonitorGroupMember{}).
		Joins("JOIN monitor_groups ON monitor_groups.id = monitor_group_members.group_id").
		Where("monitor_groups.uuid IN ?", uuids).
		Distinct().
		Pluck("monitor_group_members.monitor_id", &ids).Error; err != nil {
		return nil, ErrInternal(fmt.Errorf("listing the members of the selected groups: %w", err))
	}
	members := make(map[uint]bool, len(ids))
	for _, id := range ids {
		members[id] = true
	}
	return members, nil
}

// existingGroupUUIDs keeps the group uuids that still exist, in the order they
// were given. A group deleted on another node is dropped from a stored scope
// instead of making every following run fail on a link that is gone.
func (s *MonitorTemplateService) existingGroupUUIDs(ctx context.Context, uuids []string) ([]string, error) {
	var known []string
	if err := s.db.WithContext(ctx).Model(&models.MonitorGroup{}).
		Where("uuid IN ?", uuids).Pluck("uuid", &known).Error; err != nil {
		return nil, ErrInternal(fmt.Errorf("resolving the selected monitor groups: %w", err))
	}
	found := make(map[string]bool, len(known))
	for _, uuid := range known {
		found[uuid] = true
	}
	kept := make([]string, 0, len(uuids))
	for _, uuid := range uuids {
		if found[uuid] {
			kept = append(kept, uuid)
		}
	}
	return kept, nil
}

// groupUUIDsByID translates the deprecated id-based selection of the endpoint
// into the uuid form a scope stores.
func (s *MonitorTemplateService) groupUUIDsByID(ctx context.Context, ids []uint) ([]string, error) {
	var uuids []string
	if err := s.db.WithContext(ctx).Model(&models.MonitorGroup{}).
		Where("id IN ?", ids).Order("id ASC").Pluck("uuid", &uuids).Error; err != nil {
		return nil, ErrInternal(fmt.Errorf("resolving the selected monitor groups: %w", err))
	}
	return uuids, nil
}

// prepareLinkScope turns the options of a run into the scope it applies and
// stores.
//
// A request without a scope falls back to the deprecated GroupIDs (an older
// client must keep linking exactly what it asked for) and then to the type scope.
// A group scope is pruned of the groups that no longer exist; if none of them is
// left the run is refused, because "the groups I selected are the groups I want"
// and silently running over the whole type — or over nobody — would be worse than
// an error the operator can read.
func (s *MonitorTemplateService) prepareLinkScope(ctx context.Context, opts LinkOptions) (models.TemplateLinkScope, error) {
	scope := opts.Scope
	if scope.IsZero() {
		if len(opts.GroupIDs) > 0 {
			uuids, err := s.groupUUIDsByID(ctx, opts.GroupIDs)
			if err != nil {
				return scope, err
			}
			scope = models.TemplateLinkScope{Kind: models.TemplateScopeGroups, GroupUUIDs: uuids}
		} else {
			scope = models.TemplateLinkScope{Kind: models.TemplateScopeType}
		}
	}
	scope.Normalize()
	if problem := scope.Valid(); problem != "" {
		return scope, ErrBadRequest(i18n.CodeMonitorTemplateInvalid, problem)
	}
	if scope.Kind != models.TemplateScopeGroups {
		return scope, nil
	}
	known, err := s.existingGroupUUIDs(ctx, scope.GroupUUIDs)
	if err != nil {
		return scope, err
	}
	if len(known) == 0 {
		return scope, ErrBadRequest(i18n.CodeMonitorTemplateInvalid,
			"the selected monitor groups no longer exist")
	}
	scope.GroupUUIDs = known
	return scope, nil
}

// resolveScopeSelection loads what the scope needs from the database: the
// membership of the selected groups, or nothing at all for a tag scope (a tag is
// free form, it has no table to read).
func (s *MonitorTemplateService) resolveScopeSelection(ctx context.Context, scope models.TemplateLinkScope) (scopeSelection, error) {
	switch scope.Kind {
	case models.TemplateScopeGroups:
		members, err := s.groupMembersByUUID(ctx, scope.GroupUUIDs)
		if err != nil {
			return scopeSelection{}, err
		}
		return scopeSelection{members: members}, nil
	case models.TemplateScopeTags:
		return scopeSelection{tags: scope.Tags}, nil
	default:
		return scopeSelection{}, nil
	}
}

// storeLinkScope persists the scope of a run on the template.
//
// The write carries the same revision bump as the rest of the update path and is
// published to the peers in the same transaction: the scope decides who follows
// the template, so a node that knows the template without its scope would answer
// the dialog with the wrong selection and detach the wrong monitors on the next
// run.
func (s *MonitorTemplateService) storeLinkScope(ctx context.Context, template *models.MonitorTemplate, scope models.TemplateLinkScope) error {
	scopeJSON, err := json.Marshal(scope)
	if err != nil {
		return ErrInternal(fmt.Errorf("encoding the link scope of monitor template %d: %w", template.ID, err))
	}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		result := tx.Model(&models.MonitorTemplate{}).Where("id = ?", template.ID).Updates(map[string]any{
			"link_scope": string(scopeJSON),
			"revision":   gorm.Expr("revision + 1"),
			"updated_at": time.Now().UTC(),
		})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return gorm.ErrRecordNotFound
		}
		return s.publishTemplate(ctx, tx, template.ID)
	})
	if err != nil {
		return ErrInternal(fmt.Errorf("storing the link scope of monitor template %d: %w", template.ID, err))
	}
	template.LinkScope = scope
	return nil
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
// without clicking through every monitor. The scope is every monitor of the
// template type, the monitors of the selected groups or the monitors carrying the
// selected tags. With DryRun it only reports what would happen, so the UI can ask
// for a confirmation first.
//
// The scope of a run is the scope of the template: a run with an outside (groups,
// tags) also detaches the monitors that follow the template from outside the
// selection (see unlinkOutOfScope), and the scope itself is persisted on the
// template so the dialog reopens on it.
func (s *MonitorTemplateService) LinkAll(ctx context.Context, opts LinkOptions) (*TemplateLinkResult, error) {
	if s.mono == nil {
		return nil, fmt.Errorf("the monitor service is not wired into the template service")
	}
	template, err := s.Get(ctx, opts.TemplateID)
	if err != nil {
		return nil, err
	}
	scope, err := s.prepareLinkScope(ctx, opts)
	if err != nil {
		return nil, err
	}
	selection, err := s.resolveScopeSelection(ctx, scope)
	if err != nil {
		return nil, err
	}
	var monitors []*models.Monitor
	if err := s.db.WithContext(ctx).Where("type = ?", template.Type).
		Order("id ASC").Find(&monitors).Error; err != nil {
		return nil, ErrInternal(fmt.Errorf("listing the %s monitors: %w", template.Type, err))
	}
	monitors = scopedMonitors(monitors, selection)
	fields := propagationFields(template)
	// A dry run reports the scope it would store, so the dialog shows the
	// operator the selection that the confirmation is about to remember.
	result := &TemplateLinkResult{Monitors: len(monitors), DryRun: opts.DryRun, Scope: scope}
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
	// The scope of the run replaces the scope of the template: a run with an
	// outside (groups, tags) detaches the monitors that follow the template from
	// outside the selection, so the operator ends up with exactly the monitors
	// the dialog previewed. The type scope has no outside and never detaches.
	if scope.HasOutside() {
		unlinked, err := s.unlinkOutOfScope(ctx, template, monitors, opts.DryRun)
		if err != nil {
			return nil, err
		}
		result.Unlinked = unlinked
	}
	if !opts.DryRun {
		// The scope is stored last, once the run itself went through: a request
		// that failed halfway must not leave the template governed by a scope
		// whose monitors were never linked.
		if err := s.storeLinkScope(ctx, template, scope); err != nil {
			return nil, err
		}
		s.log.Info("monitors linked to a template",
			"template", template.ID, "name", template.Name, "type", template.Type,
			"scope", scope.Kind, "groups", len(scope.GroupUUIDs), "tags", len(scope.Tags),
			"monitors", result.Monitors,
			"linked", result.Linked, "updated", result.Updated, "unlinked", result.Unlinked)
		s.publish("monitor.template.linked", map[string]any{
			"template_id": template.ID, "scope": string(scope.Kind),
			"linked":  result.Linked,
			"updated": result.Updated, "unlinked": result.Unlinked,
		})
	}
	return result, nil
}
