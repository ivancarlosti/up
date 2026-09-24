package services

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/ivancarlosti/up/internal/config"
	"github.com/ivancarlosti/up/internal/models"
	"github.com/ivancarlosti/up/internal/utils"
)

// ---------------------------------------------------------------------------
// The apply-path flag
// ---------------------------------------------------------------------------

type syncContextKey struct{}

// WithApply marks a context as "applying a change that came from a peer". The
// emitter checks it and records nothing: without this, two nodes would publish
// each other's changes back and forth forever (docs/clustering-federated.md,
// section 5.1).
func WithApply(ctx context.Context) context.Context {
	return context.WithValue(ctx, syncContextKey{}, true)
}

// IsApplying reports whether ctx belongs to the apply path.
func IsApplying(ctx context.Context) bool {
	applying, _ := ctx.Value(syncContextKey{}).(bool)
	return applying
}

// ---------------------------------------------------------------------------
// Wire payload builders
// ---------------------------------------------------------------------------
//
// The builders are pure (no database) so the "a local id never reaches the wire"
// rule is pinned by a test rather than trusted. Each one copies the row, clears
// its local identity and its runtime decoration, and attaches the references
// expressed as uuids.

// BuildMonitorPayload renders a monitor for the wire.
//
// The group and channel links are NOT part of it: they are published as their own
// entities so that a change made from either end has a single writer.
func BuildMonitorPayload(m *models.Monitor) ([]byte, error) {
	return json.Marshal(models.MonitorPayload{
		UUID:         m.UUID,
		OriginNodeID: m.OriginNodeID,
		Revision:     m.Revision,
		UpdatedAt:    m.UpdatedAt,

		Name:                   m.Name,
		Type:                   m.Type,
		Active:                 m.Active,
		Description:            m.Description,
		IntervalSeconds:        m.IntervalSeconds,
		Retries:                m.Retries,
		RetriesIntervalSeconds: m.RetriesIntervalSeconds,
		TimeoutSeconds:         m.TimeoutSeconds,
		ResendIntervalSeconds:  m.ResendIntervalSeconds,
		UpsideDown:             m.UpsideDown,
		RunOn:                  m.RunOn,
		NodeID:                 m.NodeID,
		Tags:                   m.Tags,
		CertWatch:              m.CertWatch,
		CertNotify:             m.CertNotify,
		CertWarnDays:           m.CertWarnDays,
		Config:                 m.Config,
	})
}

// BuildMonitorGroupPayload renders a group for the wire (members are a separate
// entity).
func BuildMonitorGroupPayload(g *models.MonitorGroup) ([]byte, error) {
	return json.Marshal(models.MonitorGroupPayload{
		UUID:         g.UUID,
		OriginNodeID: g.OriginNodeID,
		Revision:     g.Revision,
		UpdatedAt:    g.UpdatedAt,

		Name:        g.Name,
		Description: g.Description,
		Color:       g.Color,
		SortOrder:   g.SortOrder,
	})
}

// BuildMonitorGroupMemberPayload renders one membership.
func BuildMonitorGroupMemberPayload(monitorUUID, groupUUID string) ([]byte, error) {
	return json.Marshal(models.MonitorGroupMemberPayload{
		MonitorUUID: monitorUUID,
		GroupUUID:   groupUUID,
	})
}

// BuildMonitorNotificationPayload renders one monitor-to-channel link.
func BuildMonitorNotificationPayload(monitorUUID, notificationUUID string) ([]byte, error) {
	return json.Marshal(models.MonitorNotificationPayload{
		MonitorUUID:      monitorUUID,
		NotificationUUID: notificationUUID,
	})
}

// BuildMonitorTemplatePayload renders a template for the wire.
//
// The template's defaults carry their links as LOCAL ids (they are applied to a
// monitor when the template is used), so those two fields are cleared here and
// travel as uuids instead: a local id on the wire would be meaningless on the
// receiver, and worse, silently wrong.
func BuildMonitorTemplatePayload(t *models.MonitorTemplate, groupUUIDs, notificationUUIDs []string) ([]byte, error) {
	defaults := t.Defaults
	defaults.NotificationIDs = nil
	defaults.GroupIDs = nil

	return json.Marshal(models.MonitorTemplatePayload{
		UUID:         t.UUID,
		OriginNodeID: t.OriginNodeID,
		Revision:     t.Revision,
		UpdatedAt:    t.UpdatedAt,

		Name:        t.Name,
		Description: t.Description,
		Type:        t.Type,
		Config:      t.Config,
		Defaults:    defaults,

		GroupUUIDs:        orEmpty(groupUUIDs),
		NotificationUUIDs: orEmpty(notificationUUIDs),
	})
}

