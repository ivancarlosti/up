package services

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"time"

	"gorm.io/gorm"

	"github.com/ivancarlosti/up/internal/i18n"
	"github.com/ivancarlosti/up/internal/models"
)

// maxSnapshotPages bounds one resync of an entity, so a pathological page count
// cannot hold the loop forever.
const maxSnapshotPages = 100

// ServeManifest answers GET /api/cluster/sync/manifest: the identity of every
// entity this node holds, as a checksum over the live (uuid, revision) pairs.
//
// It is the healing trigger. Two nodes that agree on every checksum are in sync
// and skip the snapshot entirely, which is what keeps the periodic pass cheap.
func (s *SyncService) ServeManifest(ctx context.Context) (*models.SyncManifestResponse, error) {
	if !s.Enabled() {
		return nil, ErrForbidden(i18n.CodeClusterDisabled, "synchronisation is not enabled on this node")
	}
	response := &models.SyncManifestResponse{
		ProtocolVersion: models.ProtocolVersion,
		Entities:        []models.SyncManifestEntity{},
	}
	for _, entity := range s.syncedEntities() {
		entry, err := s.entityManifest(ctx, entity)
		if err != nil {
			return nil, err
		}
		response.Entities = append(response.Entities, entry)
	}
	return response, nil
}

// entityManifest computes the identity of one entity, tombstones excluded: a
// deleted row is not part of what this node holds, so it must not change the
// checksum.
func (s *SyncService) entityManifest(ctx context.Context, entity string) (models.SyncManifestEntity, error) {
	var rows []models.SyncObject
	if err := s.db.WithContext(ctx).
		Select("uuid", "revision", "origin_node_id").
		Where("entity = ? AND deleted_at IS NULL", entity).
		Find(&rows).Error; err != nil {
		return models.SyncManifestEntity{}, ErrInternal(err)
	}
	identities := make([]models.EntityIdentity, 0, len(rows))
	var maxRevision int64
	for _, row := range rows {
		identities = append(identities, models.EntityIdentity{
			UUID: row.UUID, Revision: row.Revision, OriginNodeID: row.OriginNodeID,
		})
		if row.Revision > maxRevision {
			maxRevision = row.Revision
		}
	}
	return models.SyncManifestEntity{
		Entity:      entity,
		Count:       int64(len(rows)),
		MaxRevision: maxRevision,
		Checksum:    models.EntityChecksum(identities),
	}, nil
}

// ServeSnapshot answers GET /api/cluster/sync/snapshot?entity=&page=: the current
// identity of one entity, paged in uuid order.
//
// It is enumerated from sync_objects rather than from the entity table on purpose:
// a relation has no row of its own to enumerate and a tombstone has neither, and a
// snapshot that could not describe a deletion would leave a missed deletion
// unrepaired forever — the peer's live rows simply do not mention it.
func (s *SyncService) ServeSnapshot(ctx context.Context, entity string, page, size int) (*models.SyncSnapshotResponse, error) {
	if !s.Enabled() {
		return nil, ErrForbidden(i18n.CodeClusterDisabled, "synchronisation is not enabled on this node")
	}
	if !s.cfg.SyncEntityEnabled(entity) {
		return nil, ErrBadRequest(i18n.CodeValidation, "unknown or disabled entity "+entity)
	}
	if size <= 0 || size > s.cfg.ClusterSyncBatch {
		size = s.cfg.ClusterSyncBatch
	}
	if page < 0 {
		page = 0
	}

	var rows []models.SyncObject
	if err := s.db.WithContext(ctx).
		Where("entity = ?", entity).
		Order("uuid ASC").
		Offset(page * size).Limit(size + 1).
		Find(&rows).Error; err != nil {
		return nil, ErrInternal(err)
	}
	hasMore := len(rows) > size
	if hasMore {
		rows = rows[:size]
	}

	response := &models.SyncSnapshotResponse{
		Entity:          entity,
		Page:            page,
		HasMore:         hasMore,
		ProtocolVersion: models.ProtocolVersion,
		Changes:         []models.SyncChangePayload{},
	}
	for _, row := range rows {
		change, ok := SnapshotChange(row)
		if !ok {
			s.log.Warn("a synchronised identity carries no payload: skipping it in the snapshot",
				"entity", entity, "uuid", row.UUID)
			continue
		}
		response.Changes = append(response.Changes, change)
	}
	return response, nil
}

// SnapshotChange renders one identity of a snapshot as a wire change.
//
// An identity without a payload is reported as "cannot be described" rather than as
// a deletion: a deletion is destructive, and guessing one from a missing field
// would destroy data on the receiver.
func SnapshotChange(row models.SyncObject) (models.SyncChangePayload, bool) {
	action := models.ActionUpsert
	switch {
	case row.DeletedAt != nil:
		action = models.ActionDelete
	case row.Payload == "":
		return models.SyncChangePayload{}, false
	}
	change := models.SyncChangePayload{
		// A snapshot position is not an outbox position, so the caller must not
		// advance its cursor from it.
		ID:           0,
		Entity:       row.Entity,
		UUID:         row.UUID,
		Action:       action,
		OriginNodeID: row.OriginNodeID,
		Revision:     row.Revision,
		UpdatedAt:    row.UpdatedAt,
	}
	if row.Payload != "" && json.Valid([]byte(row.Payload)) {
		change.Payload = json.RawMessage(row.Payload)
	}
	return change, true
}

