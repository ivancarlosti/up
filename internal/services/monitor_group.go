package services

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/ivancarlosti/up/internal/config"
	"github.com/ivancarlosti/up/internal/i18n"
	"github.com/ivancarlosti/up/internal/models"
)

// MonitorGroupService owns the monitor groups: the named collections used to
// organise the monitor list and to publish a set of monitors on a status page.
type MonitorGroupService struct {
	db   *gorm.DB
	cfg  *config.Config
	log  *slog.Logger
	hub  EventPublisher
	mono *MonitorService
	// emit publishes the local writes to the peers. It is nil on a node that does
	// not publish (anything but federated mode), and every helper below is a no-op
	// in that case.
	emit *SyncEmitter
}

// NewMonitorGroupService builds the group service.
func NewMonitorGroupService(db *gorm.DB, cfg *config.Config, log *slog.Logger) *MonitorGroupService {
	return &MonitorGroupService{db: db, cfg: cfg, log: log}
}

// SetPublisher injects the real time publisher.
func (s *MonitorGroupService) SetPublisher(p EventPublisher) { s.hub = p }

// SetMonitorService injects the monitor service (used by the deep group clone).
func (s *MonitorGroupService) SetMonitorService(m *MonitorService) { s.mono = m }

// SetSyncEmitter injects the publisher of the synchronisation outbox.
func (s *MonitorGroupService) SetSyncEmitter(emitter *SyncEmitter) { s.emit = emitter }

// snapshotMembers reads the members of a group before they are rewritten.
func (s *MonitorGroupService) snapshotMembers(ctx context.Context, tx *gorm.DB, id uint) (GroupLinks, error) {
	if s.emit == nil {
		return EmptyGroupLinks(), nil
	}
	return s.emit.SnapshotGroupLinks(ctx, tx, id)
}

// publishGroup records the group and the memberships it now has, compared with the
// state captured before the write. Both writes happen inside the caller's
// transaction, so the outbox can never drift from the data.
//
// The group editor is the second place a membership can change (the monitor form is
// the first), which is why the diff is taken from the group side too: a membership
// must have exactly one writer per edit, and this is that writer for this path.
func (s *MonitorGroupService) publishGroup(ctx context.Context, tx *gorm.DB, id uint, before GroupLinks) error {
	if s.emit == nil {
		return nil
	}
	if err := s.emit.EmitMonitorGroup(ctx, tx, id); err != nil {
		return err
	}
	return s.emit.PublishGroupLinks(ctx, tx, id, before)
}

// groupUUID reads the global identity of a group (needed before a delete).
func (s *MonitorGroupService) groupUUID(ctx context.Context, tx *gorm.DB, id uint) (string, error) {
	if s.emit == nil {
		return "", nil
	}
	return s.emit.RowUUID(ctx, tx, models.EntityMonitorGroup, id)
}

// tombstoneGroup publishes the removal of a group and of its memberships. The
// memberships go first: the receiver deletes the group with its cascade, so the
// relation tombstones would otherwise refer to rows that are already gone.
func (s *MonitorGroupService) tombstoneGroup(ctx context.Context, tx *gorm.DB, uuid string, members GroupLinks) error {
	if s.emit == nil || uuid == "" {
		return nil
	}
	if err := s.emit.TombstoneGroupLinks(ctx, tx, uuid, members); err != nil {
		return err
	}
	return s.emit.EmitDelete(ctx, tx, models.EntityMonitorGroup, uuid)
}

func (s *MonitorGroupService) publish(event string, payload any) {
	if s.hub != nil {
		s.hub.Publish(event, payload)
	}
}

// MonitorGroupCloneOptions controls a group clone.
type MonitorGroupCloneOptions struct {
	// Name of the new group (empty means "<source> (copy)").
	Name string
	// Deep also clones every monitor of the group.
	Deep bool
	// CopyLinks keeps the notification channels and the groups of each cloned
	// monitor (only used with Deep).
	CopyLinks bool
}

// List returns every group with its monitor ids.
func (s *MonitorGroupService) List(ctx context.Context) ([]*models.MonitorGroup, error) {
	var groups []*models.MonitorGroup
	if err := s.db.WithContext(ctx).Order("sort_order ASC, name ASC").Find(&groups).Error; err != nil {
		return nil, ErrInternal(fmt.Errorf("listing monitor groups: %w", err))
	}
	if err := s.decorate(ctx, groups); err != nil {
		return nil, err
	}
	return groups, nil
}

