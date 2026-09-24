package models

import (
	"encoding/json"
	"time"
)

// Wire payloads of the peer protocol (docs/clustering-modes.md, section 5).
//
// They are spelled out field by field rather than embedding the models, on
// purpose: the wire form is a contract, and a field added to a model for the
// dashboard must not silently appear on the wire. Local ids never appear here at
// all — references travel as uuids.

// MonitorPayload is the configuration of a monitor.
//
// It carries no group or channel list: those relations are separate entities (see
// EntityMonitorGroupMember), so that a change made from either end has exactly
// one writer.
type MonitorPayload struct {
	UUID         string    `json:"uuid"`
	OriginNodeID string    `json:"origin_node_id"`
	Revision     int64     `json:"revision"`
	UpdatedAt    time.Time `json:"updated_at"`

	Name                   string      `json:"name"`
	Type                   MonitorType `json:"type"`
	Active                 bool        `json:"active"`
	Description            string      `json:"description"`
	IntervalSeconds        int         `json:"interval_seconds"`
	Retries                int         `json:"retries"`
	RetriesIntervalSeconds int         `json:"retries_interval_seconds"`
	TimeoutSeconds         int         `json:"timeout_seconds"`
	ResendIntervalSeconds  int         `json:"resend_interval_seconds"`
	UpsideDown             bool        `json:"upside_down"`
	RunOn                  string      `json:"run_on"`
	NodeID                 string      `json:"node_id"`
	// RunOnNodes is the subset used by run_on=some, comma separated (see models.Monitor).
	RunOnNodes   string `json:"run_on_nodes"`
	Tags         string `json:"tags"`
	CertWatch    bool   `json:"cert_watch"`
	CertNotify   bool   `json:"cert_notify"`
	CertWarnDays string `json:"cert_warn_days"`
	// The domain expiration watch travels like the certificate switches: a monitor
	// created on one node has to watch the same things on every other node.
	DomainWatch     bool       `json:"domain_watch"`
	DomainNotify    bool       `json:"domain_notify"`
	DomainWarnDays  string     `json:"domain_warn_days"`
	DomainExpiresAt *time.Time `json:"domain_expires_at,omitempty"`
	// TemplateUUID is the template the monitor follows (empty when it follows
	// none): the template's global uuid, never a local id.
	TemplateUUID string        `json:"template_uuid,omitempty"`
	Config       MonitorConfig `json:"config"`
}

// MonitorGroupPayload is a group. Its members are a separate entity, for the same
// reason as the monitor's groups.
type MonitorGroupPayload struct {
	UUID         string    `json:"uuid"`
	OriginNodeID string    `json:"origin_node_id"`
	Revision     int64     `json:"revision"`
	UpdatedAt    time.Time `json:"updated_at"`

	Name        string `json:"name"`
	Description string `json:"description"`
	Color       string `json:"color"`
	SortOrder   int    `json:"sort_order"`
}

// MonitorGroupMemberPayload is one membership. Its identity is derived from the
// pair (models.MonitorGroupMemberUUID), so the payload only names its two ends.
type MonitorGroupMemberPayload struct {
	MonitorUUID string `json:"monitor_uuid"`
	GroupUUID   string `json:"group_uuid"`
}

// MonitorNotificationPayload is one monitor-to-channel link, for the same reason.
type MonitorNotificationPayload struct {
	MonitorUUID      string `json:"monitor_uuid"`
	NotificationUUID string `json:"notification_uuid"`
}

// MonitorTemplatePayload is a template plus its default links. The defaults
// travel WITHOUT the two link id fields: they are local ids (see
// TemplateDefaults) and the links travel as uuids below.
type MonitorTemplatePayload struct {
	UUID         string    `json:"uuid"`
	OriginNodeID string    `json:"origin_node_id"`
	Revision     int64     `json:"revision"`
	UpdatedAt    time.Time `json:"updated_at"`

	Name        string           `json:"name"`
	Description string           `json:"description"`
	Type        MonitorType      `json:"type"`
	Config      MonitorConfig    `json:"config"`
	Defaults    TemplateDefaults `json:"defaults"`

	GroupUUIDs        []string `json:"group_uuids"`
	NotificationUUIDs []string `json:"notification_uuids"`
}

