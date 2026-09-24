package services

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/ivancarlosti/up/internal/models"
)

// Applying a container entity: a group, a template, a status page, a channel.
//
// Every one of them follows the same three steps as a monitor: ask the merge rule
// what to do (mergeDecision), write the local row — or drop it — and record the
// identity this node now holds (trackApplied). Only the payload shape differs: a
// template and a status page name other rows, so those references are resolved
// through sync_objects, and a reference that has not arrived yet becomes a pending
// link instead of a dangling local id.
//
// Nothing here calls a service method: a service opens its own transaction on
// another connection, which would break the atomicity between the applied change
// and the cursor that records it — and it would re-publish what was just applied.

// maxNameAttempts bounds the search for a free name on a collision. Past a handful
// of suffixes the situation is not a collision any more but a loop, and a silent
// loop is worse than a reported failure.
const maxNameAttempts = 20

// applyGroup merges one group change.
//
// Its memberships are separate entities (monitor_group_member), so they are not
// touched here: a membership arriving on its own is applied on its own, and a
// group delete removes the memberships it still had.
func (s *SyncService) applyGroup(ctx context.Context, tx *gorm.DB, change models.SyncChangePayload) (models.ApplyDecision, error) {
	decision, current, err := s.mergeDecision(ctx, tx, change)
	if err != nil {
		return models.ApplySkip, err
	}
	if decision == models.ApplySkip {
		return decision, nil
	}

	if decision == models.ApplyDelete {
		if current != nil && current.LocalID != 0 {
			// The memberships go with the group, for the same reason a channel removes
			// its links: the batch applies containers before relations, so the relation
			// tombstones would find their target already tombstoned.
			if err := deleteGroupCascade(tx, current.LocalID); err != nil {
				return decision, err
			}
		}
		return decision, s.trackApplied(ctx, tx, change, 0, true)
	}

	var payload models.MonitorGroupPayload
	if err := json.Unmarshal(change.Payload, &payload); err != nil {
		return models.ApplySkip, fmt.Errorf("unreadable group payload for %s: %w", change.UUID, err)
	}
	localID, created, err := s.writeGroup(ctx, tx, payload, current)
	if err != nil {
		return models.ApplySkip, err
	}
	if err := s.trackApplied(ctx, tx, change, localID, false); err != nil {
		return decision, err
	}
	event := "monitor.group.updated"
	if created {
		event = "monitor.group.created"
	}
	s.publish(event, map[string]any{"id": localID, "uuid": change.UUID, "origin": change.OriginNodeID})
	return decision, nil
}

// deleteGroupCascade removes a group and its memberships, exactly as the local
// delete does. The monitors themselves are untouched.
//
// The status page selection is cleaned too: a group can be a section of a page, and a
// dangling link there would survive the group it points at (the same cleanup the local
// delete performs, so both nodes end up with the same rows).
func deleteGroupCascade(tx *gorm.DB, id uint) error {
	if err := tx.Where("group_id = ?", id).Delete(&models.MonitorGroupMember{}).Error; err != nil {
		return fmt.Errorf("removing the memberships of group %d: %w", id, err)
	}
	if err := tx.Where("group_id = ?", id).Delete(&models.StatusPageGroupLink{}).Error; err != nil {
		return fmt.Errorf("removing the status page links of group %d: %w", id, err)
	}
	if err := tx.Delete(&models.MonitorGroup{}, id).Error; err != nil {
		return fmt.Errorf("removing group %d: %w", id, err)
	}
	return nil
}

