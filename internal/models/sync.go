package models

import (
	"crypto/sha256"
	"encoding/binary"
	"sort"
	"time"

	"github.com/google/uuid"
)

// ProtocolVersion is the version of the peer API (docs/clustering-modes.md).
// It is exchanged in every ping so a node talking to an incompatible build says
// so once, instead of failing in a confusing way later.
// ProtocolVersion is the version of the peer wire format. A peer that speaks a
// different one is refused with an explicit error instead of being fed a batch it
// would mis-read.
//
// History:
//   - 1: first version of the peer API (ping, status, changes).
//   - 2: the status page selection travels as records (order and per-item
//     overrides included) instead of two sorted uuid lists. Version 1 could not
//     express the display order, so applying a page on a peer silently reset it.
const ProtocolVersion = 2

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
// time needs (docs/clustering-modes.md, sections 7 and 8).
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
	// LastChangeID is the cursor of this node into the peer's outbox: the highest
	// sync_outbox id already applied. It is advanced inside the apply transaction,
	// so a crash mid-batch re-pulls instead of losing changes.
	LastChangeID int64 `gorm:"not null;default:0" json:"last_change_id"`
	// LastManifestAt and LastManifestOK record the healing pass: when the checksums
	// were last compared and whether they agreed.
	LastManifestAt *time.Time `json:"last_manifest_at"`
	LastManifestOK bool       `gorm:"not null;default:false" json:"last_manifest_ok"`
	UpdatedAt      time.Time  `json:"updated_at"`
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
	// EntityMonitorGroupMember and EntityMonitorNotification are the two
	// relations that can be edited from BOTH of their ends (a monitor form sets
	// its groups, a group editor sets its members; channel links travel the same
	// way). Describing such a relation inside both payloads would give it two
	// writers, and because the two descriptions live in different rows a
	// last-writer-wins merge could not settle them: each side would win on its own
	// row and the cluster would diverge silently. Every relation is therefore its
	// own entity with a deterministic identity (LinkEntityUUID).
	EntityMonitorGroupMember  = "monitor_group_member"
	EntityMonitorNotification = "monitor_notification"
)

// linkNamespace is the fixed namespace of the relation identifiers. Every node
// derives it the same way, which is what lets the two ends of a relation agree on
// its identity without coordinating.
var linkNamespace = uuid.NewSHA1(uuid.NameSpaceOID, []byte("github.com/ivancarlosti/up/cluster/link"))

// LinkEntityUUID returns the deterministic identity of the relation of a given
// kind between two rows: the same triple yields the same uuid on every node, so a
// membership change is an ordinary record with an ordinary revision rather than
// something each node has to name independently.
//
// The kind takes part in the identity on purpose. Without it a membership and a
// channel link over the same pair of uuids would share one identifier, and since
// sync_objects is keyed by that identifier the two entities would fight over a
// single row. With random uuids the collision is practically unreachable, but
// "practically" is not the same as "cannot".
//
// The two ends are sorted first, so the identity does not depend on the order the
// caller happens to pass them in — a swapped argument would otherwise silently
// name a different relation than the one being edited.
func LinkEntityUUID(kind, left, right string) string {
	if right < left {
		left, right = right, left
	}
	return uuid.NewSHA1(linkNamespace, []byte(kind+"|"+left+"|"+right)).String()
}

// MonitorGroupMemberUUID is the identity of one membership.
func MonitorGroupMemberUUID(monitorUUID, groupUUID string) string {
	return LinkEntityUUID(EntityMonitorGroupMember, monitorUUID, groupUUID)
}

