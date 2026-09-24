package services

import (
	"context"
	"fmt"
	"time"

	"github.com/ivancarlosti/up/internal/config"
	"github.com/ivancarlosti/up/internal/models"
)

// Enabled reports whether clustering is turned on by the environment.
func (s *ClusterService) Enabled() bool { return s.cfg.ClusterEnabled }

// Mode is the CLUSTER_MODE this node runs in (config.ClusterModeShared or
// config.ClusterModeFederated). Both are implemented; federated stays refused at
// boot until the operator migration path is documented (section 20).
func (s *ClusterService) Mode() string { return s.cfg.ClusterMode }

// Federated reports whether this node runs with its own database and
// synchronises the configuration over the peer API.
//
// Nothing branches on it yet: it exists so the phases of
// docs/clustering-federated.md can switch a behaviour without changing every
// call site at once. Always false today, because config.validate refuses to
// boot with CLUSTER_MODE=federated.
func (s *ClusterService) Federated() bool { return s.cfg.ClusterMode == config.ClusterModeFederated }

// NodeID is this node identity (NODE_ID).
func (s *ClusterService) NodeID() string { return s.cfg.NodeID }

// NodeName is this node display name (NODE_NAME).
func (s *ClusterService) NodeName() string { return s.cfg.NodeName }

// Sweep updates the liveness status of every node:
//
//	last heartbeat < 60s   -> online
//	last heartbeat < 120s  -> degraded (late, still inside the grace period)
//	last heartbeat >= 120s -> offline
//
// A node whose status changes is removed from the voting of the affected
// monitors, exactly as required by the node_unavailable_strategy.
func (s *ClusterService) Sweep(ctx context.Context) error {
	now := time.Now().UTC()
	grace := now.Add(-models.NodeOfflineSeconds * time.Second)
	late := now.Add(-models.NodeOfflineSeconds / 2 * time.Second)

	offline := s.db.WithContext(ctx).Model(&models.Node{}).
		Where("last_heartbeat IS NULL OR last_heartbeat < ?", grace).
		Updates(map[string]any{"status": models.NodeStatusOffline, "updated_at": now})
	if offline.Error != nil {
		return offline.Error
	}
	degraded := s.db.WithContext(ctx).Model(&models.Node{}).
		Where("last_heartbeat >= ? AND last_heartbeat < ?", grace, late).
		Updates(map[string]any{"status": models.NodeStatusDegraded, "updated_at": now})
	if degraded.Error != nil {
		return degraded.Error
	}
	online := s.db.WithContext(ctx).Model(&models.Node{}).
		Where("last_heartbeat >= ?", late).
		Updates(map[string]any{"status": models.NodeStatusOnline, "updated_at": now})
	if online.Error != nil {
		return online.Error
	}
	if offline.RowsAffected > 0 || degraded.RowsAffected > 0 {
		s.log.Warn("cluster node liveness changed",
			"offline", offline.RowsAffected, "degraded", degraded.RowsAffected)
		s.publish("cluster.nodes", nil)
	}
	return nil
}

// Nodes returns every registered node (self included) with the IsSelf flag.
func (s *ClusterService) Nodes(ctx context.Context) ([]models.Node, error) {
	var nodes []models.Node
	if err := s.db.WithContext(ctx).Order("is_primary DESC, name ASC").Find(&nodes).Error; err != nil {
		return nil, ErrInternal(fmt.Errorf("listing cluster nodes: %w", err))
	}
	for i := range nodes {
		nodes[i].IsSelf = nodes[i].NodeID == s.cfg.NodeID
	}
	return nodes, nil
}