// writeGroup inserts or refreshes the local row of a group.
func (s *SyncService) writeGroup(ctx context.Context, tx *gorm.DB, payload models.MonitorGroupPayload, current *models.SyncObject) (uint, bool, error) {
	var localID uint
	if current != nil {
		localID = current.LocalID
	}
	name, err := s.uniqueName(ctx, tx, &models.MonitorGroup{}, "name", payload.Name, localID, 150)
	if err != nil {
		return 0, false, err
	}
	columns := map[string]any{
		"uuid":           payload.UUID,
		"origin_node_id": payload.OriginNodeID,
		"revision":       payload.Revision,
		"name":           name,
		"description":    payload.Description,
		"color":          payload.Color,
		"sort_order":     payload.SortOrder,
		"updated_at":     payload.UpdatedAt,
	}

	if localID != 0 {
		if err := tx.WithContext(ctx).Model(&models.MonitorGroup{}).
			Where("id = ?", localID).Updates(columns).Error; err != nil {
			return 0, false, fmt.Errorf("updating group %s: %w", payload.UUID, err)
		}
		return localID, false, nil
	}

	row := models.MonitorGroup{
		UUID:         payload.UUID,
		OriginNodeID: payload.OriginNodeID,
		Revision:     payload.Revision,
		Name:         name,
		UpdatedAt:    payload.UpdatedAt,
	}
	if err := tx.WithContext(ctx).Create(&row).Error; err != nil {
		return 0, false, fmt.Errorf("creating group %s: %w", payload.UUID, err)
	}
	if err := tx.WithContext(ctx).Model(&models.MonitorGroup{}).
		Where("id = ?", row.ID).Updates(columns).Error; err != nil {
		return 0, false, fmt.Errorf("storing group %s: %w", payload.UUID, err)
	}
	return row.ID, true, nil
}

// uniqueName resolves a name that another row already holds.
//
// A name is unique per table for groups, templates and status pages (the slug for
// a page), so an arriving row can collide with one the operator created locally.
// The LOCAL row keeps its name: it is the operator's own data, and synchronisation
// must not rename it behind their back. The arriving row is the one that gives way,
// with a deterministic suffix, so the change still lands — the identity converges,
// the cursor advances, and no change is ever blocked by a name.
//
// The suffix depends only on what is taken locally, so it is stable: re-applying
// the same change produces the same name, and the row does not drift between
// passes.
func (s *SyncService) uniqueName(ctx context.Context, tx *gorm.DB, model any, column, name string, excludeID uint, maxLen int) (string, error) {
	if name == "" {
		return name, nil
	}
	base := name
	if len(name) > maxLen {
		// Leave room for the suffix itself, counted in runes: the column is sized
		// in characters, and cutting mid-rune would produce invalid UTF-8.
		base = string([]rune(name)[:maxLen-8])
	}

	for attempt := 0; attempt < maxNameAttempts; attempt++ {
		candidate := base
		if attempt > 0 {
			candidate = fmt.Sprintf("%s (%d)", base, attempt+1)
		}
		query := tx.WithContext(ctx).Model(model).Where(column+" = ?", candidate)
		if excludeID != 0 {
			query = query.Where("id <> ?", excludeID)
		}
		var count int64
		if err := query.Count(&count).Error; err != nil {
			return "", fmt.Errorf("checking whether %q is free: %w", candidate, err)
		}
		if count == 0 {
			if attempt > 0 {
				s.log.Warn("a name was already taken locally: the arriving row landed under a suffixed name",
					"wanted", name, "stored", candidate)
			}
			return candidate, nil
		}
	}
	return "", fmt.Errorf("no free name for %q after %d attempts", name, maxNameAttempts)
}