// Get loads one group.
func (s *MonitorGroupService) Get(ctx context.Context, id uint) (*models.MonitorGroup, error) {
	var group models.MonitorGroup
	err := s.db.WithContext(ctx).First(&group, id).Error
	if err == gorm.ErrRecordNotFound {
		return nil, ErrNotFound(i18n.CodeMonitorGroupNotFound, fmt.Sprintf("monitor group %d does not exist", id))
	}
	if err != nil {
		return nil, ErrInternal(fmt.Errorf("loading monitor group %d: %w", id, err))
	}
	decorated := []*models.MonitorGroup{&group}
	if err := s.decorate(ctx, decorated); err != nil {
		return nil, err
	}
	return &group, nil
}

// MonitorIDs returns the ids of the monitors of a group.
func (s *MonitorGroupService) MonitorIDs(ctx context.Context, groupID uint) ([]uint, error) {
	var members []models.MonitorGroupMember
	if err := s.db.WithContext(ctx).Where("group_id = ?", groupID).
		Order("sort_order ASC, monitor_id ASC").Find(&members).Error; err != nil {
		return nil, ErrInternal(err)
	}
	ids := make([]uint, 0, len(members))
	for _, member := range members {
		ids = append(ids, member.MonitorID)
	}
	return ids, nil
}

// GroupsForMonitors returns the group ids of every given monitor (one query),
// used to decorate the monitor payloads.
func (s *MonitorGroupService) GroupsForMonitors(ctx context.Context, monitorIDs []uint) (map[uint][]uint, error) {
	out := map[uint][]uint{}
	if len(monitorIDs) == 0 {
		return out, nil
	}
	var members []models.MonitorGroupMember
	if err := s.db.WithContext(ctx).
		Where("monitor_id IN ?", monitorIDs).
		Order("group_id ASC").
		Find(&members).Error; err != nil {
		return nil, ErrInternal(err)
	}
	for _, member := range members {
		out[member.MonitorID] = append(out[member.MonitorID], member.GroupID)
	}
	return out, nil
}

// Create stores a group and its members.
func (s *MonitorGroupService) Create(ctx context.Context, group *models.MonitorGroup, monitorIDs []uint) error {
	group.Name = strings.TrimSpace(group.Name)
	if err := s.validate(ctx, group, 0); err != nil {
		return err
	}
	// The identity is stamped here rather than by a later update, so the row, the
	// payload and the response all name the same creator from the start.
	group.OriginNodeID = s.cfg.NodeID
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(group).Error; err != nil {
			return err
		}
		if err := replaceMonitorGroupMembers(tx, group.ID, monitorIDs); err != nil {
			return err
		}
		return s.publishGroup(ctx, tx, group.ID, EmptyGroupLinks())
	})
	if err != nil {
		return ErrInternal(fmt.Errorf("creating monitor group: %w", err))
	}
	s.log.Info("monitor group created", "id", group.ID, "name", group.Name, "monitors", len(monitorIDs))
	s.publish("monitor.group.created", group)
	return nil
}

// Update saves the editable fields of a group; a nil monitorIDs keeps the
// current members (same contract as the monitor notification links).
func (s *MonitorGroupService) Update(ctx context.Context, group *models.MonitorGroup, monitorIDs []uint) error {
	group.Name = strings.TrimSpace(group.Name)
	if err := s.validate(ctx, group, group.ID); err != nil {
		return err
	}
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		before, err := s.snapshotMembers(ctx, tx, group.ID)
		if err != nil {
			return err
		}
		result := tx.Model(&models.MonitorGroup{}).Where("id = ?", group.ID).Updates(map[string]any{
			"name":        group.Name,
			"description": group.Description,
			"color":       group.Color,
			"sort_order":  group.SortOrder,
			// Every edit advances the revision: it is the primary component of the
			// merge order, so a wrong clock cannot make an old edit win. It is
			// bumped by the database so two concurrent edits cannot both write the
			// same number.
			"revision":   gorm.Expr("revision + 1"),
			"updated_at": time.Now().UTC(),
		})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return gorm.ErrRecordNotFound
		}
		if monitorIDs != nil {
			if err := replaceMonitorGroupMembers(tx, group.ID, monitorIDs); err != nil {
				return err
			}
		}
		return s.publishGroup(ctx, tx, group.ID, before)
	})
	if err == gorm.ErrRecordNotFound {
		return ErrNotFound(i18n.CodeMonitorGroupNotFound, fmt.Sprintf("monitor group %d does not exist", group.ID))
	}
	if err != nil {
		return ErrInternal(fmt.Errorf("updating monitor group %d: %w", group.ID, err))
	}
	s.log.Info("monitor group updated", "id", group.ID, "name", group.Name)
	s.publish("monitor.group.updated", group)
	return nil
}

