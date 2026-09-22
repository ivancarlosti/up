package services

import (
	"context"
	"fmt"
	"time"

	"github.com/ivancarlosti/up/internal/models"
)

// Enabled reports whether clustering is turned on by the environment.
func (s *ClusterService) Enabled() bool { return s.cfg.ClusterEnabled }

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
func (s *ClusterService) IsPrimary(ctx context.Context) bool {
	if !s.cfg.ClusterEnabled {
		return true
	}
	var node models.Node
	if err := s.db.WithContext(ctx).Where("node_id = ?", s.cfg.NodeID).First(&node).Error; err != nil {
		return false
	}
	return node.IsPrimary
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