// StatusPagePayload is a status page plus its selection.
type StatusPagePayload struct {
	UUID         string    `json:"uuid"`
	OriginNodeID string    `json:"origin_node_id"`
	Revision     int64     `json:"revision"`
	UpdatedAt    time.Time `json:"updated_at"`

	Slug        string `json:"slug"`
	Title       string `json:"title"`
	Description string `json:"description"`
	FooterText  string `json:"footer_text"`
	Theme       string `json:"theme"`
	IsPublic    bool   `json:"is_public"`
	ShowUptime  bool   `json:"show_uptime"`
	ShowCharts  bool   `json:"show_charts"`
	ShowTags    bool   `json:"show_tags"`
	ShowExpiry  bool   `json:"show_expiry"`
	CustomCSS   string `json:"custom_css"`

	Monitors []StatusPageItemPayload  `json:"monitors"`
	Groups   []StatusPageGroupPayload `json:"groups"`
}

// StatusPageItemPayload is one monitor of a page's selection, WITH its display
// order and its per-page overrides.
//
// Sending the uuids alone is not enough: the order and the overrides live in the
// join row, so a receiver rebuilding the selection from uuids would silently reset
// both — the page would look different on the peer for no reason anyone could see.
type StatusPageItemPayload struct {
	UUID        string `json:"uuid"`
	DisplayName string `json:"display_name"`
	GroupName   string `json:"group_name"`
	SortOrder   int    `json:"sort_order"`
	ShowUptime  bool   `json:"show_uptime"`
	ShowChart   bool   `json:"show_chart"`
}

// StatusPageGroupPayload is one group section of a page, for the same reason.
type StatusPageGroupPayload struct {
	UUID        string `json:"uuid"`
	DisplayName string `json:"display_name"`
	SortOrder   int    `json:"sort_order"`
}

// NotificationPayload is a delivery channel.
//
// It carries the channel configuration, credentials included: the node that owns
// the notification election is the one that sends, so it must hold them. That is
// why the peers must run over TLS in federated mode.
//
// Its links to monitors are a separate entity (EntityMonitorNotification).
type NotificationPayload struct {
	UUID         string    `json:"uuid"`
	OriginNodeID string    `json:"origin_node_id"`
	Revision     int64     `json:"revision"`
	UpdatedAt    time.Time `json:"updated_at"`

	Name                  string             `json:"name"`
	Type                  NotificationType   `json:"type"`
	Active                bool               `json:"active"`
	IsDefault             bool               `json:"is_default"`
	ResendIntervalSeconds int                `json:"resend_interval_seconds"`
	Config                NotificationConfig `json:"config"`
}

// PeerVotePayload is the verdict of one monitor, published by the node that took
// the measurement (GET /api/cluster/sync/votes).
//
// The status travels as its stable lower case name, so a peer can reject a value it
// does not understand instead of silently mapping it to "unknown".
type PeerVotePayload struct {
	MonitorUUID string    `json:"monitor_uuid"`
	Status      string    `json:"status"`
	LatencyMS   int64     `json:"latency_ms"`
	Message     string    `json:"message"`
	Important   bool      `json:"important"`
	CheckedAt   time.Time `json:"checked_at"`
}

// SyncVotesResponse answers GET /api/cluster/sync/votes. It carries the verdicts of
// THIS node only: the receiver merges them with its own heartbeats and with the other
// peers' votes, which is what keeps `ANY_NODE_FAILS`/`ALL_NODES_FAIL`/`QUORUM`
// meaningful without a shared heartbeats table.
type SyncVotesResponse struct {
	NodeID          string            `json:"node_id"`
	ProtocolVersion int               `json:"protocol_version"`
	ServerTime      time.Time         `json:"server_time"`
	Votes           []PeerVotePayload `json:"votes"`
}

// SettingPayload is one whitelisted setting as it travels between nodes.
type SettingPayload struct {
	Key       string    `json:"key"`
	Value     string    `json:"value"`
	UpdatedAt time.Time `json:"updated_at"`
}

// SyncSettingsResponse answers GET /api/cluster/sync/settings.
//
// It carries the presentation settings and, when its own opt-in is on, the session secret.
// Nothing else: API tokens, IP rules and per-node rate limits are deliberately never
// synchronised (docs/clustering-modes.md, section 10), because security configuration
// stays a per-node concern.
type SyncSettingsResponse struct {
	NodeID          string           `json:"node_id"`
	ProtocolVersion int              `json:"protocol_version"`
	ServerTime      time.Time        `json:"server_time"`
	Settings        []SettingPayload `json:"settings"`
}