// resolveLinks maps uuids to local ids, recording every uuid that has not arrived
// yet as a pending link.
//
// A reference is never left dangling: one that cannot be resolved is left out of
// the local row (the rest of the row still lands) and reported, so the healing pass
// re-delivers the container once the target exists.
func (s *SyncService) resolveLinks(ctx context.Context, tx *gorm.DB, containerEntity, containerUUID, kind, entity string, uuids []string) ([]uint, error) {
	ids := make([]uint, 0, len(uuids))
	seen := make(map[uint]bool, len(uuids))
	for _, uuid := range uuids {
		if strings.TrimSpace(uuid) == "" {
			continue
		}
		id, ok, err := s.localID(ctx, tx, entity, uuid)
		if err != nil {
			return nil, err
		}
		if !ok {
			if err := s.recordPendingLink(ctx, tx, containerEntity, containerUUID, kind, uuid); err != nil {
				return nil, err
			}
			continue
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		ids = append(ids, id)
	}
	return ids, nil
}

// applyTemplate merges one template change.
//
// A template names groups and channels in its defaults, so those references are
// resolved here: they travel as uuids and are stored as local ids, because they are
// applied to a monitor when the template is used.
func (s *SyncService) applyTemplate(ctx context.Context, tx *gorm.DB, change models.SyncChangePayload) (models.ApplyDecision, error) {
	decision, current, err := s.mergeDecision(ctx, tx, change)
	if err != nil {
		return models.ApplySkip, err
	}
	if decision == models.ApplySkip {
		return decision, nil
	}

	if decision == models.ApplyDelete {
		if current != nil && current.LocalID != 0 {
			if err := tx.WithContext(ctx).Delete(&models.MonitorTemplate{}, current.LocalID).Error; err != nil {
				return decision, fmt.Errorf("removing template %d: %w", current.LocalID, err)
			}
		}
		return decision, s.trackApplied(ctx, tx, change, 0, true)
	}

	var payload models.MonitorTemplatePayload
	if err := json.Unmarshal(change.Payload, &payload); err != nil {
		return models.ApplySkip, fmt.Errorf("unreadable template payload for %s: %w", change.UUID, err)
	}
	var localID uint
	if current != nil {
		localID = current.LocalID
	}
	localID, created, err := s.writeTemplate(ctx, tx, payload, localID)
	if err != nil {
		return models.ApplySkip, err
	}
	if err := s.trackApplied(ctx, tx, change, localID, false); err != nil {
		return decision, err
	}
	// The template is written, so whatever reference it was waiting for has either
	// arrived (the marker goes) or is still missing (the marker stays and counts).
	if err := s.clearResolvedPendingLinks(ctx, tx, change.Entity, change.UUID); err != nil {
		return decision, err
	}
	event := "monitor.template.updated"
	if created {
		event = "monitor.template.created"
	}
	s.publish(event, map[string]any{"id": localID, "uuid": change.UUID, "origin": change.OriginNodeID})
	return decision, nil
}

// writeTemplate inserts or refreshes the local row of a template.
func (s *SyncService) writeTemplate(ctx context.Context, tx *gorm.DB, payload models.MonitorTemplatePayload, localID uint) (uint, bool, error) {
	groupIDs, err := s.resolveLinks(ctx, tx,
		models.EntityMonitorTemplate, payload.UUID, "group_uuids", models.EntityMonitorGroup, payload.GroupUUIDs)
	if err != nil {
		return 0, false, err
	}
	notificationIDs, err := s.resolveLinks(ctx, tx,
		models.EntityMonitorTemplate, payload.UUID, "notification_uuids", models.EntityNotification, payload.NotificationUUIDs)
	if err != nil {
		return 0, false, err
	}
	name, err := s.uniqueName(ctx, tx, &models.MonitorTemplate{}, "name", payload.Name, localID, 150)
	if err != nil {
		return 0, false, err
	}

	// The defaults keep their links as local ids; they travel as uuids and are
	// resolved above.
	defaults := payload.Defaults
	defaults.GroupIDs = groupIDs
	defaults.NotificationIDs = notificationIDs

	if localID != 0 {
		defaultsJSON, err := json.Marshal(defaults)
		if err != nil {
			return 0, false, fmt.Errorf("encoding the defaults of template %s: %w", payload.UUID, err)
		}
		// The config is written as a marshalled JSON string: GORM applies the
		// serializer to model fields, not to raw map values.
		configJSON, err := json.Marshal(payload.Config)
		if err != nil {
			return 0, false, fmt.Errorf("encoding the config of template %s: %w", payload.UUID, err)
		}
		columns := map[string]any{
			"uuid":           payload.UUID,
			"origin_node_id": payload.OriginNodeID,
			"revision":       payload.Revision,
			"name":           name,
			"description":    payload.Description,
			"type":           payload.Type,
			"config":         string(configJSON),
			"defaults":       string(defaultsJSON),
			"updated_at":     payload.UpdatedAt,
		}
		if err := tx.WithContext(ctx).Model(&models.MonitorTemplate{}).
			Where("id = ?", localID).Updates(columns).Error; err != nil {
			return 0, false, fmt.Errorf("updating template %s: %w", payload.UUID, err)
		}
		return localID, false, nil
	}

	row := models.MonitorTemplate{
		UUID:         payload.UUID,
		OriginNodeID: payload.OriginNodeID,
		Revision:     payload.Revision,
		Name:         name,
		Description:  payload.Description,
		Type:         payload.Type,
		Config:       payload.Config,
		Defaults:     defaults,
		UpdatedAt:    payload.UpdatedAt,
	}
	if err := tx.WithContext(ctx).Create(&row).Error; err != nil {
		return 0, false, fmt.Errorf("creating template %s: %w", payload.UUID, err)
	}
	return row.ID, true, nil
}

// applyNotification merges one delivery-channel change.
//
// Its links to monitors are separate entities (monitor_notification), so they are
// not touched here.
func (s *SyncService) applyNotification(ctx context.Context, tx *gorm.DB, change models.SyncChangePayload) (models.ApplyDecision, error) {
	decision, current, err := s.mergeDecision(ctx, tx, change)
	if err != nil {
		return models.ApplySkip, err
	}
	if decision == models.ApplySkip {
		return decision, nil
	}

	if decision == models.ApplyDelete {
		if current != nil && current.LocalID != 0 {
			// The links go with the channel. They are separate entities with their own
			// tombstones, but those arrive in the same batch as this one and the batch
			// applies containers BEFORE relations, so a link removal would find its
			// target already tombstoned and leave the row behind for ever.
			if err := tx.WithContext(ctx).Where("notification_id = ?", current.LocalID).
				Delete(&models.MonitorNotification{}).Error; err != nil {
				return decision, fmt.Errorf("removing the links of channel %d: %w", current.LocalID, err)
			}
			if err := tx.WithContext(ctx).Delete(&models.Notification{}, current.LocalID).Error; err != nil {
				return decision, fmt.Errorf("removing channel %d: %w", current.LocalID, err)
			}
		}
		return decision, s.trackApplied(ctx, tx, change, 0, true)
	}

	var payload models.NotificationPayload
	if err := json.Unmarshal(change.Payload, &payload); err != nil {
		return models.ApplySkip, fmt.Errorf("unreadable channel payload for %s: %w", change.UUID, err)
	}
	var localID uint
	if current != nil {
		localID = current.LocalID
	}
	localID, created, err := s.writeNotification(ctx, tx, payload, localID)
	if err != nil {
		return models.ApplySkip, err
	}
	if err := s.trackApplied(ctx, tx, change, localID, false); err != nil {
		return decision, err
	}
	event := "notification.updated"
	if created {
		event = "notification.created"
	}
	s.publish(event, map[string]any{"id": localID, "uuid": change.UUID, "origin": change.OriginNodeID})
	return decision, nil
}

// writeNotification inserts or refreshes the local row of a channel.
func (s *SyncService) writeNotification(ctx context.Context, tx *gorm.DB, payload models.NotificationPayload, localID uint) (uint, bool, error) {
	// A channel name is not unique, so unlike a group or a page there is nothing to
	// resolve here.
	if localID != 0 {
		configJSON, err := json.Marshal(payload.Config)
		if err != nil {
			return 0, false, fmt.Errorf("encoding the config of channel %s: %w", payload.UUID, err)
		}
		columns := map[string]any{
			"uuid":                    payload.UUID,
			"origin_node_id":          payload.OriginNodeID,
			"revision":                payload.Revision,
			"name":                    payload.Name,
			"type":                    payload.Type,
			"active":                  payload.Active,
			"is_default":              payload.IsDefault,
			"resend_interval_seconds": payload.ResendIntervalSeconds,
			"config":                  string(configJSON),
			"updated_at":              payload.UpdatedAt,
		}
		if err := tx.WithContext(ctx).Model(&models.Notification{}).
			Where("id = ?", localID).Updates(columns).Error; err != nil {
			return 0, false, fmt.Errorf("updating channel %s: %w", payload.UUID, err)
		}
		return localID, false, nil
	}

	row := models.Notification{
		UUID:                  payload.UUID,
		OriginNodeID:          payload.OriginNodeID,
		Revision:              payload.Revision,
		Name:                  payload.Name,
		Type:                  payload.Type,
		Active:                payload.Active,
		IsDefault:             payload.IsDefault,
		ResendIntervalSeconds: payload.ResendIntervalSeconds,
		Config:                payload.Config,
		UpdatedAt:             payload.UpdatedAt,
	}
	if err := tx.WithContext(ctx).Create(&row).Error; err != nil {
		return 0, false, fmt.Errorf("creating channel %s: %w", payload.UUID, err)
	}
	return row.ID, true, nil
}

// applyStatusPage merges one status page change, its selection included.
//
// The selection is part of the payload rather than a separate entity (unlike a
// group's members): it carries the display order and the per-item overrides, so
// rebuilding it from uuids alone would silently reset both.
func (s *SyncService) applyStatusPage(ctx context.Context, tx *gorm.DB, change models.SyncChangePayload) (models.ApplyDecision, error) {
	decision, current, err := s.mergeDecision(ctx, tx, change)
	if err != nil {
		return models.ApplySkip, err
	}
	if decision == models.ApplySkip {
		return decision, nil
	}

	if decision == models.ApplyDelete {
		if current != nil && current.LocalID != 0 {
			if err := deleteStatusPageSelection(tx, current.LocalID); err != nil {
				return decision, err
			}
			if err := tx.WithContext(ctx).Delete(&models.StatusPage{}, current.LocalID).Error; err != nil {
				return decision, fmt.Errorf("removing status page %d: %w", current.LocalID, err)
			}
		}
		return decision, s.trackApplied(ctx, tx, change, 0, true)
	}

	var payload models.StatusPagePayload
	if err := json.Unmarshal(change.Payload, &payload); err != nil {
		return models.ApplySkip, fmt.Errorf("unreadable status page payload for %s: %w", change.UUID, err)
	}
	var localID uint
	if current != nil {
		localID = current.LocalID
	}
	localID, _, err = s.writeStatusPage(ctx, tx, payload, localID)
	if err != nil {
		return models.ApplySkip, err
	}
	if err := s.trackApplied(ctx, tx, change, localID, false); err != nil {
		return decision, err
	}
	// The page and its selection are written, so a reference the page was waiting for
	// has either arrived (the marker goes) or is still missing (it stays and counts).
	if err := s.clearResolvedPendingLinks(ctx, tx, change.Entity, change.UUID); err != nil {
		return decision, err
	}
	// A public page is rendered from the database on every request and this service
	// has no event hub, so unlike a monitor there is nothing to broadcast here.
	return decision, nil
}

// writeStatusPage inserts or refreshes the local row of a page and its selection.
func (s *SyncService) writeStatusPage(ctx context.Context, tx *gorm.DB, payload models.StatusPagePayload, localID uint) (uint, bool, error) {
	slug, err := s.uniqueName(ctx, tx, &models.StatusPage{}, "slug", payload.Slug, localID, 120)
	if err != nil {
		return 0, false, err
	}
	created := localID == 0
	if created {
		row := models.StatusPage{
			UUID:         payload.UUID,
			OriginNodeID: payload.OriginNodeID,
			Revision:     payload.Revision,
			Slug:         slug,
			UpdatedAt:    payload.UpdatedAt,
		}
		if err := tx.WithContext(ctx).Create(&row).Error; err != nil {
			return 0, false, fmt.Errorf("creating status page %s: %w", payload.UUID, err)
		}
		localID = row.ID
	}
	columns := map[string]any{
		"uuid":           payload.UUID,
		"origin_node_id": payload.OriginNodeID,
		"revision":       payload.Revision,
		"slug":           slug,
		"title":          payload.Title,
		"description":    payload.Description,
		"footer_text":    payload.FooterText,
		"theme":          payload.Theme,
		"is_public":      payload.IsPublic,
		"show_uptime":    payload.ShowUptime,
		"show_charts":    payload.ShowCharts,
		"show_tags":      payload.ShowTags,
		"show_expiry":    payload.ShowExpiry,
		"custom_css":     payload.CustomCSS,
		"updated_at":     payload.UpdatedAt,
	}
	if err := tx.WithContext(ctx).Model(&models.StatusPage{}).
		Where("id = ?", localID).Updates(columns).Error; err != nil {
		return 0, false, fmt.Errorf("storing status page %s: %w", payload.UUID, err)
	}
	if err := s.writeStatusPageSelection(ctx, tx, payload, localID); err != nil {
		return 0, false, err
	}
	return localID, created, nil
}

// deleteStatusPageSelection removes the selection of a page.
func deleteStatusPageSelection(tx *gorm.DB, pageID uint) error {
	for _, model := range []any{&models.StatusPageMonitor{}, &models.StatusPageGroupLink{}} {
		if err := tx.Where("status_page_id = ?", pageID).Delete(model).Error; err != nil {
			return fmt.Errorf("removing the selection of status page %d: %w", pageID, err)
		}
	}
	return nil
}

// writeStatusPageSelection rebuilds the selection of a page from the payload.
//
// The order and the per-item overrides come from the payload as they are: they are
// the page the operator built, and recomputing them would quietly change it.
func (s *SyncService) writeStatusPageSelection(ctx context.Context, tx *gorm.DB, payload models.StatusPagePayload, pageID uint) error {
	if err := deleteStatusPageSelection(tx, pageID); err != nil {
		return err
	}
	for _, item := range payload.Monitors {
		monitorID, ok, err := s.localID(ctx, tx, models.EntityMonitor, item.UUID)
		if err != nil {
			return err
		}
		if !ok {
			if err := s.recordPendingLink(ctx, tx,
				models.EntityStatusPage, payload.UUID, "monitor_uuids", item.UUID); err != nil {
				return err
			}
			continue
		}
		link := models.StatusPageMonitor{
			StatusPageID: pageID,
			MonitorID:    monitorID,
			DisplayName:  item.DisplayName,
			GroupName:    item.GroupName,
			SortOrder:    item.SortOrder,
			ShowUptime:   item.ShowUptime,
			ShowChart:    item.ShowChart,
		}
		if err := tx.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&link).Error; err != nil {
			return fmt.Errorf("storing the selection of status page %s: %w", payload.UUID, err)
		}
	}
	for _, group := range payload.Groups {
		groupID, ok, err := s.localID(ctx, tx, models.EntityMonitorGroup, group.UUID)
		if err != nil {
			return err
		}
		if !ok {
			if err := s.recordPendingLink(ctx, tx,
				models.EntityStatusPage, payload.UUID, "group_uuids", group.UUID); err != nil {
				return err
			}
			continue
		}
		link := models.StatusPageGroupLink{
			StatusPageID: pageID,
			GroupID:      groupID,
			DisplayName:  group.DisplayName,
			SortOrder:    group.SortOrder,
		}
		if err := tx.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&link).Error; err != nil {
			return fmt.Errorf("storing the group selection of status page %s: %w", payload.UUID, err)
		}
	}
	return nil
}