// MonitorNotificationUUID is the identity of one monitor-to-channel link.
func MonitorNotificationUUID(monitorUUID, notificationUUID string) string {
	return LinkEntityUUID(EntityMonitorNotification, monitorUUID, notificationUUID)
}

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
	// UpdatedAt is the VERSION timestamp of the change: the updated_at of the row on
	// the node that produced it, which is the timestamp the merge compares.
	//
	// It cannot be derived from CreatedAt. The insert time of the outbox row is
	// always a little later than the row update it describes, so a node comparing
	// its own row's updated_at against a peer's outbox insert time is comparing two
	// different quantities — and both nodes then conclude that the other is newer.
	// Measured in the lab: two concurrent edits made each node adopt the other's
	// change, and each recorded the opposite conflict verdict.
	UpdatedAt time.Time `json:"updated_at"`
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
	// Payload is the last wire payload applied or published (empty for a
	// tombstone).
	//
	// It is stored so that a full resync can be enumerated from this table alone.
	// A relation has no row of its own (its uuid is derived from its two ends) and
	// a tombstone has neither, so without the payload here the snapshot could
	// describe live rows but never a deletion — and a missed deletion is exactly
	// the divergence the healing pass exists to repair.
	Payload   string    `gorm:"type:text" json:"-"`
	UpdatedAt time.Time `json:"updated_at"`
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
// marker, not a queue: the healing pass re-delivers the container with its full
// link list, and by then the target exists.
//
// It is also bounded. A marker is CLEARED as soon as the container applies with
// every reference resolved, and it stops triggering retries once Attempts passes
// the cap — otherwise a reference whose target never arrives would re-pull that
// entity's snapshot on every cycle for ever.
type SyncPendingLink struct {
	ID uint `gorm:"primaryKey" json:"id"`
	// Entity and UUID identify the container (the monitor, the group, ...).
	Entity string `gorm:"size:32;not null;uniqueIndex:idx_sync_pending_link,priority:1" json:"entity"`
	UUID   string `gorm:"size:36;not null;uniqueIndex:idx_sync_pending_link,priority:2" json:"uuid"`
	// Kind is which link list the reference came from (group_uuids, ...).
	Kind string `gorm:"size:32;not null;uniqueIndex:idx_sync_pending_link,priority:3" json:"kind"`
	// MissingUUID is the reference that could not be resolved yet.
	MissingUUID string `gorm:"size:36;not null;uniqueIndex:idx_sync_pending_link,priority:4" json:"missing_uuid"`
	// Attempts counts how many times the container was re-applied while this
	// reference stayed missing. Past maxPendingLinkAttempts the marker stops
	// triggering retries and stays visible for the operator instead.
	Attempts      int       `gorm:"not null;default:0" json:"attempts"`
	LastAttemptAt time.Time `json:"last_attempt_at"`
	CreatedAt     time.Time `json:"created_at"`
}

// TableName keeps the table name stable.
func (SyncPendingLink) TableName() string { return "sync_pending_links" }