// SetMonitors rewrites the members of a group (used by the group editor).
func (s *MonitorGroupService) SetMonitors(ctx context.Context, id uint, monitorIDs []uint) (*models.MonitorGroup, error) {
	if _, err := s.Get(ctx, id); err != nil {
		return nil, err
	}
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		before, err := s.snapshotMembers(ctx, tx, id)
		if err != nil {
			return err
		}
		if err := replaceMonitorGroupMembers(tx, id, monitorIDs); err != nil {
			return err
		}
		// Only the members moved. A membership is its own entity with its own
		// revision, so the group ROW is not republished: that would add an outbox row
		// which every peer then skips as not newer (same revision, same timestamp) —
		// pure noise per save. publishGroup is for the paths where the row changed.
		if s.emit == nil {
			return nil
		}
		return s.emit.PublishGroupLinks(ctx, tx, id, before)
	}); err != nil {
		return nil, ErrInternal(fmt.Errorf("updating the members of group %d: %w", id, err))
	}
	group, err := s.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	s.log.Info("monitor group members updated", "id", id, "monitors", len(group.MonitorIDs))
	s.publish("monitor.group.updated", group)
	return group, nil
}

// Delete removes a group and its memberships (the monitors stay untouched).
//
// It is a hard delete on purpose: a soft deleted row would keep its name in the
// unique index, so deleting a group and creating it again with the same name
// would fail.
func (s *MonitorGroupService) Delete(ctx context.Context, id uint) error {
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// The identity and the members are read BEFORE the cascade: once the rows
		// are gone they cannot be read, and the tombstones have to name them.
		uuid, err := s.groupUUID(ctx, tx, id)
		if err != nil {
			return err
		}
		members, err := s.snapshotMembers(ctx, tx, id)
		if err != nil {
			return err
		}
		if err := tx.Where("group_id = ?", id).Delete(&models.MonitorGroupMember{}).Error; err != nil {
			return err
		}
		// A group can be a section of a status page, so its links go with it: leaving
		// them behind would point a page at a group that no longer exists (and a peer
		// applying the same delete would remove them, so the two nodes would differ).
		if err := tx.Where("group_id = ?", id).Delete(&models.StatusPageGroupLink{}).Error; err != nil {
			return err
		}
		result := tx.Delete(&models.MonitorGroup{}, id)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return gorm.ErrRecordNotFound
		}
		return s.tombstoneGroup(ctx, tx, uuid, members)
	})
	if err == gorm.ErrRecordNotFound {
		return ErrNotFound(i18n.CodeMonitorGroupNotFound, fmt.Sprintf("monitor group %d does not exist", id))
	}
	if err != nil {
		return ErrInternal(fmt.Errorf("deleting monitor group %d: %w", id, err))
	}
	s.log.Info("monitor group deleted", "id", id)
	s.publish("monitor.group.deleted", map[string]any{"id": id})
	return nil
}

// Clone copies a group, optionally cloning every monitor inside it.
func (s *MonitorGroupService) Clone(ctx context.Context, id uint, opts MonitorGroupCloneOptions) (*models.MonitorGroup, error) {
	source, err := s.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	name := strings.TrimSpace(opts.Name)
	if name == "" {
		name = source.Name + " (copy)"
	}
	unique, err := s.uniqueName(ctx, name)
	if err != nil {
		return nil, err
	}

	clone := &models.MonitorGroup{
		Name:        unique,
		Description: source.Description,
		Color:       source.Color,
		SortOrder:   source.SortOrder + 1,
	}

	// A shallow clone starts empty on purpose: two groups pointing at the very
	// same monitors is usually a mistake, not an intention.
	var monitorIDs []uint
	if opts.Deep && s.mono != nil {
		monitorIDs = make([]uint, 0, len(source.MonitorIDs))
		for _, monitorID := range source.MonitorIDs {
			cloned, cloneErr := s.mono.Clone(ctx, monitorID, MonitorCloneOptions{
				CopyNotifications: opts.CopyLinks,
				CopyGroups:        opts.CopyLinks,
			})
			if cloneErr != nil {
				return nil, cloneErr
			}
			monitorIDs = append(monitorIDs, cloned.ID)
		}
	}

	if err := s.Create(ctx, clone, monitorIDs); err != nil {
		return nil, err
	}
	s.log.Info("monitor group cloned",
		"source", id, "clone", clone.ID, "monitors", len(monitorIDs), "deep", opts.Deep)
	return s.Get(ctx, clone.ID)
}

