package services

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/ivancarlosti/up/internal/models"
)

// ApplyBatch applies the changes a peer published.
//
// Everything happens in ONE transaction, the cursor included: a crash mid-batch
// re-pulls instead of losing rows, and a batch that fails leaves the cursor
// untouched so the next cycle retries it.
//
// The changes are applied in two passes — rows first, then the relations between
// them — because the order inside a batch is not defined by the protocol: a
// membership may arrive before the monitor it points at.
func (s *SyncService) ApplyBatch(ctx context.Context, peerNodeID string, changes []models.SyncChangePayload) error {
	if len(changes) == 0 {
		return nil
	}
	// The apply must not publish. Without this the two nodes would publish each
	// other's changes back and forth forever.
	applying := WithApply(ctx)

	err := s.db.WithContext(applying).Transaction(func(tx *gorm.DB) error {
		applied, skipped, err := s.applyChanges(applying, tx, changes)
		if err != nil {
			return err
		}
		// The cursor is the id of the last change of the batch, applied or not: the
		// ones that were skipped were skipped for good (they are older than what
		// this node already has), so re-pulling them would never help.
		if err := s.advanceCursor(applying, tx, peerNodeID, int64(changes[len(changes)-1].ID)); err != nil {
			return err
		}
		s.log.Debug("applied a batch from a peer",
			"peer", peerNodeID, "changes", len(changes), "applied", applied, "skipped", skipped)
		return nil
	})
	if err != nil {
		return err
	}
	return nil
}

// applyChanges is the two-pass body shared by the incremental path and the full
// resync: rows first, then the relations between them, so the order inside a batch
// never matters (a membership may arrive before the monitor it points at).
//
// It is the SAME code in both paths on purpose: a bug that existed in only one of
// them would be found by the healing pass only after it had already diverged.
func (s *SyncService) applyChanges(ctx context.Context, tx *gorm.DB, changes []models.SyncChangePayload) (int, int, error) {
	applied, skipped := 0, 0

	relations := make([]models.SyncChangePayload, 0, len(changes))
	for _, change := range changes {
		if isRelation(change.Entity) {
			relations = append(relations, change)
			continue
		}
		decision, err := s.applyEntity(ctx, tx, change)
		if err != nil {
			return applied, skipped, err
		}
		if decision == models.ApplySkip {
			skipped++
		} else {
			applied++
		}
	}
	for _, change := range relations {
		decision, err := s.applyEntity(ctx, tx, change)
		if err != nil {
			return applied, skipped, err
		}
		if decision == models.ApplySkip {
			skipped++
		} else {
			applied++
		}
	}
	return applied, skipped, nil
}

// isRelation reports whether an entity is a relation between two rows (it has no
// row of its own to write in a first pass).
func isRelation(entity string) bool {
	return entity == models.EntityMonitorGroupMember || entity == models.EntityMonitorNotification
}

// applyEntity dispatches one change to the applier of its entity.
func (s *SyncService) applyEntity(ctx context.Context, tx *gorm.DB, change models.SyncChangePayload) (models.ApplyDecision, error) {
	switch change.Entity {
	case models.EntityMonitor:
		return s.applyMonitor(ctx, tx, change)
	case models.EntityMonitorGroupMember, models.EntityMonitorNotification:
		return s.applyRelation(ctx, tx, change)
	default:
		// An entity this build does not know (a newer peer). Skipping it keeps the
		// rest of the batch flowing, and the manifest pass reports the divergence
		// instead of leaving it silent.
		s.log.Warn("ignoring a change of an unsupported entity",
			"entity", change.Entity, "uuid", change.UUID, "protocol_version", models.ProtocolVersion)
		return models.ApplySkip, nil
	}
}

// advanceCursor records how far this node has consumed a peer's outbox.
func (s *SyncService) advanceCursor(ctx context.Context, tx *gorm.DB, peerNodeID string, cursor int64) error {
	if cursor <= 0 {
		return nil
	}
	return tx.WithContext(ctx).Model(&models.SyncPeer{}).
		Where("peer_node_id = ?", peerNodeID).
		Update("last_change_id", cursor).Error
}

