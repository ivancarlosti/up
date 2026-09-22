package services

import (
	"context"
	"strings"
	"time"

	"github.com/ivancarlosti/up/internal/i18n"
	"github.com/ivancarlosti/up/internal/models"
	"github.com/ivancarlosti/up/internal/utils"
)

// RegisterNode (inbound) validates a join request received by the primary node
// and stores the new node in the registry.
func (s *ClusterService) RegisterNode(ctx context.Context, req JoinRequest) (*JoinResponse, error) {
	if !s.cfg.ClusterEnabled {
		return nil, ErrForbidden(i18n.CodeClusterDisabled, "clustering is disabled on this node")
	}
	expected, err := s.PrivateKey(ctx)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(req.PrivateKey) == "" || !utils.SecureCompare(strings.TrimSpace(req.PrivateKey), expected) {
		s.log.Warn("cluster join rejected: invalid private key", "node_id", req.NodeID)
		return nil, ErrForbidden(i18n.CodeClusterKeyInvalid, "the provided cluster private key does not match")
	}

	nodeID := strings.TrimSpace(req.NodeID)
	if nodeID == "" {
		return nil, ErrBadRequest(i18n.CodeValidation, "node_id is required")
	}
	if nodeID == s.cfg.NodeID {
		return nil, ErrConflict(i18n.CodeClusterPrimarySelf, "the joining node reports the same node_id as the primary")
	}
	nodeName := strings.TrimSpace(req.NodeName)
	if nodeName == "" {
		nodeName = nodeID
	}
	apiURL := strings.TrimRight(strings.TrimSpace(req.APIURL), "/")
	if apiURL == "" {
		return nil, ErrBadRequest(i18n.CodeValidation,
			"api_url is required (the joining node must send its APP_URL)")
	}

	now := time.Now().UTC()
	node := models.Node{
		NodeID:         nodeID,
		Name:           nodeName,
		APIURL:         apiURL,
		PrivateKeyHash: utils.SHA256Hex(req.PrivateKey),
		LastHeartbeat:  &now,
		Status:         models.NodeStatusOnline,
		IsPrimary:      false,
	}
	if err := s.UpsertNode(ctx, node); err != nil {
		return nil, ErrInternal(err)
	}
	if err := s.db.WithContext(ctx).Model(&models.Node{}).
		Where("node_id = ?", nodeID).
		Update("is_primary", false).Error; err != nil {
		s.log.Warn("could not flag the joined node", "error", err)
	}

	status, err := s.Status(ctx)
	if err != nil {
		return nil, err
	}
	s.log.Info("node joined the cluster", "node_id", nodeID, "name", nodeName, "api_url", apiURL)
	s.publish("cluster.nodes", nil)
	return &JoinResponse{Status: status, NodeID: nodeID, Joined: true, Message: "node registered"}, nil
}

// Leave removes a node from the registry (defaults to this node) and promotes a
// new primary when the leaving node was the primary one.
func (s *ClusterService) Leave(ctx context.Context, nodeID string) (*models.ClusterStatus, error) {
	if !s.cfg.ClusterEnabled {
		return nil, ErrForbidden(i18n.CodeClusterDisabled, "clustering is disabled on this node")
	}
	nodeID = strings.TrimSpace(nodeID)
	if nodeID == "" {
		nodeID = s.cfg.NodeID
	}

	var node models.Node
	if err := s.db.WithContext(ctx).Where("node_id = ?", nodeID).First(&node).Error; err != nil {
		return nil, ErrNotFound(i18n.CodeClusterDisabled, "node "+nodeID+" is not part of the cluster")
	}
	if err := s.RemoveNode(ctx, nodeID); err != nil {
		return nil, err
	}

	if node.IsPrimary {
		// Promote the oldest remaining node so the cluster keeps a primary.
		var successor models.Node
		if err := s.db.WithContext(ctx).
			Where("node_id <> ?", nodeID).
			Order("created_at ASC").
			First(&successor).Error; err == nil {
			if updateErr := s.db.WithContext(ctx).Model(&models.Node{}).
				Where("id = ?", successor.ID).
				Update("is_primary", true).Error; updateErr != nil {
				s.log.Warn("could not promote a new primary node", "error", updateErr)
			} else {
				s.log.Warn("primary node left the cluster, promoted", "node_id", successor.NodeID)
			}
		}
	}
	if nodeID == s.cfg.NodeID {
		s.log.Warn("this node left the cluster; join again with the primary key to participate")
	}
	return s.Status(ctx)
}
