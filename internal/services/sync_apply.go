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
	if err == nil {
		return nil
	}

	// The batch is all-or-nothing, so a single unreadable change would otherwise
	// leave the cursor where it is and queue every later change behind it for ever.
	// Degrade to one change per transaction: what applies is recorded, what fails is
	// counted, and what passes the attempt cap is stepped over so the rest can flow.
	s.log.Warn("a batch from a peer could not be applied: isolating its changes one by one",
		"peer", peerNodeID, "changes", len(changes), "error", err)
	return s.applyOneByOne(applying, peerNodeID, changes)
}

// applyOneByOne isolates the change a batch could not apply.
//
// The order is preserved: the first change that fails without having reached the
// attempt cap stops the walk, so the next cycle retries the tail in order and
// nothing is applied out of order. A change that reaches the cap is SKIPPED —
// recorded as a dead letter and stepped over — because one bad change must never
// stop a peer's synchronisation. Skipping is recoverable on purpose: the identity
// the receiver holds then disagrees with the sender's, and the manifest pass heals
// it with a snapshot.
func (s *SyncService) applyOneByOne(ctx context.Context, peerNodeID string, changes []models.SyncChangePayload) error {
	for _, change := range changes {
		err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			if _, _, err := s.applyChanges(ctx, tx, []models.SyncChangePayload{change}); err != nil {
				return err
			}
			return s.advanceCursor(ctx, tx, peerNodeID, int64(change.ID))
		})
		if err == nil {
			continue
		}

		attempts, err := s.recordDeadLetter(ctx, peerNodeID, change, err)
		if err != nil {
			return err
		}
		if attempts < maxDeadLetterAttempts {
			return err
		}
		s.log.Error("skipping a change this node cannot apply, after the attempt cap",
			"peer", peerNodeID, "change_id", change.ID, "entity", change.Entity,
			"uuid", change.UUID, "attempts", attempts)
		if err := s.markDeadLetterSkipped(ctx, peerNodeID, change.ID); err != nil {
			return err
		}
		if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			return s.advanceCursor(ctx, tx, peerNodeID, int64(change.ID))
		}); err != nil {
			return err
		}
	}
	return nil
}