// BuildStatusPagePayload renders a status page for the wire.
func BuildStatusPagePayload(p *models.StatusPage, monitorUUIDs, groupUUIDs []string) ([]byte, error) {
	return json.Marshal(models.StatusPagePayload{
		UUID:         p.UUID,
		OriginNodeID: p.OriginNodeID,
		Revision:     p.Revision,
		UpdatedAt:    p.UpdatedAt,

		Slug:        p.Slug,
		Title:       p.Title,
		Description: p.Description,
		FooterText:  p.FooterText,
		Theme:       p.Theme,
		IsPublic:    p.IsPublic,
		ShowUptime:  p.ShowUptime,
		ShowCharts:  p.ShowCharts,
		ShowTags:    p.ShowTags,
		CustomCSS:   p.CustomCSS,

		MonitorUUIDs: orEmpty(monitorUUIDs),
		GroupUUIDs:   orEmpty(groupUUIDs),
	})
}

// BuildNotificationPayload renders a channel for the wire.
func BuildNotificationPayload(n *models.Notification) ([]byte, error) {
	return json.Marshal(models.NotificationPayload{
		UUID:         n.UUID,
		OriginNodeID: n.OriginNodeID,
		Revision:     n.Revision,
		UpdatedAt:    n.UpdatedAt,

		Name:                  n.Name,
		Type:                  n.Type,
		Active:                n.Active,
		IsDefault:             n.IsDefault,
		ResendIntervalSeconds: n.ResendIntervalSeconds,
		Config:                n.Config,
	})
}

// orEmpty keeps the JSON shape stable: an absent list and an empty list mean the
// same thing (no links), so an always-present array avoids a receiver having to
// distinguish null from [].
func orEmpty(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}

// ---------------------------------------------------------------------------
// Emitter
// ---------------------------------------------------------------------------

// syncEntities maps an entity name to the table that holds it. It is the single
// place where an entity name becomes a table name.
var syncEntities = map[string]string{
	models.EntityMonitor:         "monitors",
	models.EntityMonitorGroup:    "monitor_groups",
	models.EntityMonitorTemplate: "monitor_templates",
	models.EntityStatusPage:      "status_pages",
	models.EntityNotification:    "notifications",
}

// SyncIdentity is the sync identity of one local row.
type SyncIdentity struct {
	LocalID      uint
	UUID         string
	Revision     int64
	OriginNodeID string
	UpdatedAt    time.Time
}

// SyncEmitter records every local write in `sync_outbox` and keeps `sync_objects`
// in step, inside the transaction that performed the write.
//
// It is deliberately called from the service layer instead of from GORM hooks:
// the services write with map-based updates, whose affected row ids are not
// exposed to hooks, and a missed emission is permanent divergence. Three obvious
// calls per entity beat unobvious framework machinery.
type SyncEmitter struct {
	db  *gorm.DB
	cfg *config.Config
	log *slog.Logger
}

// NewSyncEmitter builds the emitter.
func NewSyncEmitter(db *gorm.DB, cfg *config.Config, log *slog.Logger) *SyncEmitter {
	return &SyncEmitter{db: db, cfg: cfg, log: log}
}

// Enabled reports whether this node publishes changes.
//
// Only federated mode has an outbox: in shared mode every node already reads and
// writes the same rows, so publishing them to each other would be noise (and a
// shared node and a federated node exchange nothing by design).
func (e *SyncEmitter) Enabled() bool {
	return e.cfg != nil && e.cfg.SyncEnabled()
}

// skip reports whether emission must be bypassed: the node is not federated, or
// it is applying a change that came from a peer.
func (e *SyncEmitter) skip(ctx context.Context) bool {
	return !e.Enabled() || IsApplying(ctx)
}

