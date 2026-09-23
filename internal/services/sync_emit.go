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
func BuildMonitorPayload(m *models.Monitor, groupUUIDs, notificationUUIDs []string) ([]byte, error) {
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

		GroupUUIDs:        orEmpty(groupUUIDs),
		NotificationUUIDs: orEmpty(notificationUUIDs),
	})
}

// BuildMonitorGroupPayload renders a group for the wire.
func BuildMonitorGroupPayload(g *models.MonitorGroup, monitorUUIDs []string) ([]byte, error) {
	return json.Marshal(models.MonitorGroupPayload{
		UUID:         g.UUID,
		OriginNodeID: g.OriginNodeID,
		Revision:     g.Revision,
		UpdatedAt:    g.UpdatedAt,

		Name:        g.Name,
		Description: g.Description,
		Color:       g.Color,
		SortOrder:   g.SortOrder,

		MonitorUUIDs: orEmpty(monitorUUIDs),
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
func BuildNotificationPayload(n *models.Notification, monitorUUIDs []string) ([]byte, error) {
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

		MonitorUUIDs: orEmpty(monitorUUIDs),
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
	return e.cfg != nil && e.cfg.ClusterMode == config.ClusterModeFederated
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
	groupUUIDs, err := uuidList(ctx, tx,
		`SELECT g.uuid FROM monitor_groups g
		   JOIN monitor_group_members m ON m.group_id = g.id
		  WHERE m.monitor_id = ? ORDER BY g.uuid`, row.ID)
	if err != nil {
		return err
	}
	notificationUUIDs, err := uuidList(ctx, tx,
		`SELECT n.uuid FROM notifications n
		   JOIN monitor_notifications l ON l.notification_id = n.id
		  WHERE l.monitor_id = ? ORDER BY n.uuid`, row.ID)
	if err != nil {
		return err
	}
	payload, err := BuildMonitorPayload(&row, groupUUIDs, notificationUUIDs)
	if err != nil {
		return err
	}
	return e.write(ctx, tx, models.EntityMonitor, SyncIdentity{
		LocalID: row.ID, UUID: row.UUID, Revision: row.Revision,
		OriginNodeID: row.OriginNodeID, UpdatedAt: row.UpdatedAt,
	}, models.ActionUpsert, payload)
}

// EmitMonitorGroup publishes a group and its membership.
func (e *SyncEmitter) EmitMonitorGroup(ctx context.Context, tx *gorm.DB, id uint) error {
	if e.skip(ctx) {
		return nil
	}
	var row models.MonitorGroup
	if err := tx.WithContext(ctx).First(&row, id).Error; err != nil {
		return fmt.Errorf("reading the group to publish: %w", err)
	}
	monitorUUIDs, err := uuidList(ctx, tx,
		`SELECT m.uuid FROM monitors m
		   JOIN monitor_group_members g ON g.monitor_id = m.id
		  WHERE g.group_id = ? ORDER BY m.uuid`, row.ID)
	if err != nil {
		return err
	}
	payload, err := BuildMonitorGroupPayload(&row, monitorUUIDs)
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

// EmitNotification publishes a delivery channel and its links.
//
// The payload carries the channel secrets, which is the point: the node that
// owns the notification election is the one that sends, so it must hold them.
// That is why federated mode requires TLS between the peers.
func (e *SyncEmitter) EmitNotification(ctx context.Context, tx *gorm.DB, id uint) error {
	if e.skip(ctx) {
		return nil
	}
	var row models.Notification
	if err := tx.WithContext(ctx).First(&row, id).Error; err != nil {
		return fmt.Errorf("reading the channel to publish: %w", err)
	}
	monitorUUIDs, err := uuidList(ctx, tx,
		`SELECT m.uuid FROM monitors m
		   JOIN monitor_notifications l ON l.monitor_id = m.id
		  WHERE l.notification_id = ? ORDER BY m.uuid`, row.ID)
	if err != nil {
		return err
	}
	payload, err := BuildNotificationPayload(&row, monitorUUIDs)
	if err != nil {
		return err
	}
	return e.write(ctx, tx, models.EntityNotification, SyncIdentity{
		LocalID: row.ID, UUID: row.UUID, Revision: row.Revision,
		OriginNodeID: row.OriginNodeID, UpdatedAt: row.UpdatedAt,
	}, models.ActionUpsert, payload)
}

// EmitDelete publishes a tombstone for a row that has just been removed.
//
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
		UpdatedAt:    time.Now().UTC(),
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
