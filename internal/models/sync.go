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

// SettledPeers returns the settled peers ordered by node id, which is the order
// the leader derivation and the notification ownership rely on: every node
// computes the same list from the same rule, so they pick the same winner
// without coordinating.
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