// EmitMonitor publishes a monitor (created or updated) from inside its write
// transaction. It re-reads the row so the payload carries exactly what the
// database holds, including the revision the update just computed.
func (e *SyncEmitter) EmitMonitor(ctx context.Context, tx *gorm.DB, id uint) error {
	if e.skip(ctx) {
		return nil
	}
	var row models.Monitor
	if err := tx.WithContext(ctx).First(&row, id).Error; err != nil {
		return fmt.Errorf("reading the monitor to publish: %w", err)
	}
	payload, err := BuildMonitorPayload(&row)
	if err != nil {
		return err
	}
	return e.write(ctx, tx, models.EntityMonitor, SyncIdentity{
		LocalID: row.ID, UUID: row.UUID, Revision: row.Revision,
		OriginNodeID: row.OriginNodeID, UpdatedAt: row.UpdatedAt,
	}, models.ActionUpsert, payload)
}

// EmitMonitorGroup publishes a group.
func (e *SyncEmitter) EmitMonitorGroup(ctx context.Context, tx *gorm.DB, id uint) error {
	if e.skip(ctx) {
		return nil
	}
	var row models.MonitorGroup
	if err := tx.WithContext(ctx).First(&row, id).Error; err != nil {
		return fmt.Errorf("reading the group to publish: %w", err)
	}
	payload, err := BuildMonitorGroupPayload(&row)
	if err != nil {
		return err
	}
	return e.write(ctx, tx, models.EntityMonitorGroup, SyncIdentity{
		LocalID: row.ID, UUID: row.UUID, Revision: row.Revision,
		OriginNodeID: row.OriginNodeID, UpdatedAt: row.UpdatedAt,
	}, models.ActionUpsert, payload)
}

// EmitMonitorTemplate publishes a template.
func (e *SyncEmitter) EmitMonitorTemplate(ctx context.Context, tx *gorm.DB, id uint) error {
	if e.skip(ctx) {
		return nil
	}
	var row models.MonitorTemplate
	if err := tx.WithContext(ctx).First(&row, id).Error; err != nil {
		return fmt.Errorf("reading the template to publish: %w", err)
	}
	groupUUIDs, err := uuidList(ctx, tx,
		`SELECT uuid FROM monitor_groups WHERE id IN ? ORDER BY uuid`, row.Defaults.GroupIDs)
	if err != nil {
		return err
	}
	notificationUUIDs, err := uuidList(ctx, tx,
		`SELECT uuid FROM notifications WHERE id IN ? ORDER BY uuid`, row.Defaults.NotificationIDs)
	if err != nil {
		return err
	}
	payload, err := BuildMonitorTemplatePayload(&row, groupUUIDs, notificationUUIDs)
	if err != nil {
		return err
	}
	return e.write(ctx, tx, models.EntityMonitorTemplate, SyncIdentity{
		LocalID: row.ID, UUID: row.UUID, Revision: row.Revision,
		OriginNodeID: row.OriginNodeID, UpdatedAt: row.UpdatedAt,
	}, models.ActionUpsert, payload)
}

// EmitStatusPage publishes a status page and its selection.
func (e *SyncEmitter) EmitStatusPage(ctx context.Context, tx *gorm.DB, id uint) error {
	if e.skip(ctx) {
		return nil
	}
	var row models.StatusPage
	if err := tx.WithContext(ctx).First(&row, id).Error; err != nil {
		return fmt.Errorf("reading the status page to publish: %w", err)
	}
	monitorUUIDs, err := uuidList(ctx, tx,
		`SELECT m.uuid FROM monitors m
		   JOIN status_page_monitors s ON s.monitor_id = m.id
		  WHERE s.status_page_id = ? ORDER BY m.uuid`, row.ID)
	if err != nil {
		return err
	}
	groupUUIDs, err := uuidList(ctx, tx,
		`SELECT g.uuid FROM monitor_groups g
		   JOIN status_page_groups s ON s.group_id = g.id
		  WHERE s.status_page_id = ? ORDER BY g.uuid`, row.ID)
	if err != nil {
		return err
	}
	payload, err := BuildStatusPagePayload(&row, monitorUUIDs, groupUUIDs)
	if err != nil {
		return err
	}
	return e.write(ctx, tx, models.EntityStatusPage, SyncIdentity{
		LocalID: row.ID, UUID: row.UUID, Revision: row.Revision,
		OriginNodeID: row.OriginNodeID, UpdatedAt: row.UpdatedAt,
	}, models.ActionUpsert, payload)
}

