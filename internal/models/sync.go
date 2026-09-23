package models

import (
	"sort"
	"time"
)

// ProtocolVersion is the version of the peer API (docs/clustering-federated.md).
// It is exchanged in every ping so a node talking to an incompatible build says
// so once, instead of failing in a confusing way later.
const ProtocolVersion = 1

// Peer status values stored in SyncPeer.Status.
const (
	// PeerStatusUnknown means the peer has never answered.
	PeerStatusUnknown = "unknown"
	// PeerStatusOnline means the last ping succeeded.
	PeerStatusOnline = "online"
	// PeerStatusError means the last ping failed (see LastError).
	PeerStatusError = "error"
	// PeerStatusIncompatible means it answered with a different protocol version
	// or a different cluster mode: syncing with it is pointless.
	PeerStatusIncompatible = "incompatible"
)

// SyncPeer is this node's own view of another node.
//
// It belongs to the node that stores it: every node keeps its own rows, which is
// why the peer table is local in federated mode. `nodes.last_heartbeat` answers
// "when did this peer last report itself"; SyncPeer answers "when did *I* last
// reach it, and for how long has that been working", which is what the settle
// time needs (docs/clustering-federated.md, sections 7 and 8).
type SyncPeer struct {
	PeerNodeID string `gorm:"primaryKey;size:64" json:"peer_node_id"`
	// PeerName, PeerAPIURL and PeerVersion mirror what the last successful ping
	// reported. The version is stored here and not on nodes: Node.Version is a
	// runtime field (gorm:"-") and is never persisted.
	PeerName        string `gorm:"size:150" json:"peer_name"`
	PeerAPIURL      string `gorm:"size:255" json:"peer_api_url"`
	PeerVersion     string `gorm:"size:64" json:"peer_version"`
	ProtocolVersion int    `gorm:"not null;default:0" json:"protocol_version"`
	// Status is the outcome of the last exchange with this peer.
	Status string `gorm:"size:20;not null;default:unknown" json:"status"`
	// LastSeenAt is updated by an outbound ping *and* by a valid inbound request
	// from that peer, so a node reachable in one direction only still shows up.
	LastSeenAt *time.Time `json:"last_seen_at"`
	// LastSuccessAt is updated by a successful outbound ping only.
	LastSuccessAt *time.Time `json:"last_success_at"`
	// OnlineSince is when the peer started being continuously reachable. It is
	// cleared as soon as an exchange fails, and it is what stops a flapping peer
	// from taking the leader role (and the notification duty) mid-incident.
	OnlineSince *time.Time `json:"online_since"`
	// LastError is the last failure, cleared by a success.
	LastError   string     `json:"last_error"`
	LastErrorAt *time.Time `json:"last_error_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

// Settled reports whether the peer has been continuously online for at least
// settle. A peer that never answered, or whose last exchange failed, is never
// settled.
func (p *SyncPeer) Settled(now time.Time, settle time.Duration) bool {
	if p.OnlineSince == nil {
		return false
	}
	return now.Sub(*p.OnlineSince) >= settle
}

// Entity names used by the synchronisation. They are stored in sync_outbox,
// sync_objects, sync_conflicts and sync_pending_links, and they are part of the
// peer protocol: renaming one is a breaking change.
const (
	EntityMonitor         = "monitor"
	EntityMonitorGroup    = "monitor_group"
	EntityStatusPage      = "status_page"
	EntityMonitorTemplate = "monitor_template"
	EntityNotification    = "notification"
)

// Actions of a sync_outbox row.
const (
	ActionUpsert = "upsert"
	ActionDelete = "delete"
)

// Link kinds carried inside a payload and tracked by sync_pending_links.
const (
	LinkGroupUUIDs        = "group_uuids"
	LinkNotificationUUIDs = "notification_uuids"
	LinkMonitorUUIDs      = "monitor_uuids"
)

// SyncOutbox is the change log of this node: every local write to a synchronised
// entity appends one row here, inside the same transaction as the write itself,
// so the log can never drift from the data.
//
// It is written by SyncObject-aware services and read by the peer endpoint that
// serves changes; nothing else scans for differences.
type SyncOutbox struct {
	ID     uint   `gorm:"primaryKey" json:"id"`
	Entity string `gorm:"size:32;not null;index:idx_sync_outbox_entity" json:"entity"`
	UUID   string `gorm:"size:36;not null;index:idx_sync_outbox_uuid" json:"uuid"`
	// Action is upsert or delete (a delete carries no payload).
	Action string `gorm:"size:10;not null" json:"action"`
	// OriginNodeID is the node that PRODUCED this change (the editor). It is not
	// the same as the row's own origin_node_id, which is the creator and never
	// changes: attribution of a conflict needs the editor.
	OriginNodeID string `gorm:"size:64" json:"origin_node_id"`
	Revision     int64  `gorm:"not null;default:1" json:"revision"`
	// Payload is the JSON body of the row, with uuid references instead of local
	// ids. It is null on a delete.
	Payload string `gorm:"type:text" json:"payload,omitempty"`
	// PayloadHash detects a change that only reorders keys: it is used for change
	// detection, never as an authenticator.
	PayloadHash string    `gorm:"size:64" json:"payload_hash"`
	CreatedAt   time.Time `json:"created_at"`
}

// TableName keeps the table name stable.
func (SyncOutbox) TableName() string { return "sync_outbox" }

// SyncObject maps a global uuid to the local row that holds it, and remembers the
// revision this node has applied. It is the authoritative memory of the merge:
// the local row itself cannot say which revision it came from.
type SyncObject struct {
	UUID   string `gorm:"primaryKey;size:36" json:"uuid"`
	Entity string `gorm:"size:32;not null;index:idx_sync_objects_entity" json:"entity"`
	// LocalID is the primary key of the row in its own table.
	LocalID      uint   `gorm:"not null" json:"local_id"`
	OriginNodeID string `gorm:"size:64" json:"origin_node_id"`
	Revision     int64  `gorm:"not null;default:1" json:"revision"`
	// DeletedAt marks a tombstone: the local row is gone, the identity is kept so
	// a late upsert cannot resurrect it.
	DeletedAt *time.Time `json:"deleted_at"`
	UpdatedAt time.Time  `json:"updated_at"`
}

// TableName keeps the table name stable.
func (SyncObject) TableName() string { return "sync_objects" }

// SyncConflict stores an edit that lost the merge, so a concurrent edit is never
// silently dropped: it is reported in the Admin UI instead.
type SyncConflict struct {
	ID           uint      `gorm:"primaryKey" json:"id"`
	Entity       string    `gorm:"size:32;not null" json:"entity"`
	UUID         string    `gorm:"size:36;not null;index:idx_sync_conflicts_uuid" json:"uuid"`
	KeptOrigin   string    `gorm:"size:64" json:"kept_origin"`
	KeptRevision int64     `json:"kept_revision"`
	LostOrigin   string    `gorm:"size:64" json:"lost_origin"`
	LostRevision int64     `json:"lost_revision"`
	DetectedAt   time.Time `json:"detected_at"`
}

// TableName keeps the table name stable.
func (SyncConflict) TableName() string { return "sync_conflicts" }

// SyncPendingLink records a reference that arrived before its target. It is a
// marker, not a queue: the healing pass (manifest mismatch, or the forced
// reconcile it triggers) re-delivers the container with its full link list, and
// by then the target exists.
type SyncPendingLink struct {
	ID uint `gorm:"primaryKey" json:"id"`
	// Entity and UUID identify the container (the monitor, the group, ...).
	Entity string `gorm:"size:32;not null;uniqueIndex:idx_sync_pending_link,priority:1" json:"entity"`
	UUID   string `gorm:"size:36;not null;uniqueIndex:idx_sync_pending_link,priority:2" json:"uuid"`
	// Kind is which link list the reference came from (group_uuids, ...).
	Kind string `gorm:"size:32;not null;uniqueIndex:idx_sync_pending_link,priority:3" json:"kind"`
	// MissingUUID is the reference that could not be resolved yet.
	MissingUUID string    `gorm:"size:36;not null;uniqueIndex:idx_sync_pending_link,priority:4" json:"missing_uuid"`
	CreatedAt   time.Time `json:"created_at"`
}

// TableName keeps the table name stable.
func (SyncPendingLink) TableName() string { return "sync_pending_links" }

func SettledPeers(peers []SyncPeer, now time.Time, settle time.Duration) []SyncPeer {
	out := make([]SyncPeer, 0, len(peers))
	for _, peer := range peers {
		if peer.Settled(now, settle) {
			out = append(out, peer)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].PeerNodeID < out[j].PeerNodeID })
	return out
}
