package models

import "time"

// NodeOfflineSeconds is the grace period after which a node that stopped
// reporting heartbeats is considered offline (the specification requires two
// minutes).
const NodeOfflineSeconds = 120

// NodeHeartbeatSeconds is how often each node refreshes its own liveness
// timestamp in the nodes table.
const NodeHeartbeatSeconds = 30

// Node is a member of the cluster. Every node points to the same external
// database, which is what keeps the monitor list synchronised.
type Node struct {
	ID   uint   `gorm:"primaryKey" json:"id"`
	Name string `gorm:"size:150;not null" json:"name"`
	// NodeID is the NODE_ID environment variable (stable across restarts).
	NodeID string `gorm:"size:64;uniqueIndex;not null" json:"node_id"`
	// APIURL is the base URL other nodes use to reach this one (APP_URL).
	APIURL string `gorm:"size:255" json:"api_url"`
	// PrivateKeyHash is the SHA-256 hash of the cluster private key that this
	// node accepted when joining.
	PrivateKeyHash string     `gorm:"size:128" json:"-"`
	LastHeartbeat  *time.Time `json:"last_heartbeat"`
	Status         NodeStatus `gorm:"size:20;not null;default:offline" json:"status"`
	IsPrimary      bool       `gorm:"not null;default:false" json:"is_primary"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`

	// IsSelf marks the node the current process is running on (runtime only).
	IsSelf bool `gorm:"-" json:"is_self"`
	// Version is the build version reported by the node (runtime only).
	Version string `gorm:"-" json:"version,omitempty"`
}

// Online reports whether the node reported a heartbeat inside the grace period.
func (n *Node) Online(now time.Time) bool {
	if n.LastHeartbeat == nil {
		return false
	}
	return now.Sub(*n.LastHeartbeat) <= NodeOfflineSeconds*time.Second
}

// ClusterSettings holds the cluster wide behaviour rules. The table always
// contains a single row (id = 1).
type ClusterSettings struct {
	ID uint `gorm:"primaryKey" json:"id"`
	// FailureStrategy: ANY_NODE_FAILS | ALL_NODES_FAIL | QUORUM
	FailureStrategy FailureStrategy `gorm:"size:32;not null;default:ALL_NODES_FAIL" json:"failure_strategy"`
	// NodeUnavailableStrategy: IGNORE | MARK_DEGRADED
	NodeUnavailableStrategy NodeUnavailableStrategy `gorm:"size:32;not null;default:IGNORE" json:"node_unavailable_strategy"`
	// NotificationSender: PRIMARY_ONLY | ANY_WITH_LOCK
	NotificationSender NotificationSenderStrategy `gorm:"size:32;not null;default:ANY_WITH_LOCK" json:"notification_sender"`
	UpdatedAt          time.Time                  `json:"updated_at"`
}

// DefaultClusterSettings returns the initial configuration used on the first
// boot and by single node installations.
func DefaultClusterSettings() *ClusterSettings {
	return &ClusterSettings{
		ID:                      1,
		FailureStrategy:         FailureStrategyAllNodesFail,
		NodeUnavailableStrategy: NodeUnavailableIgnore,
		NotificationSender:      NotificationSenderAnyWithLock,
	}
}

// ClusterStatus is the payload of GET /api/cluster/status.
type ClusterStatus struct {
	Enabled bool `json:"enabled"`
	// Mode is the CLUSTER_MODE this node runs in: "shared" (every node points at
	// the same database) or "federated" (one database per node, the nodes
	// synchronise over the peer API). It is additive: an older frontend simply
	// ignores it.
	Mode             string           `json:"mode"`
	NodeID           string           `json:"node_id"`
	NodeName         string           `json:"node_name"`
	IsPrimary        bool             `json:"is_primary"`
	PrivateKey       string           `json:"private_key,omitempty"`
	Settings         *ClusterSettings `json:"settings"`
	Nodes            []Node           `json:"nodes"`
	OnlineNodes      int              `json:"online_nodes"`
	TotalNodes       int              `json:"total_nodes"`
	OfflineNodeNames []string         `json:"offline_node_names"`
}