// EmitNotification publishes a delivery channel.
//
// The payload carries the channel secrets, which is the point: the node that owns
// the notification election is the one that sends, so it must hold them. That is
// why federated mode requires TLS between the peers. Its monitor links are a
// separate entity.
func (e *SyncEmitter) EmitNotification(ctx context.Context, tx *gorm.DB, id uint) error {
	if e.skip(ctx) {
		return nil
	}
	var row models.Notification
	if err := tx.WithContext(ctx).First(&row, id).Error; err != nil {
		return fmt.Errorf("reading the channel to publish: %w", err)
	}
	payload, err := BuildNotificationPayload(&row)
	if err != nil {
		return err
	}
	return e.write(ctx, tx, models.EntityNotification, SyncIdentity{
		LocalID: row.ID, UUID: row.UUID, Revision: row.Revision,
		OriginNodeID: row.OriginNodeID, UpdatedAt: row.UpdatedAt,
	}, models.ActionUpsert, payload)
}

// EmitMonitorGroupMember publishes or removes one membership.
//
// The relation is its own entity, so a membership change is one record with one
// revision, whether it came from the monitor form or from the group editor; the
// identity is derived from the pair, so both ends compute the same one.
func (e *SyncEmitter) EmitMonitorGroupMember(ctx context.Context, tx *gorm.DB, monitorUUID, groupUUID string, linked bool) error {
	if monitorUUID == "" || groupUUID == "" {
		return nil
	}
	uuid := models.MonitorGroupMemberUUID(monitorUUID, groupUUID)
	return e.emitLink(ctx, tx, models.EntityMonitorGroupMember, uuid, linked, func() ([]byte, error) {
		return BuildMonitorGroupMemberPayload(monitorUUID, groupUUID)
	})
}

// EmitMonitorNotification publishes or removes one monitor-to-channel link.
func (e *SyncEmitter) EmitMonitorNotification(ctx context.Context, tx *gorm.DB, monitorUUID, notificationUUID string, linked bool) error {
	if monitorUUID == "" || notificationUUID == "" {
		return nil
	}
	uuid := models.MonitorNotificationUUID(monitorUUID, notificationUUID)
	return e.emitLink(ctx, tx, models.EntityMonitorNotification, uuid, linked, func() ([]byte, error) {
		return BuildMonitorNotificationPayload(monitorUUID, notificationUUID)
	})
}

// emitLink publishes (linked) or tombstones (!linked) one relation.
//
// A relation has no version column of its own, so its revision comes from
// sync_objects: the last one applied plus one. LocalID stays zero because a
// relation is addressed by its two ends, not by a row id.
func (e *SyncEmitter) emitLink(ctx context.Context, tx *gorm.DB, entity, uuid string, linked bool, build func() ([]byte, error)) error {
	if e.skip(ctx) || uuid == "" {
		return nil
	}
	if !linked {
		return e.EmitDelete(ctx, tx, entity, uuid)
	}
	revision, err := e.nextRevision(ctx, tx, uuid)
	if err != nil {
		return err
	}
	payload, err := build()
	if err != nil {
		return err
	}
	return e.write(ctx, tx, entity, SyncIdentity{UUID: uuid, Revision: revision}, models.ActionUpsert, payload)
}

// nextRevision returns the revision to use for a record that carries no version
// column of its own: the last revision applied for that uuid, plus one.
func (e *SyncEmitter) nextRevision(ctx context.Context, tx *gorm.DB, uuid string) (int64, error) {
	var current models.SyncObject
	err := tx.WithContext(ctx).Where("uuid = ?", uuid).First(&current).Error
	switch {
	case err == gorm.ErrRecordNotFound:
		return 1, nil
	case err != nil:
		return 0, fmt.Errorf("reading the sync identity of %s: %w", uuid, err)
	}
	return current.Revision + 1, nil
}

// MonitorLinks is the published state of a monitor's relations: the groups it
// belongs to and the channels it notifies, as uuids.
type MonitorLinks struct {
	GroupUUIDs        map[string]bool
	NotificationUUIDs map[string]bool
}