// resolvePendingLinks materialises the references that were missing when their
// container was last applied.
//
// Re-applying the container is NOT an option, which is what the first design got
// wrong: the container arrives at the revision this node already holds, so the merge
// rule skips it — as it must, otherwise every re-delivery would look like a new
// version. The reference is therefore resolved as LOCAL work: the container's stored
// payload is written again, without touching its identity, so the join rows appear as
// soon as their target exists.
//
// The payload used is the one stored beside the identity (models.SyncObject.Payload),
// so this works for a container that arrived through a change or through a snapshot,
// and it needs no collaboration from the peer.
func (s *SyncService) resolvePendingLinks(ctx context.Context, tx *gorm.DB) error {
	var markers []models.SyncPendingLink
	if err := tx.WithContext(ctx).Where("attempts < ?", maxPendingLinkAttempts).
		Find(&markers).Error; err != nil {
		return ErrInternal(err)
	}
	if len(markers) == 0 {
		return nil
	}

	// A container can have several missing references: it is written once, after the
	// last of its markers has been looked at.
	containers := map[string]models.SyncPendingLink{}
	for _, marker := range markers {
		target := pendingLinkTarget(marker.Entity, marker.Kind)
		if target == "" {
			continue
		}
		_, resolved, err := s.localID(ctx, tx, target, marker.MissingUUID)
		if err != nil {
			return err
		}
		if resolved {
			containers[marker.Entity+"|"+marker.UUID] = marker
		}
	}
	for _, marker := range containers {
		if err := s.resolveContainerReferences(ctx, tx, marker); err != nil {
			return err
		}
	}
	return nil
}