// realignCursor moves this node's cursor outside of an apply transaction, for the
// case where the position has to jump rather than advance by one batch.
func (s *SyncService) realignCursor(ctx context.Context, peerNodeID string, cursor int64) error {
	if cursor <= 0 {
		return nil
	}
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return s.advanceCursor(ctx, tx, peerNodeID, cursor)
	}); err != nil {
		return ErrInternal(err)
	}
	return nil
}

// loadSyncObject reads what this node has already applied for a uuid.
func (s *SyncService) loadSyncObject(ctx context.Context, tx *gorm.DB, uuid string) (*models.SyncObject, error) {
	var object models.SyncObject
	err := tx.WithContext(ctx).Where("uuid = ?", uuid).First(&object).Error
	switch {
	case err == gorm.ErrRecordNotFound:
		return nil, nil
	case err != nil:
		return nil, fmt.Errorf("reading the sync identity of %s: %w", uuid, err)
	}
	return &object, nil
}

// trackApplied records the revision this node now holds for a uuid, so the next
// change is compared against it.
func (s *SyncService) trackApplied(ctx context.Context, tx *gorm.DB, change models.SyncChangePayload, localID uint, deleted bool) error {
	var deletedAt *time.Time
	if deleted {
		now := time.Now().UTC()
		deletedAt = &now
	}
	updatedAt := change.UpdatedAt
	if updatedAt.IsZero() {
		updatedAt = time.Now().UTC()
	}
	object := models.SyncObject{
		UUID:         change.UUID,
		Entity:       change.Entity,
		LocalID:      localID,
		OriginNodeID: change.OriginNodeID,
		Revision:     change.Revision,
		DeletedAt:    deletedAt,
		// The payload travels with the identity so that a full resync can be
		// enumerated from this table alone, tombstones included (see models.SyncObject).
		Payload: string(change.Payload),
		// The SENDER's timestamp, not the moment this node applied it: it is a
		// component of the merge order, so stamping it locally would make two nodes
		// compare the same change differently.
		UpdatedAt: updatedAt,
	}
	return tx.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "uuid"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"entity", "local_id", "origin_node_id", "revision", "deleted_at", "payload", "updated_at",
		}),
	}).Create(&object).Error
}

// applyMonitor merges one monitor change into the local database.
func (s *SyncService) applyMonitor(ctx context.Context, tx *gorm.DB, change models.SyncChangePayload) (models.ApplyDecision, error) {
	current, err := s.loadSyncObject(ctx, tx, change.UUID)
	if err != nil {
		return models.ApplySkip, err
	}

	incoming := models.SyncCandidate{
		Revision:     change.Revision,
		UpdatedAt:    change.UpdatedAt,
		OriginNodeID: change.OriginNodeID,
	}
	deleted := change.Action == models.ActionDelete
	decision := models.Decide(incoming, deleted, current)

	// A concurrent edit is reported whichever way the merge goes: when the incoming
	// change wins, the edit it replaces is the one that lost. A stale re-delivery
	// is not a conflict (see models.IsConflict) and stays silent.
	if models.IsConflict(incoming, current) {
		if err := s.recordConflict(ctx, tx, change, current, decision != models.ApplySkip); err != nil {
			return decision, err
		}
	}
	if decision == models.ApplySkip {
		return decision, nil
	}

	if decision == models.ApplyDelete {
		if current != nil && current.LocalID != 0 {
			if err := deleteMonitorCascade(tx, current.LocalID); err != nil {
				return decision, err
			}
		}
		// A delete for a row this node never had still records the tombstone, so a
		// late upsert cannot create it afterwards.
		return decision, s.trackApplied(ctx, tx, change, 0, true)
	}

	var payload models.MonitorPayload
	if err := json.Unmarshal(change.Payload, &payload); err != nil {
		return models.ApplySkip, fmt.Errorf("unreadable monitor payload for %s: %w", change.UUID, err)
	}
	// The envelope is authoritative for WHO produced this version: the payload copy
	// is rendered from the sender's row and may carry a stale or empty origin.
	if change.OriginNodeID != "" {
		payload.OriginNodeID = change.OriginNodeID
	}
	localID, created, err := s.writeMonitor(ctx, tx, payload, current)
	if err != nil {
		return models.ApplySkip, err
	}
	if err := s.trackApplied(ctx, tx, change, localID, false); err != nil {
		return decision, err
	}
	if err := s.ensureMonitorState(ctx, tx, localID); err != nil {
		return decision, err
	}
	// The dashboard and the status pages react exactly as they do to a local edit,
	// and the scheduler picks the monitor up on its next reconcile pass.
	event := "monitor.updated"
	if created {
		event = "monitor.created"
	}
	s.publish(event, map[string]any{"id": localID, "uuid": change.UUID, "origin": change.OriginNodeID})
	return decision, nil
}