// EmptyMonitorLinks is the "no relation" state, used when a monitor is created.
func EmptyMonitorLinks() MonitorLinks {
	return MonitorLinks{GroupUUIDs: map[string]bool{}, NotificationUUIDs: map[string]bool{}}
}

// RowUUID reads the global identity of a local row.
//
// The delete paths need it BEFORE deleting: once the row is gone its uuid cannot
// be read any more, and the tombstone has to name it.
func (e *SyncEmitter) RowUUID(ctx context.Context, tx *gorm.DB, entity string, localID uint) (string, error) {
	table, ok := syncEntities[entity]
	if !ok {
		return "", fmt.Errorf("unknown sync entity %q", entity)
	}
	var row struct{ UUID string }
	if err := tx.WithContext(ctx).Table(table).Select("uuid").Where("id = ?", localID).Scan(&row).Error; err != nil {
		return "", fmt.Errorf("reading the uuid of %s %d: %w", entity, localID, err)
	}
	return row.UUID, nil
}

// SnapshotMonitorLinks reads the relations a monitor has right now.
//
// The caller takes it BEFORE rewriting them, so that PublishMonitorLinks can tell
// what actually moved: a re-save with the same groups must not put anything in the
// outbox.
func (e *SyncEmitter) SnapshotMonitorLinks(ctx context.Context, tx *gorm.DB, monitorID uint) (MonitorLinks, error) {
	links := EmptyMonitorLinks()
	if e.skip(ctx) {
		return links, nil
	}
	groups, err := uuidList(ctx, tx,
		`SELECT g.uuid FROM monitor_groups g
		   JOIN monitor_group_members m ON m.group_id = g.id
		  WHERE m.monitor_id = ? ORDER BY g.uuid`, monitorID)
	if err != nil {
		return links, err
	}
	for _, uuid := range groups {
		links.GroupUUIDs[uuid] = true
	}
	channels, err := uuidList(ctx, tx,
		`SELECT n.uuid FROM notifications n
		   JOIN monitor_notifications l ON l.notification_id = n.id
		  WHERE l.monitor_id = ? ORDER BY n.uuid`, monitorID)
	if err != nil {
		return links, err
	}
	for _, uuid := range channels {
		links.NotificationUUIDs[uuid] = true
	}
	return links, nil
}

// PublishMonitorLinks emits the difference between the relations a monitor had
// and the ones it has now: an upsert for what appeared, a tombstone for what went
// away, and nothing at all for a relation that did not move.
func (e *SyncEmitter) PublishMonitorLinks(ctx context.Context, tx *gorm.DB, monitorID uint, before MonitorLinks) error {
	if e.skip(ctx) {
		return nil
	}
	monitorUUID, err := e.RowUUID(ctx, tx, models.EntityMonitor, monitorID)
	if err != nil {
		return err
	}
	if monitorUUID == "" {
		// The monitor is gone (a delete): the caller tombstones the relations
		// itself, because a monitor that no longer exists cannot be the subject of
		// a diff.
		return nil
	}
	now, err := e.SnapshotMonitorLinks(ctx, tx, monitorID)
	if err != nil {
		return err
	}

	addedGroups, removedGroups := models.DiffSets(before.GroupUUIDs, now.GroupUUIDs)
	for _, groupUUID := range addedGroups {
		if err := e.EmitMonitorGroupMember(ctx, tx, monitorUUID, groupUUID, true); err != nil {
			return err
		}
	}
	for _, groupUUID := range removedGroups {
		if err := e.EmitMonitorGroupMember(ctx, tx, monitorUUID, groupUUID, false); err != nil {
			return err
		}
	}

	addedChannels, removedChannels := models.DiffSets(before.NotificationUUIDs, now.NotificationUUIDs)
	for _, channelUUID := range addedChannels {
		if err := e.EmitMonitorNotification(ctx, tx, monitorUUID, channelUUID, true); err != nil {
			return err
		}
	}
	for _, channelUUID := range removedChannels {
		if err := e.EmitMonitorNotification(ctx, tx, monitorUUID, channelUUID, false); err != nil {
			return err
		}
	}
	return nil
}