// resolveContainerReferences writes a container's payload again, so the references
// that have arrived since the last attempt become real rows.
func (s *SyncService) resolveContainerReferences(ctx context.Context, tx *gorm.DB, marker models.SyncPendingLink) error {
	container, err := s.loadSyncObject(ctx, tx, marker.UUID)
	if err != nil {
		return err
	}
	if container == nil || container.Payload == "" || container.LocalID == 0 {
		// The container itself is not here (or is a tombstone): there is nothing to
		// resolve against, so the marker stays until the attempt cap.
		return nil
	}

	switch marker.Entity {
	case models.EntityStatusPage:
		var payload models.StatusPagePayload
		if err := json.Unmarshal([]byte(container.Payload), &payload); err != nil {
			return fmt.Errorf("unreadable stored status page payload for %s: %w", marker.UUID, err)
		}
		if err := s.writeStatusPageSelection(ctx, tx, payload, container.LocalID); err != nil {
			return err
		}
	case models.EntityMonitorTemplate:
		var payload models.MonitorTemplatePayload
		if err := json.Unmarshal([]byte(container.Payload), &payload); err != nil {
			return fmt.Errorf("unreadable stored template payload for %s: %w", marker.UUID, err)
		}
		if _, _, err := s.writeTemplate(ctx, tx, payload, container.LocalID); err != nil {
			return err
		}
	case models.EntityMonitorGroupMember, models.EntityMonitorNotification:
		// A relation carries its two ends in its own stored payload: the uuid is a
		// hash of them, so the payload is the only way back to the pair.
		change := models.SyncChangePayload{
			Entity:  marker.Entity,
			UUID:    marker.UUID,
			Payload: json.RawMessage(container.Payload),
		}
		monitorUUID, targetUUID, err := s.relationEnds(ctx, tx, change)
		if err != nil {
			return err
		}
		if _, err := s.linkRelation(ctx, tx, marker.Entity, monitorUUID, targetUUID); err != nil {
			return err
		}
	default:
		return nil
	}
	return s.clearResolvedPendingLinks(ctx, tx, marker.Entity, marker.UUID)
}