// writeMonitor inserts or refreshes the local row of a monitor.
//
// The identity and the revision come from the payload, never from this node: an
// applied change keeps the sender's revision, or every hop would look like a new
// edit and the merge order would collapse.
func (s *SyncService) writeMonitor(ctx context.Context, tx *gorm.DB, payload models.MonitorPayload, current *models.SyncObject) (uint, bool, error) {
	// One map for both paths, so the INSERT and the UPDATE can never write a
	// different set of columns.
	columns := map[string]any{
		"uuid":                     payload.UUID,
		"origin_node_id":           payload.OriginNodeID,
		"revision":                 payload.Revision,
		"name":                     payload.Name,
		"type":                     payload.Type,
		"active":                   payload.Active,
		"description":              payload.Description,
		"interval_seconds":         payload.IntervalSeconds,
		"retries":                  payload.Retries,
		"retries_interval_seconds": payload.RetriesIntervalSeconds,
		"timeout_seconds":          payload.TimeoutSeconds,
		"resend_interval_seconds":  payload.ResendIntervalSeconds,
		"upside_down":              payload.UpsideDown,
		"run_on":                   payload.RunOn,
		"node_id":                  payload.NodeID,
		"tags":                     payload.Tags,
		"cert_watch":               payload.CertWatch,
		"cert_notify":              payload.CertNotify,
		"cert_warn_days":           payload.CertWarnDays,
		"updated_at":               payload.UpdatedAt,
	}
	// The config column is stored as JSON, like MonitorService writes it: GORM
	// applies the serializer to model fields, not to raw map values.
	configJSON, err := json.Marshal(payload.Config)
	if err != nil {
		return 0, false, fmt.Errorf("encoding the config of monitor %s: %w", payload.UUID, err)
	}
	columns["config"] = string(configJSON)

	if current != nil && current.LocalID != 0 {
		result := tx.WithContext(ctx).Model(&models.Monitor{}).
			Where("id = ?", current.LocalID).Updates(columns)
		if result.Error != nil {
			return 0, false, fmt.Errorf("updating monitor %s: %w", payload.UUID, result.Error)
		}
		return current.LocalID, false, nil
	}

	// The INSERT only reserves the local id: GORM fills created_at with now (the
	// row's local lifetime starts here) while the sender's updated_at is preserved
	// because it is not zero.
	row := models.Monitor{
		UUID:         payload.UUID,
		OriginNodeID: payload.OriginNodeID,
		Revision:     payload.Revision,
		UpdatedAt:    payload.UpdatedAt,
	}
	if err := tx.WithContext(ctx).Create(&row).Error; err != nil {
		return 0, false, fmt.Errorf("creating monitor %s: %w", payload.UUID, err)
	}
	if err := tx.WithContext(ctx).Model(&models.Monitor{}).
		Where("id = ?", row.ID).Updates(columns).Error; err != nil {
		return 0, false, fmt.Errorf("storing monitor %s: %w", payload.UUID, err)
	}
	return row.ID, true, nil
}

// ensureMonitorState creates the aggregated state row a monitor needs, so the
// evaluator and the dashboard find one (a locally created monitor gets it in
// MonitorService.Create).
func (s *SyncService) ensureMonitorState(ctx context.Context, tx *gorm.DB, monitorID uint) error {
	var count int64
	if err := tx.WithContext(ctx).Model(&models.MonitorState{}).
		Where("monitor_id = ?", monitorID).Count(&count).Error; err != nil {
		return fmt.Errorf("looking for the state of monitor %d: %w", monitorID, err)
	}
	if count > 0 {
		return nil
	}
	state := models.MonitorState{
		MonitorID: monitorID,
		Status:    models.AggregateUnknown,
		ChangedAt: time.Now().UTC(),
	}
	return tx.WithContext(ctx).Create(&state).Error
}