// SyncChangePayload is one entry of a changes or snapshot batch.
type SyncChangePayload struct {
	// ID is the outbox row id: it is the cursor a peer advances, and it is local
	// to the SENDING node (the receiver stores it as sync_peers.last_change_id).
	ID     int64  `json:"id"`
	Entity string `json:"entity"`
	UUID   string `json:"uuid"`
	Action string `json:"action"`
	// OriginNodeID is the node that produced the change (the editor).
	OriginNodeID string `json:"origin_node_id"`
	Revision     int64  `json:"revision"`
	// Payload is the marshalled row: null on a delete.
	Payload     json.RawMessage `json:"payload,omitempty"`
	PayloadHash string          `json:"payload_hash"`
	UpdatedAt   time.Time       `json:"updated_at"`
}

// SyncChangesResponse answers GET /api/cluster/sync/changes.
type SyncChangesResponse struct {
	Changes   []SyncChangePayload `json:"changes"`
	NextSince int64               `json:"next_since"`
	HasMore   bool                `json:"has_more"`
	// CursorExpired is set when the caller's cursor points before the oldest row
	// still kept (the tombstone prune removed what it needed). The caller must fall
	// back to a full snapshot: serving the surviving rows would silently skip the
	// pruned ones forever.
	CursorExpired   bool  `json:"cursor_expired"`
	LatestRevision  int64 `json:"latest_revision"`
	ProtocolVersion int   `json:"protocol_version"`
}

// SyncSnapshotResponse answers GET /api/cluster/sync/snapshot: the current state
// of one entity, in deterministic uuid order, paged.
type SyncSnapshotResponse struct {
	Entity          string              `json:"entity"`
	Page            int                 `json:"page"`
	HasMore         bool                `json:"has_more"`
	ProtocolVersion int                 `json:"protocol_version"`
	Changes         []SyncChangePayload `json:"changes"`
}

// SyncManifestEntity is the identity of one entity on the node that answers.
type SyncManifestEntity struct {
	Entity string `json:"entity"`
	// Count and MaxRevision are informational; the checksum is what the peer
	// compares.
	Count       int64  `json:"count"`
	MaxRevision int64  `json:"max_revision"`
	Checksum    string `json:"checksum"`
}

// SyncManifestResponse answers GET /api/cluster/sync/manifest. It is the healing
// trigger: two nodes that agree on every checksum are in sync and skip the
// snapshot.
type SyncManifestResponse struct {
	ProtocolVersion int                  `json:"protocol_version"`
	Entities        []SyncManifestEntity `json:"entities"`
}

// OutboxToChange renders an outbox row as a wire change.
func OutboxToChange(row SyncOutbox) SyncChangePayload {
	var payload json.RawMessage
	if row.Payload != "" && json.Valid([]byte(row.Payload)) {
		payload = json.RawMessage(row.Payload)
	}
	// An invalid payload is handed over as ABSENT rather than as broken JSON. A
	// json.RawMessage that cannot be marshalled fails the WHOLE response, so a single
	// corrupt row would make this node unservable to every peer — a far worse failure
	// than one change that cannot be applied, which the receiver contains as a dead
	// letter.
	// The version timestamp, not the moment the row entered this outbox: the merge
	// compares the timestamp of the version, and the local side compares the row's
	// own updated_at (see SyncOutbox.UpdatedAt). Rows written before the column
	// existed fall back to the insert time.
	versionAt := row.UpdatedAt
	if versionAt.IsZero() {
		versionAt = row.CreatedAt
	}
	return SyncChangePayload{
		ID:           int64(row.ID),
		Entity:       row.Entity,
		UUID:         row.UUID,
		Action:       row.Action,
		OriginNodeID: row.OriginNodeID,
		Revision:     row.Revision,
		Payload:      payload,
		PayloadHash:  row.PayloadHash,
		UpdatedAt:    versionAt,
	}
}

// SyncedEntities lists every entity the protocol carries, in a stable order.
func SyncedEntities() []string {
	return []string{
		EntityMonitor,
		EntityMonitorGroup,
		EntityMonitorGroupMember,
		EntityMonitorTemplate,
		EntityStatusPage,
		EntityNotification,
		EntityMonitorNotification,
	}
}