// manifestDue reports whether the healing pass is due for a peer.
func (s *SyncService) manifestDue(ctx context.Context, peerNodeID string) bool {
	interval := time.Duration(s.cfg.ClusterSyncManifestSeconds) * time.Second
	at := s.cluster.loadPeerRow(ctx, peerNodeID).LastManifestAt
	return at == nil || time.Since(*at) >= interval
}

// recordManifest remembers when the checksums were compared and whether they
// agreed, so the admin view can show a peer that never reconciles.
func (s *SyncService) recordManifest(ctx context.Context, peerNodeID string, agreed bool) {
	now := time.Now().UTC()
	if err := s.db.WithContext(ctx).Model(&models.SyncPeer{}).
		Where("peer_node_id = ?", peerNodeID).
		Updates(map[string]any{"last_manifest_at": now, "last_manifest_ok": agreed}).Error; err != nil {
		s.log.Warn("could not record the manifest pass", "peer", peerNodeID, "error", err)
	}
}

// reconcilePeer compares every checksum with a peer and resynchronises the
// entities that disagree.
//
// This is what makes the protocol self-healing instead of "eventually maybe": the
// incremental pull can miss a change (a pruned outbox, a dropped request, a
// restored backup), and the checksum comparison is what notices.
func (s *SyncService) reconcilePeer(ctx context.Context, peer models.Node) error {
	manifest, err := s.fetchManifest(ctx, peer)
	if err != nil {
		return err
	}
	if manifest.ProtocolVersion != models.ProtocolVersion {
		return fmt.Errorf("peer speaks protocol %d, this node speaks %d",
			manifest.ProtocolVersion, models.ProtocolVersion)
	}

	remote := make(map[string]models.SyncManifestEntity, len(manifest.Entities))
	for _, entry := range manifest.Entities {
		remote[entry.Entity] = entry
	}

	resynced := 0
	for _, entity := range s.syncedEntities() {
		local, err := s.entityManifest(ctx, entity)
		if err != nil {
			return err
		}
		theirs, present := remote[entity]
		if present && theirs.Checksum == local.Checksum {
			continue
		}
		s.log.Warn("an entity disagrees with a peer: resynchronising it from the snapshot",
			"peer", peer.NodeID, "entity", entity,
			"local_rows", local.Count, "remote_rows", theirs.Count)
		if err := s.pullSnapshot(ctx, peer, entity); err != nil {
			return err
		}
		resynced++
	}

	s.recordManifest(ctx, peer.NodeID, resynced == 0)
	if resynced > 0 {
		s.log.Info("resynchronised the entities that disagreed with a peer",
			"peer", peer.NodeID, "entities", resynced)
	}
	return nil
}

// pullSnapshot applies the whole state of one entity as a peer describes it.
//
// It walks the same apply path as the incremental pull (applyChanges), so a bug
// cannot exist in only one of the two.
func (s *SyncService) pullSnapshot(ctx context.Context, peer models.Node, entity string) error {
	for page := 0; page < maxSnapshotPages; page++ {
		snapshot, err := s.fetchSnapshot(ctx, peer, entity, page)
		if err != nil {
			return err
		}
		if len(snapshot.Changes) > 0 {
			// A snapshot position is not an outbox position, so no cursor moves
			// here: only the incremental path advances it.
			applying := WithApply(ctx)
			if err := s.db.WithContext(applying).Transaction(func(tx *gorm.DB) error {
				_, _, err := s.applyChanges(applying, tx, snapshot.Changes)
				return err
			}); err != nil {
				return err
			}
		}
		if !snapshot.HasMore {
			return nil
		}
	}
	s.log.Warn("stopped a resync at the page cap: the entity may still disagree",
		"peer", peer.NodeID, "entity", entity, "pages", maxSnapshotPages)
	return nil
}

// retryPendingLinks re-pulls the entities that hold a reference which arrived
// before its target. The snapshot carries the whole entity, so the reference is
// materialised by the very next apply and its marker is then cleared.
//
// Markers past the attempt cap are left out: an unresolved reference stays VISIBLE
// for the operator, but it stops costing a snapshot pull on every cycle for ever.
func (s *SyncService) retryPendingLinks(ctx context.Context, peer models.Node) error {
	var entities []string
	if err := s.db.WithContext(ctx).Model(&models.SyncPendingLink{}).
		Where("attempts < ?", maxPendingLinkAttempts).
		Distinct().Pluck("entity", &entities).Error; err != nil {
		return ErrInternal(err)
	}
	if len(entities) == 0 {
		return nil
	}
	for _, entity := range entities {
		if err := s.pullSnapshot(ctx, peer, entity); err != nil {
			return err
		}
	}
	s.log.Info("retried the references that arrived before their targets",
		"peer", peer.NodeID, "entities", entities)
	return nil
}

// fetchManifest asks a peer for its entity checksums.
func (s *SyncService) fetchManifest(ctx context.Context, peer models.Node) (*models.SyncManifestResponse, error) {
	var out models.SyncManifestResponse
	if err := s.cluster.peerRequest(ctx, peer, "/api/cluster/sync/manifest", "", &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// fetchSnapshot pulls one page of an entity from a peer.
func (s *SyncService) fetchSnapshot(ctx context.Context, peer models.Node, entity string, page int) (*models.SyncSnapshotResponse, error) {
	query := url.Values{}
	query.Set("entity", entity)
	query.Set("page", strconv.Itoa(page))
	query.Set("size", strconv.Itoa(s.cfg.ClusterSyncBatch))

	var out models.SyncSnapshotResponse
	if err := s.cluster.peerRequest(ctx, peer, "/api/cluster/sync/snapshot", query.Encode(), &out); err != nil {
		return nil, err
	}
	return &out, nil
}