// recordDeadLetter counts a change this node could not apply and returns the number
// of attempts it has now had.
func (s *SyncService) recordDeadLetter(ctx context.Context, peerNodeID string, change models.SyncChangePayload, cause error) (int, error) {
	now := time.Now().UTC()
	entry := models.SyncDeadLetter{
		PeerNodeID:  peerNodeID,
		ChangeID:    change.ID,
		Entity:      change.Entity,
		UUID:        change.UUID,
		PayloadHash: change.PayloadHash,
		Attempts:    1,
		LastError:   truncateForLog(cause.Error(), 500),
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	err := s.db.WithContext(ctx).Clauses(clause.OnConflict{
		DoUpdates: clause.Assignments(map[string]any{
			"attempts":     gorm.Expr("attempts + 1"),
			"entity":       entry.Entity,
			"uuid":         entry.UUID,
			"payload_hash": entry.PayloadHash,
			"last_error":   entry.LastError,
			"updated_at":   now,
		}),
	}).Create(&entry).Error
	if err != nil {
		return 0, ErrInternal(err)
	}

	var current models.SyncDeadLetter
	if err := s.db.WithContext(ctx).
		Where("peer_node_id = ? AND change_id = ?", peerNodeID, change.ID).
		First(&current).Error; err != nil {
		return 0, ErrInternal(err)
	}
	if current.Attempts <= maxDeadLetterAttempts {
		s.log.Warn("a change from a peer keeps failing to apply",
			"peer", peerNodeID, "change_id", change.ID, "entity", change.Entity,
			"uuid", change.UUID, "attempts", current.Attempts, "error", cause)
	}
	return current.Attempts, nil
}

// markDeadLetterSkipped records that a change was stepped over rather than applied.
func (s *SyncService) markDeadLetterSkipped(ctx context.Context, peerNodeID string, changeID int64) error {
	if err := s.db.WithContext(ctx).Model(&models.SyncDeadLetter{}).
		Where("peer_node_id = ? AND change_id = ?", peerNodeID, changeID).
		Updates(map[string]any{"skipped": true, "updated_at": time.Now().UTC()}).Error; err != nil {
		return ErrInternal(err)
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

	// A target that just arrived unblocks the references that were waiting for it.
	// That is LOCAL work, not a version: the container is not re-applied as a change
	// (the merge rule would — correctly — skip it at the revision already held), its
	// stored payload is only written again (see resolvePendingLinks).
	if err := s.resolvePendingLinks(ctx, tx); err != nil {
		return applied, skipped, err
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
	case models.EntityMonitorGroup:
		return s.applyGroup(ctx, tx, change)
	case models.EntityMonitorTemplate:
		return s.applyTemplate(ctx, tx, change)
	case models.EntityStatusPage:
		return s.applyStatusPage(ctx, tx, change)
	case models.EntityNotification:
		return s.applyNotification(ctx, tx, change)
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

// mergeDecision loads what this node holds for a uuid, compares the incoming
// change with it and reports the verdict, recording a concurrent edit on the way.
//
// Every entity goes through it so that the merge rule, the conflict log and the
// tombstone comparison cannot drift apart between one entity and another: a
// divergence there would be invisible until two nodes disagreed about a row.
func (s *SyncService) mergeDecision(ctx context.Context, tx *gorm.DB, change models.SyncChangePayload) (models.ApplyDecision, *models.SyncObject, error) {
	current, err := s.loadSyncObject(ctx, tx, change.UUID)
	if err != nil {
		return models.ApplySkip, nil, err
	}
	incoming := models.SyncCandidate{
		Revision:     change.Revision,
		UpdatedAt:    change.UpdatedAt,
		OriginNodeID: change.OriginNodeID,
	}
	deleted := change.Action == models.ActionDelete
	decision := models.Decide(incoming, deleted, current)

	// A concurrent edit is reported whichever way the merge went: when the incoming
	// change wins, the edit it replaces is the one that lost. A stale re-delivery
	// is not a conflict (see models.IsConflict) and stays silent.
	if models.IsConflict(incoming, current) {
		if err := s.recordConflict(ctx, tx, change, current, decision != models.ApplySkip); err != nil {
			return decision, current, err
		}
	}
	return decision, current, nil
}

// applyMonitor merges one monitor change into the local database.
func (s *SyncService) applyMonitor(ctx context.Context, tx *gorm.DB, change models.SyncChangePayload) (models.ApplyDecision, error) {
	decision, current, err := s.mergeDecision(ctx, tx, change)
	if err != nil {
		return models.ApplySkip, err
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
// pending link instead of being dropped: the healing pass re-delivers the container
// with its full relation set once the target exists.
func (s *SyncService) applyRelation(ctx context.Context, tx *gorm.DB, change models.SyncChangePayload) (models.ApplyDecision, error) {
	decision, _, err := s.mergeDecision(ctx, tx, change)
	if err != nil {
		return models.ApplySkip, err
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

	monitorUUID, targetUUID, err := s.relationEnds(ctx, tx, change)
	if err != nil {
		return models.ApplySkip, err
	}
	linked, err := s.linkRelation(ctx, tx, change.Entity, monitorUUID, targetUUID)
	if err != nil {
		return models.ApplySkip, err
	}
	if !linked {
		if err := s.recordPendingLink(ctx, tx, change.Entity, change.UUID, "target", targetUUID); err != nil {
			return models.ApplySkip, err
		}
	} else if err := s.clearResolvedPendingLinks(ctx, tx, change.Entity, change.UUID); err != nil {
		return models.ApplySkip, err
	}
	return decision, s.trackApplied(ctx, tx, change, 0, false)
}

// relationEnds reads the two uuids a relation payload names.
//
// A tombstone may arrive with no payload of its own, and the uuid of a relation is
// a HASH of the two ends, so it cannot be inverted: the pair is then read from what
// this node stored when it applied the link. When there is neither, the relation was
// never applied here and there is nothing to remove.
func (s *SyncService) relationEnds(ctx context.Context, tx *gorm.DB, change models.SyncChangePayload) (string, string, error) {
	payload := change.Payload
	if len(payload) == 0 {
		object, err := s.loadSyncObject(ctx, tx, change.UUID)
		if err != nil {
			return "", "", err
		}
		if object == nil || object.Payload == "" {
			return "", "", nil
		}
		payload = []byte(object.Payload)
	}

	switch change.Entity {
	case models.EntityMonitorGroupMember:
		var decoded models.MonitorGroupMemberPayload
		if err := json.Unmarshal(payload, &decoded); err != nil {
			return "", "", fmt.Errorf("unreadable membership payload for %s: %w", change.UUID, err)
		}
		return decoded.MonitorUUID, decoded.GroupUUID, nil
	case models.EntityMonitorNotification:
		var decoded models.MonitorNotificationPayload
		if err := json.Unmarshal(payload, &decoded); err != nil {
			return "", "", fmt.Errorf("unreadable link payload for %s: %w", change.UUID, err)
		}
		return decoded.MonitorUUID, decoded.NotificationUUID, nil
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
	monitorUUID, targetUUID, err := s.relationEnds(ctx, tx, change)
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
// maxDeadLetterAttempts is how many times one change may fail before it is stepped
// over. It is deliberately small: a change that has failed this many times is not
// transiently unlucky, and the peer's later changes must not queue behind it. The
// skip is recoverable, because the manifest pass re-delivers the state.
const maxDeadLetterAttempts = 10

// maxPendingLinkAttempts bounds how many times a missing reference makes its
// container be re-applied. Past it the marker stops triggering retries and stays
// visible for the operator: an unresolved reference is a fact worth keeping, but it
// is not worth a snapshot pull every cycle for ever.
const maxPendingLinkAttempts = 20

func (s *SyncService) recordPendingLink(ctx context.Context, tx *gorm.DB, entity, uuid, kind, missingUUID string) error {
	now := time.Now().UTC()
	pending := models.SyncPendingLink{
		Entity:        entity,
		UUID:          uuid,
		Kind:          kind,
		MissingUUID:   missingUUID,
		Attempts:      1,
		LastAttemptAt: now,
		CreatedAt:     now,
	}
	// The same reference is reported again on every retry, so the marker counts the
	// attempts instead of piling up rows.
	err := tx.WithContext(ctx).Clauses(clause.OnConflict{
		DoUpdates: clause.Assignments(map[string]any{
			"attempts":        gorm.Expr("attempts + 1"),
			"last_attempt_at": now,
		}),
	}).Create(&pending).Error
	if err != nil {
		return fmt.Errorf("recording a pending link for %s: %w", uuid, err)
	}
	if pending.Attempts == maxPendingLinkAttempts {
		s.log.Warn("a reference is still missing after many retries: it will stop being retried and stay listed",
			"entity", entity, "uuid", uuid, "kind", kind, "missing", missingUUID,
			"attempts", maxPendingLinkAttempts)
	}
	return nil
}

// pendingLinkTarget maps the kind of a reference list to the entity it points at.
func pendingLinkTarget(entity, kind string) string {
	switch kind {
	case "monitor_uuids":
		return models.EntityMonitor
	case "group_uuids":
		return models.EntityMonitorGroup
	case "notification_uuids":
		return models.EntityNotification
	case "target":
		if entity == models.EntityMonitorGroupMember {
			return models.EntityMonitorGroup
		}
		return models.EntityNotification
	}
	return ""
}

// clearResolvedPendingLinks drops the markers of a container (or of one relation)
// whose missing reference has arrived since the last attempt.
//
// Without this the markers outlive the problem: they would keep naming an entity
// whose references are all present, and the retry would re-pull its snapshot on
// every cycle for ever. It is safe to call after any successful apply — markers
// whose reference is still missing are simply left alone.
func (s *SyncService) clearResolvedPendingLinks(ctx context.Context, tx *gorm.DB, entity, uuid string) error {
	if uuid == "" {
		return nil
	}
	var markers []models.SyncPendingLink
	if err := tx.WithContext(ctx).Where("entity = ? AND uuid = ?", entity, uuid).
		Find(&markers).Error; err != nil {
		return ErrInternal(err)
	}
	for _, marker := range markers {
		target := pendingLinkTarget(marker.Entity, marker.Kind)
		if target == "" {
			continue
		}
		_, resolved, err := s.localID(ctx, tx, target, marker.MissingUUID)
		if err != nil {
			return err
		}
		if !resolved {
			continue
		}
		if err := tx.WithContext(ctx).Delete(&models.SyncPendingLink{}, marker.ID).Error; err != nil {
			return ErrInternal(err)
		}
	}
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