// decorate fills MonitorIDs and MonitorCount.
func (s *MonitorGroupService) decorate(ctx context.Context, groups []*models.MonitorGroup) error {
	if len(groups) == 0 {
		return nil
	}
	ids := make([]uint, 0, len(groups))
	for _, group := range groups {
		ids = append(ids, group.ID)
	}
	var members []models.MonitorGroupMember
	if err := s.db.WithContext(ctx).Where("group_id IN ?", ids).
		Order("group_id ASC, sort_order ASC, monitor_id ASC").Find(&members).Error; err != nil {
		return ErrInternal(err)
	}
	byGroup := map[uint][]uint{}
	for _, member := range members {
		byGroup[member.GroupID] = append(byGroup[member.GroupID], member.MonitorID)
	}
	for _, group := range groups {
		group.MonitorIDs = byGroup[group.ID]
		if group.MonitorIDs == nil {
			group.MonitorIDs = []uint{}
		}
		group.MonitorCount = len(group.MonitorIDs)
	}
	return nil
}

// validate checks the name (required and unique, ignoring the edited group).
func (s *MonitorGroupService) validate(ctx context.Context, group *models.MonitorGroup, currentID uint) error {
	if group.Name == "" {
		return ErrBadRequest(i18n.CodeMonitorGroupInvalid, "name is required")
	}
	if len(group.Name) > 150 {
		return ErrBadRequest(i18n.CodeMonitorGroupInvalid, "name must be at most 150 characters")
	}
	var existing models.MonitorGroup
	query := s.db.WithContext(ctx).Where("LOWER(name) = LOWER(?)", group.Name)
	if currentID > 0 {
		query = query.Where("id <> ?", currentID)
	}
	switch err := query.First(&existing).Error; {
	case err == nil:
		return ErrConflict(i18n.CodeMonitorGroupInvalid,
			fmt.Sprintf("a group named %q already exists", existing.Name))
	case err != gorm.ErrRecordNotFound:
		return ErrInternal(err)
	}
	return nil
}

// uniqueName appends a counter until the name is free (used by the clone).
func (s *MonitorGroupService) uniqueName(ctx context.Context, base string) (string, error) {
	name := base
	for attempt := 2; attempt < 100; attempt++ {
		var count int64
		if err := s.db.WithContext(ctx).Model(&models.MonitorGroup{}).
			Where("LOWER(name) = LOWER(?)", name).Count(&count).Error; err != nil {
			return "", ErrInternal(err)
		}
		if count == 0 {
			return name, nil
		}
		name = fmt.Sprintf("%s (%d)", base, attempt)
	}
	return "", ErrConflict(i18n.CodeMonitorGroupInvalid,
		fmt.Sprintf("could not find a free name for %q", base))
}

// replaceMonitorGroupMembers rewrites the members of a group, validating that
// the monitors exist so a deleted monitor cannot leave a dangling membership.
func replaceMonitorGroupMembers(tx *gorm.DB, groupID uint, monitorIDs []uint) error {
	if monitorIDs != nil {
		ids := uniqueIDs(monitorIDs)
		if len(ids) > 0 {
			var found int64
			if err := tx.Model(&models.Monitor{}).Where("id IN ?", ids).Count(&found).Error; err != nil {
				return err
			}
			if int(found) != len(ids) {
				return ErrBadRequest(i18n.CodeMonitorGroupInvalid, "monitor_ids contains an unknown monitor")
			}
		}
	}
	if err := tx.Where("group_id = ?", groupID).Delete(&models.MonitorGroupMember{}).Error; err != nil {
		return err
	}
	order := 0
	for _, id := range uniqueIDs(monitorIDs) {
		order++
		member := models.MonitorGroupMember{GroupID: groupID, MonitorID: id, SortOrder: order}
		if err := tx.Create(&member).Error; err != nil {
			return err
		}
	}
	return nil
}

// validateGroupIDs checks that every group exists. A nil slice means "keep the
// current links" and is accepted without touching the database.
func validateGroupIDs(tx *gorm.DB, groupIDs []uint) error {
	if groupIDs == nil {
		return nil
	}
	ids := uniqueIDs(groupIDs)
	if len(ids) == 0 {
		return nil
	}
	var found int64
	if err := tx.Model(&models.MonitorGroup{}).Where("id IN ?", ids).Count(&found).Error; err != nil {
		return err
	}
	if int(found) != len(ids) {
		return ErrBadRequest(i18n.CodeMonitorGroupInvalid, "group_ids contains an unknown group")
	}
	return nil
}

// uniqueIDs drops the zeroes and the duplicates of an id list, keeping the order
// (the position is what the UI uses to sort the members).
func uniqueIDs(ids []uint) []uint {
	out := make([]uint, 0, len(ids))
	seen := map[uint]bool{}
	for _, id := range ids {
		if id == 0 || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out
}