// recordConflict stores an edit that lost the merge, so a concurrent edit is
// reported instead of disappearing. incomingWon says which side was kept.
func (s *SyncService) recordConflict(ctx context.Context, tx *gorm.DB, change models.SyncChangePayload, current *models.SyncObject, incomingWon bool) error {
	incoming := models.SyncCandidate{
		Revision:     change.Revision,
		UpdatedAt:    change.UpdatedAt,
		OriginNodeID: change.OriginNodeID,
	}
	keptOrigin, keptRevision, lostOrigin, lostRevision := models.ConflictSides(incoming, current, incomingWon)

	conflict := models.SyncConflict{
		Entity:       change.Entity,
		UUID:         change.UUID,
		KeptOrigin:   keptOrigin,
		KeptRevision: keptRevision,
		LostOrigin:   lostOrigin,
		LostRevision: lostRevision,
		DetectedAt:   time.Now().UTC(),
	}
	if err := tx.WithContext(ctx).Create(&conflict).Error; err != nil {
		return fmt.Errorf("recording a conflict on %s: %w", change.UUID, err)
	}
	s.log.Warn("two nodes edited the same record: one edit was kept",
		"entity", change.Entity, "uuid", change.UUID,
		"kept_origin", keptOrigin, "kept_revision", keptRevision,
		"lost_origin", lostOrigin, "lost_revision", lostRevision)
	return nil
}

// applyRelation merges one relation change: a membership or a monitor-to-channel
// link.
//
// A relation is addressed by its two ends, so the local rows are resolved through
// sync_objects. When one end has not arrived yet the change is recorded as a
// pending link instead of being dropped: the healing pass re-delivers the
// container with its full relation set once the target exists.
func (s *SyncService) applyRelation(ctx context.Context, tx *gorm.DB, change models.SyncChangePayload) (models.ApplyDecision, error) {
	current, err := s.loadSyncObject(ctx, tx, change.UUID)
	if err != nil {
		return models.ApplySkip, err
	}
	incoming := models.SyncCandidate{
		Revision:     change.Revision,
		UpdatedAt:    change.UpdatedAt,
		OriginNodeID: change.OriginNodeID,
	}
	deleted := change.Action == models.ActionDelete
	decision := models.Decide(incoming, deleted, current)

	if models.IsConflict(incoming, current) {
		if err := s.recordConflict(ctx, tx, change, current, decision != models.ApplySkip); err != nil {
			return decision, err
		}
	}
	if decision == models.ApplySkip {
		return decision, nil
	}

	if decision == models.ApplyDelete {
		if err := s.removeRelation(ctx, tx, change); err != nil {
			return decision, err
		}
		return decision, s.trackApplied(ctx, tx, change, 0, true)
	}

	monitorUUID, targetUUID, err := relationEnds(change)
	if err != nil {
		return models.ApplySkip, err
	}
	linked, err := s.linkRelation(ctx, tx, change.Entity, monitorUUID, targetUUID)
	if err != nil {
		return models.ApplySkip, err
	}
	if !linked {
		if err := s.recordPendingLink(ctx, tx, change.Entity, change.UUID, targetUUID); err != nil {
			return models.ApplySkip, err
		}
	}
	return decision, s.trackApplied(ctx, tx, change, 0, false)
}

// relationEnds reads the two uuids a relation payload names.
func relationEnds(change models.SyncChangePayload) (string, string, error) {
	switch change.Entity {
	case models.EntityMonitorGroupMember:
		var payload models.MonitorGroupMemberPayload
		if err := json.Unmarshal(change.Payload, &payload); err != nil {
			return "", "", fmt.Errorf("unreadable membership payload for %s: %w", change.UUID, err)
		}
		return payload.MonitorUUID, payload.GroupUUID, nil
	case models.EntityMonitorNotification:
		var payload models.MonitorNotificationPayload
		if err := json.Unmarshal(change.Payload, &payload); err != nil {
			return "", "", fmt.Errorf("unreadable link payload for %s: %w", change.UUID, err)
		}
		return payload.MonitorUUID, payload.NotificationUUID, nil
	}
	return "", "", fmt.Errorf("entity %q is not a relation", change.Entity)
}