// TombstoneMonitorLinks removes the relations of a monitor that is being deleted.
func (e *SyncEmitter) TombstoneMonitorLinks(ctx context.Context, tx *gorm.DB, monitorUUID string, links MonitorLinks) error {
	if e.skip(ctx) || monitorUUID == "" {
		return nil
	}
	for groupUUID := range links.GroupUUIDs {
		if err := e.EmitMonitorGroupMember(ctx, tx, monitorUUID, groupUUID, false); err != nil {
			return err
		}
	}
	for channelUUID := range links.NotificationUUIDs {
		if err := e.EmitMonitorNotification(ctx, tx, monitorUUID, channelUUID, false); err != nil {
			return err
		}
	}
	return nil
}

// It is called with the uuid read BEFORE the delete (the row is gone by now). The
// tombstone's revision is the last applied revision plus one, so it beats every
// earlier change and a re-delivered old payload cannot resurrect the row.
func (e *SyncEmitter) EmitDelete(ctx context.Context, tx *gorm.DB, entity, uuid string) error {
	if e.skip(ctx) || uuid == "" {
		return nil
	}
	var current models.SyncObject
	err := tx.WithContext(ctx).Where("uuid = ?", uuid).First(&current).Error
	if err != nil && err != gorm.ErrRecordNotFound {
		return fmt.Errorf("reading the sync identity to delete: %w", err)
	}

	tombstone := SyncIdentity{
		LocalID:      current.LocalID,
		UUID:         uuid,
		Revision:     current.Revision + 1,
		OriginNodeID: current.OriginNodeID,
		UpdatedAt:    time.Now().UTC(),
	}
	return e.write(ctx, tx, entity, tombstone, models.ActionDelete, nil)
}

// write appends the outbox row and updates the uuid mapping, both inside the
// caller's transaction: the log can never drift from the data.
func (e *SyncEmitter) write(ctx context.Context, tx *gorm.DB, entity string, identity SyncIdentity, action string, payload []byte) error {
	if identity.UUID == "" {
		return fmt.Errorf("refusing to publish %s without a uuid", entity)
	}
	if identity.Revision <= 0 {
		identity.Revision = 1
	}
	if identity.OriginNodeID == "" {
		// A row created before the sync identity existed carries no origin yet
		// (database.Backfill stamps it on the next boot). Attributing the change to
		// this node keeps the merge tie-break meaningful in the meantime, instead
		// of leaving an empty component in the ordering key.
		identity.OriginNodeID = e.cfg.NodeID
	}
	if identity.UpdatedAt.IsZero() {
		identity.UpdatedAt = time.Now().UTC()
	}
	outbox := models.SyncOutbox{
		Entity:       entity,
		UUID:         identity.UUID,
		Action:       action,
		OriginNodeID: e.cfg.NodeID,
		Revision:     identity.Revision,
		Payload:      string(payload),
		PayloadHash:  utils.SHA256Hex(string(payload)),
		CreatedAt:    time.Now().UTC(),
	}
	if err := tx.WithContext(ctx).Create(&outbox).Error; err != nil {
		return fmt.Errorf("appending to the outbox: %w", err)
	}
	return e.track(ctx, tx, entity, identity, action == models.ActionDelete)
}

// track keeps sync_objects, the memory of the merge, in step with the outbox.
func (e *SyncEmitter) track(ctx context.Context, tx *gorm.DB, entity string, identity SyncIdentity, deleted bool) error {
	var deletedAt *time.Time
	if deleted {
		now := time.Now().UTC()
		deletedAt = &now
	}
	object := models.SyncObject{
		UUID:         identity.UUID,
		Entity:       entity,
		LocalID:      identity.LocalID,
		OriginNodeID: identity.OriginNodeID,
		Revision:     identity.Revision,
		DeletedAt:    deletedAt,
		// The row's own updated_at, NOT the current time: this timestamp is a
		// component of the merge order, so stamping it here would make two nodes
		// disagree about the same change.
		UpdatedAt: identity.UpdatedAt,
	}
	return tx.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "uuid"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"entity", "local_id", "origin_node_id", "revision", "deleted_at", "updated_at",
		}),
	}).Create(&object).Error
}

// uuidList runs a query that returns a single uuid column.
func uuidList(ctx context.Context, tx *gorm.DB, query string, args ...any) ([]string, error) {
	var uuids []string
	if err := tx.WithContext(ctx).Raw(query, args...).Scan(&uuids).Error; err != nil {
		return nil, fmt.Errorf("resolving uuids: %w", err)
	}
	return uuids, nil
}