// SyncDeadLetter is a change this node could not apply.
//
// It exists so that ONE bad change cannot stop a peer's synchronisation for ever:
// without it the apply transaction rolls back, the cursor never moves past the
// offending change, and every later change queues behind it. Past the attempt cap
// the change is skipped and recorded here, which turns a silent stall into a
// visible, bounded problem.
type SyncDeadLetter struct {
	ID uint `gorm:"primaryKey" json:"id"`
	// One row per (peer, change) so the attempts accumulate instead of piling up.
	PeerNodeID  string `gorm:"size:64;not null;uniqueIndex:idx_sync_dead_letter,priority:1" json:"peer_node_id"`
	ChangeID    int64  `gorm:"not null;uniqueIndex:idx_sync_dead_letter,priority:2" json:"change_id"`
	Entity      string `gorm:"size:32" json:"entity"`
	UUID        string `gorm:"size:36" json:"uuid"`
	PayloadHash string `gorm:"size:64" json:"payload_hash"`
	Attempts    int    `gorm:"not null;default:1" json:"attempts"`
	// Skipped is set when the change was finally passed over. A row that is not
	// skipped is still being retried, so the two states are distinguishable.
	Skipped   bool      `gorm:"not null;default:false" json:"skipped"`
	LastError string    `gorm:"size:500" json:"last_error"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// TableName keeps the table name stable.
func (SyncDeadLetter) TableName() string { return "sync_dead_letters" }

// PeerVote is one node's verdict for one monitor, as another node reported it.
//
// It is keyed by monitor UUID and not by a local id: a peer's ids are its own, so the
// uuid is the only reference the two nodes agree on. The row is replaced on every
// fetch, because a vote is a snapshot of a measurement and not a history.
//
// This is what lets a federated cluster evaluate a status strategy without a shared
// heartbeats table: every node publishes its own verdicts, fetches the peers', and
// merges both before aggregating.
type PeerVote struct {
	ID          uint   `gorm:"primaryKey" json:"id"`
	MonitorUUID string `gorm:"size:36;not null;uniqueIndex:idx_peer_votes,priority:1" json:"monitor_uuid"`
	NodeID      string `gorm:"size:64;not null;uniqueIndex:idx_peer_votes,priority:2" json:"node_id"`

	Status    HeartbeatStatus `gorm:"not null" json:"status"`
	LatencyMS int64           `json:"latency_ms"`
	Message   string          `gorm:"size:500" json:"message"`
	Important bool            `gorm:"not null;default:false" json:"important"`
	// CheckedAt is when the REPORTING node took the measurement. It is the timestamp
	// the freshness window is applied to, so a peer's old verdict expires exactly like
	// a local one.
	CheckedAt time.Time `json:"checked_at"`
	// FetchedAt is when this node read it, so a peer that stopped reporting is
	// distinguishable from one still reporting an old verdict.
	FetchedAt time.Time `json:"fetched_at"`
}

// TableName keeps the table name stable.
func (PeerVote) TableName() string { return "peer_votes" }

// CursorExpired reports whether a peer's cursor points before the oldest row its
// outbox still keeps.
//
// The rule is not `oldest > since`: a cursor that sits exactly one row before the
// oldest kept row is still able to continue from the next one, so only a gap of MORE
// than one row means the changes in between are gone for good. Getting this off by one
// either heals a peer on every cycle (noisy, and it re-sends snapshots for nothing) or
// silently skips the pruned changes (the divergence the healing pass exists to repair).
func CursorExpired(oldestID, since int64) bool {
	if oldestID <= 0 || since <= 0 {
		return false
	}
	return oldestID > since+1
}

// PickLeader returns the node that leads, from the local view only.
//
// The rule is the smallest node_id among the nodes that have been continuously online
// for the settle time, this node included when it has been up that long. Two details
// are deliberate:
//
//   - a LONE node leads immediately, settle time or not: there is nobody to flap
//     against, and a single-node cluster must be able to alert right after a restart;
//   - a node that is not settled yet yields to one that is, and when nobody is settled
//     the answer is empty — nobody leads, and nobody alerts, which is the safe side of
//     the failover described in section 8.
func PickLeader(selfID string, selfSettled bool, peers []SyncPeer, now time.Time, settle time.Duration) string {
	if len(peers) == 0 {
		return selfID
	}
	best := ""
	if selfSettled {
		best = selfID
	}
	for _, peer := range peers {
		if !peer.Settled(now, settle) {
			continue
		}
		if best == "" || peer.PeerNodeID < best {
			best = peer.PeerNodeID
		}
	}
	return best
}

// NotificationCandidates lists the nodes that may own a notification, from the local
// view only: this node when it is settled, plus the settled peers.
//
// A lone node is a candidate immediately, for the same reason it leads immediately: a
// single-node cluster must alert (section 8).
func NotificationCandidates(selfID string, selfSettled bool, peers []SyncPeer, now time.Time, settle time.Duration) []string {
	if len(peers) == 0 {
		return []string{selfID}
	}
	candidates := make([]string, 0, len(peers)+1)
	if selfSettled {
		candidates = append(candidates, selfID)
	}
	for _, peer := range peers {
		if peer.Settled(now, settle) {
			candidates = append(candidates, peer.PeerNodeID)
		}
	}
	sort.Strings(candidates)
	return candidates
}

// RendezvousOwner returns the candidate that owns a key under rendezvous (HRW)
// hashing, or "" when there is no candidate.
//
// Rendezvous is not a detail here: `fnv1a(key) % len(candidates)` reassigns roughly
// half of the keys whenever the candidate set changes, so a single peer blip would hand
// most monitors to a different notification owner — and with two nodes believing they
// own the same monitor, two alerts for one incident. Rendezvous moves only the keys of
// the node that actually joined or left.
//
// The key must be the monitor UUID alone: putting the event or a wall-clock bucket into
// it would reintroduce both the two-senders problem and a dependency on clocks that
// disagree.
func RendezvousOwner(key string, candidates []string) string {
	best := ""
	var bestScore uint64
	for _, candidate := range candidates {
		if candidate == "" {
			continue
		}
		digest := sha256.Sum256([]byte(key + "\x00" + candidate))
		score := binary.BigEndian.Uint64(digest[:8])
		// Strictly greater, so the answer cannot depend on the input order.
		if best == "" || score > bestScore {
			best, bestScore = candidate, score
		}
	}
	return best
}

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
