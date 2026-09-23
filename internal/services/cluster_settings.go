package services

import (
	"context"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/ivancarlosti/up/internal/i18n"
	"github.com/ivancarlosti/up/internal/models"
)

// Settings returns the cluster wide behaviour rules, creating the default row
// on the first call.
func (s *ClusterService) Settings(ctx context.Context) (*models.ClusterSettings, error) {
	var settings models.ClusterSettings
	err := s.db.WithContext(ctx).First(&settings, 1).Error
	if err == gorm.ErrRecordNotFound {
		settings = *models.DefaultClusterSettings()
		if err := s.db.WithContext(ctx).Create(&settings).Error; err != nil {
			return nil, ErrInternal(err)
		}
		return &settings, nil
	}
	if err != nil {
		return nil, ErrInternal(err)
	}
	return &settings, nil
}

// UpdateSettings validates and stores the behavior rules (admin only).
func (s *ClusterService) UpdateSettings(ctx context.Context, input *models.ClusterSettings) (*models.ClusterSettings, error) {
	if input.FailureStrategy != "" && !input.FailureStrategy.Valid() {
		return nil, ErrBadRequest(i18n.CodeClusterStrategy,
			"failure_strategy must be ANY_NODE_FAILS, ALL_NODES_FAIL or QUORUM")
	}
	if input.NodeUnavailableStrategy != "" && !input.NodeUnavailableStrategy.Valid() {
		return nil, ErrBadRequest(i18n.CodeClusterStrategy,
			"node_unavailable_strategy must be IGNORE or MARK_DEGRADED")
	}
	if input.NotificationSender != "" && !input.NotificationSender.Valid() {
		return nil, ErrBadRequest(i18n.CodeClusterStrategy,
			"notification_sender must be PRIMARY_ONLY or ANY_WITH_LOCK")
	}

	current, err := s.Settings(ctx)
	if err != nil {
		return nil, err
	}
	if input.FailureStrategy != "" {
		current.FailureStrategy = input.FailureStrategy
	}
	if input.NodeUnavailableStrategy != "" {
		current.NodeUnavailableStrategy = input.NodeUnavailableStrategy
	}
	if input.NotificationSender != "" {
		current.NotificationSender = input.NotificationSender
	}
	current.UpdatedAt = time.Now().UTC()

	err = s.db.WithContext(ctx).Model(&models.ClusterSettings{}).Where("id = ?", current.ID).Updates(map[string]any{
		"failure_strategy":          current.FailureStrategy,
		"node_unavailable_strategy": current.NodeUnavailableStrategy,
		"notification_sender":       current.NotificationSender,
		"updated_at":                current.UpdatedAt,
	}).Error
	if err != nil {
		return nil, ErrInternal(err)
	}
	s.log.Info("cluster settings updated",
		"failure_strategy", current.FailureStrategy,
		"node_unavailable_strategy", current.NodeUnavailableStrategy,
		"notification_sender", current.NotificationSender)
	s.publish("cluster.settings", current)
	return current, nil
}

// PrivateKey returns the shared cluster key.
func (s *ClusterService) PrivateKey(ctx context.Context) (string, error) {
	key, err := s.settings.ClusterPrivateKey(ctx)
	if err != nil {
		return "", ErrInternal(err)
	}
	return key, nil
}

// RegeneratePrivateKey replaces the shared key: every other node must join
// again with the new value.
func (s *ClusterService) RegeneratePrivateKey(ctx context.Context) (string, error) {
	key, err := s.settings.RegenerateClusterPrivateKey(ctx)
	if err != nil {
		return "", ErrInternal(err)
	}
	s.publish("cluster.key", map[string]any{"regenerated": true})
	return key, nil
}

// Status returns the payload of GET /api/cluster/status.
func (s *ClusterService) Status(ctx context.Context) (*models.ClusterStatus, error) {
	nodes, err := s.Nodes(ctx)
	if err != nil {
		return nil, err
	}
	settings, err := s.Settings(ctx)
	if err != nil {
		return nil, err
	}
	key, err := s.PrivateKey(ctx)
	if err != nil {
		return nil, err
	}

	status := &models.ClusterStatus{
		Enabled:    s.cfg.ClusterEnabled,
		Mode:       s.Mode(),
		NodeID:     s.cfg.NodeID,
		NodeName:   s.cfg.NodeName,
		IsPrimary:  s.IsPrimary(ctx),
		PrivateKey: key,
		Settings:   settings,
		Nodes:      nodes,
		TotalNodes: len(nodes),
	}
	for _, node := range nodes {
		if node.Status == models.NodeStatusOnline {
			status.OnlineNodes++
			continue
		}
		status.OfflineNodeNames = append(status.OfflineNodeNames, node.Name)
	}
	return status, nil
}

// UpsertNode stores (or refreshes) a node reported by a join request.
func (s *ClusterService) UpsertNode(ctx context.Context, node models.Node) error {
	now := time.Now().UTC()
	node.UpdatedAt = now
	node.CreatedAt = now
	return s.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "node_id"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"name", "api_url", "private_key_hash", "last_heartbeat", "status", "updated_at",
		}),
	}).Create(&node).Error
}

// RemoveNode deletes a node from the registry.
func (s *ClusterService) RemoveNode(ctx context.Context, nodeID string) error {
	result := s.db.WithContext(ctx).Where("node_id = ?", nodeID).Delete(&models.Node{})
	if result.Error != nil {
		return ErrInternal(result.Error)
	}
	if result.RowsAffected == 0 {
		return ErrNotFound(i18n.CodeClusterDisabled, "node "+nodeID+" is not part of the cluster")
	}
	s.log.Info("node removed from the cluster", "node_id", nodeID)
	s.publish("cluster.nodes", nil)
	return nil
}