// IsPrimary reports whether the current process runs on the primary node. In a
// single node installation (CLUSTER_ENABLED=false) it is always true.
//
// In shared mode the primary is a fact of the shared table (`nodes.is_primary`), set by
// the first node that boots. In federated mode there is no such row, so the role is
// DERIVED from this node's own view: the settled node with the smallest node_id. Every
// node computes the same winner from its own view, which is what keeps `run_on=primary`
// probes and the notification duty on one node without a coordinator.
func (s *ClusterService) IsPrimary(ctx context.Context) bool {
	if !s.cfg.ClusterEnabled {
		return true
	}
	if s.Federated() {
		return s.leaderNodeID(ctx) == s.cfg.NodeID
	}
	var node models.Node
	if err := s.db.WithContext(ctx).Where("node_id = ?", s.cfg.NodeID).First(&node).Error; err != nil {
		return false
	}
	return node.IsPrimary
}

// leaderNodeID derivers the leader from this node's own view (federated mode).
//
// The candidates are the nodes that have been continuously reachable for
// CLUSTER_LEADER_SETTLE_SECONDS, and this node itself. The settle time is what keeps a
// peer blip, or a node that just restarted, from taking the duty mid-incident: it owns
// the same probes a `run_on=primary` monitor and the alerting of every monitor it owns.
//
// A lone node is its own leader immediately: there is nobody to flap against, and a
// single-node cluster must be able to alert right after a restart.
func (s *ClusterService) leaderNodeID(ctx context.Context) string {
	if s.cfg.ClusterLeaderMode == config.ClusterLeaderExplicit {
		if s.cfg.ClusterLeaderNodeID != "" {
			return s.cfg.ClusterLeaderNodeID
		}
		// Pinned to nothing is a misconfiguration; the lowest_id rule is still a
		// working cluster, so it is used and the boot validation reports it.
		return s.lowestSettledNodeID(ctx)
	}
	return s.lowestSettledNodeID(ctx)
}

// lowestSettledNodeID returns the smallest node id among this node and the settled
// peers, or "" when neither is settled yet.
func (s *ClusterService) lowestSettledNodeID(ctx context.Context) string {
	settle := time.Duration(s.cfg.ClusterLeaderSettleSeconds) * time.Second
	now := time.Now().UTC()
	selfSettled := settle <= 0 || now.Sub(s.startedAt) >= settle

	var rows []models.SyncPeer
	if err := s.db.WithContext(ctx).Find(&rows).Error; err != nil {
		// A read failure must not silently move the leadership: the next pass will see
		// the peers again, and answering "myself" is what the node already believed.
		s.log.Warn("could not read the peers to derive the leader", "error", err)
		if selfSettled {
			return s.cfg.NodeID
		}
		return ""
	}
	return models.PickLeader(s.cfg.NodeID, selfSettled, rows, now, settle)
}

// settledNodeIDs lists this node and the settled peers, in node_id order. It is the
// candidate set of the notification election.
func (s *ClusterService) settledNodeIDs(ctx context.Context) []string {
	settle := time.Duration(s.cfg.ClusterLeaderSettleSeconds) * time.Second
	now := time.Now().UTC()
	selfSettled := settle <= 0 || now.Sub(s.startedAt) >= settle

	var rows []models.SyncPeer
	if err := s.db.WithContext(ctx).Find(&rows).Error; err != nil {
		s.log.Warn("could not read the peers to elect the notification owner", "error", err)
		if selfSettled {
			return []string{s.cfg.NodeID}
		}
		return nil
	}
	return models.NotificationCandidates(s.cfg.NodeID, selfSettled, rows, now, settle)
}

// OnlineNodes splits the registered nodes into online and offline sets. Nodes
// in the "degraded" state are still counted as online but reported separately.
func (s *ClusterService) OnlineNodes(ctx context.Context) (online []models.Node, offline []models.Node, err error) {
	nodes, err := s.Nodes(ctx)
	if err != nil {
		return nil, nil, err
	}
	for _, node := range nodes {
		if node.Status == models.NodeStatusOffline {
			offline = append(offline, node)
			continue
		}
		online = append(online, node)
	}
	return online, offline, nil
}