// linkRelation materialises a relation whose two ends both exist locally. It
// reports false when one of them has not arrived yet.
func (s *SyncService) linkRelation(ctx context.Context, tx *gorm.DB, entity, monitorUUID, targetUUID string) (bool, error) {
	monitorID, monitorOK, err := s.localID(ctx, tx, models.EntityMonitor, monitorUUID)
	if err != nil || !monitorOK {
		return false, err
	}
	target, targetOK, err := s.localID(ctx, tx, targetEntity(entity), targetUUID)
	if err != nil || !targetOK {
		return false, err
	}

	switch entity {
	case models.EntityMonitorGroupMember:
		link := models.MonitorGroupMember{GroupID: target, MonitorID: monitorID}
		return true, tx.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&link).Error
	case models.EntityMonitorNotification:
		link := models.MonitorNotification{MonitorID: monitorID, NotificationID: target, OnDown: true, OnUp: true}
		return true, tx.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&link).Error
	}
	return false, fmt.Errorf("entity %q is not a relation", entity)
}

// removeRelation deletes the local link of a relation that was removed elsewhere.
// A missing row is fine: the change may name a relation this node never had.
func (s *SyncService) removeRelation(ctx context.Context, tx *gorm.DB, change models.SyncChangePayload) error {
	monitorUUID, targetUUID, err := relationEnds(change)
	if err != nil {
		return err
	}
	monitorID, monitorOK, err := s.localID(ctx, tx, models.EntityMonitor, monitorUUID)
	if err != nil {
		return err
	}
	target, targetOK, err := s.localID(ctx, tx, targetEntity(change.Entity), targetUUID)
	if err != nil {
		return err
	}
	if !monitorOK || !targetOK {
		return nil
	}
	switch change.Entity {
	case models.EntityMonitorGroupMember:
		return tx.WithContext(ctx).Where("monitor_id = ? AND group_id = ?", monitorID, target).
			Delete(&models.MonitorGroupMember{}).Error
	case models.EntityMonitorNotification:
		return tx.WithContext(ctx).Where("monitor_id = ? AND notification_id = ?", monitorID, target).
			Delete(&models.MonitorNotification{}).Error
	}
	return fmt.Errorf("entity %q is not a relation", change.Entity)
}

// targetEntity maps a relation to the entity of its second end.
func targetEntity(entity string) string {
	if entity == models.EntityMonitorGroupMember {
		return models.EntityMonitorGroup
	}
	return models.EntityNotification
}

// localID resolves a uuid to the local row, reporting whether that row exists and
// is not a tombstone.
func (s *SyncService) localID(ctx context.Context, tx *gorm.DB, entity, uuid string) (uint, bool, error) {
	object, err := s.loadSyncObject(ctx, tx, uuid)
	if err != nil {
		return 0, false, err
	}
	if object == nil || object.DeletedAt != nil || object.LocalID == 0 {
		return 0, false, nil
	}
	return object.LocalID, true, nil
}

// recordPendingLink marks a relation whose target has not arrived.
//
// It is a marker rather than a queue: the healing pass re-delivers the container
// with its full relation set, and by then the target exists. That keeps the retry
// bounded and visible instead of silently dropping the link.
func (s *SyncService) recordPendingLink(ctx context.Context, tx *gorm.DB, entity, uuid, missingUUID string) error {
	pending := models.SyncPendingLink{
		Entity:      entity,
		UUID:        uuid,
		Kind:        entity,
		MissingUUID: missingUUID,
		CreatedAt:   time.Now().UTC(),
	}
	if err := tx.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&pending).Error; err != nil {
		return fmt.Errorf("recording a pending link for %s: %w", uuid, err)
	}
	s.log.Info("a relation arrived before its target: it will be linked after the next reconciliation",
		"entity", entity, "uuid", uuid, "missing", missingUUID)
	return nil
}

// healPeer rebuilds the view of a peer that cannot be caught up incrementally.
//
// It is reached when a cursor points into the pruned region of the outbox: the
// rows the caller needed no longer exist, so only the manifest and the snapshots
// can reconcile the two nodes. If the checksums happen to agree (the missed
// changes were superseded and both nodes converged anyway), the pass correctly
// does nothing.
func (s *SyncService) healPeer(ctx context.Context, peer models.Node) error {
	s.log.Warn("the cursor points into a pruned region of a peer's outbox: resynchronising from the snapshots",
		"peer", peer.NodeID)
	return s.reconcilePeer(ctx, peer)
}
