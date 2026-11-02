package services

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/ivancarlosti/up/internal/models"
)

// systemOwner is the resolved ownership of one system.
type systemOwner struct {
	NodeID   string
	NodeName string
	// Valid is true only while the lease is inside its validity window AND the
	// node that holds it is still reporting heartbeats. A lease whose node died
	// is a stale row, not an owner.
	Valid bool
}

// ownerIndex resolves the owner of every system.
//
// One query for the leases and one for the online nodes: the listing asks this on
// every render, so it must not be a query per row.
func (s *SystemService) ownerIndex(ctx context.Context) (map[uint]systemOwner, error) {
	var leases []models.SystemLease
	if err := s.db.WithContext(ctx).Find(&leases).Error; err != nil {
		return nil, ErrInternal(fmt.Errorf("reading the system leases: %w", err))
	}
	out := make(map[uint]systemOwner, len(leases))
	if len(leases) == 0 {
		return out, nil
	}
	online, err := s.onlineNodeIDs(ctx, s.db)
	if err != nil {
		return nil, ErrInternal(err)
	}
	names, err := s.nodeNames(ctx)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	for _, lease := range leases {
		out[lease.SystemID] = systemOwner{
			NodeID:   lease.NodeID,
			NodeName: names[lease.NodeID],
			Valid:    lease.ExpiresAt.After(now) && online[lease.NodeID],
		}
	}
	return out, nil
}

// onlineNodeIDs is the set of nodes inside the liveness grace period, using the
// same rule as the rest of the cluster (models.NodeOfflineSeconds).
func (s *SystemService) onlineNodeIDs(ctx context.Context, db *gorm.DB) (map[string]bool, error) {
	var ids []string
	grace := time.Now().UTC().Add(-models.NodeOfflineSeconds * time.Second)
	if err := db.WithContext(ctx).Model(&models.Node{}).
		Where("last_heartbeat >= ?", grace).
		Pluck("node_id", &ids).Error; err != nil {
		return nil, fmt.Errorf("reading the online nodes: %w", err)
	}
	out := make(map[string]bool, len(ids))
	for _, id := range ids {
		out[id] = true
	}
	return out, nil
}

// nodeNames resolves the display name of every node.
func (s *SystemService) nodeNames(ctx context.Context) (map[string]string, error) {
	var nodes []models.Node
	if err := s.db.WithContext(ctx).Select("node_id", "name").Find(&nodes).Error; err != nil {
		return nil, ErrInternal(fmt.Errorf("reading the node names: %w", err))
	}
	out := make(map[string]string, len(nodes))
	for _, node := range nodes {
		out[node.NodeID] = node.Name
	}
	return out, nil
}

// decorate fills the runtime fields of a system.
//
// The status is derived from the freshness of the last sample, not from the
// socket: a system whose agent reconnected a second ago but whose last sample is
// ten minutes old has no data to show, and calling it up would be a lie. Three
// missed polls (models.SystemDownAfter) are tolerated, so one slow collection does
// not flip the badge.
// decorate turns a stored row into the API view: the runtime status, the owner
// and the alert rules of the machine.
func (s *SystemService) decorate(system *models.System, owner systemOwner) SystemView {
	view := SystemView{System: *system}
	view.Connected = s.agents.connected(system.ID)
	if owner.NodeID != "" {
		view.OwnerNode = owner.NodeID
		view.OwnerNodeName = owner.NodeName
	}
	now := time.Now().UTC()
	switch {
	case !system.Enabled:
		view.Status = models.SystemStatusPending
	case system.LastSampleAt == nil:
		view.Status = models.SystemStatusPending
	case now.Sub(*system.LastSampleAt) > models.SystemDownAfter:
		view.Status = models.SystemStatusDown
	default:
		view.Status = models.SystemStatusUp
	}
	return view
}

// ClaimSystem tries to become the owner of a system.
//
// The rules, in order: a node that already owns the lease renews it; a lease that
// expired or whose node went offline is taken over; a lease held by a live node is
// respected. The row is locked for update, so two nodes asking at the same instant
// cannot both win.
//
// It reports whether this node owns the system now. A caller that loses must not
// poll: two nodes sampling one machine would double every row of the history.
func (s *SystemService) ClaimSystem(ctx context.Context, systemID uint) (bool, error) {
	if s.cluster != nil && !s.cluster.CanOwnAgent(ctx) {
		s.log.Debug("not taking the agent connection: this node is not settled yet", "system_id", systemID)
		return false, nil
	}
	nodeID := s.nodeID()
	now := time.Now().UTC()
	claimed := false
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		online, err := s.onlineNodeIDs(ctx, tx)
		if err != nil {
			return err
		}
		var lease models.SystemLease
		readErr := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("system_id = ?", systemID).First(&lease).Error
		switch {
		case readErr == nil:
			if lease.NodeID != nodeID && lease.ExpiresAt.After(now) && online[lease.NodeID] {
				return nil
			}
		case errors.Is(readErr, gorm.ErrRecordNotFound):
			// No owner yet: the insert below creates the row.
		default:
			return fmt.Errorf("reading the lease of system %d: %w", systemID, readErr)
		}
		claimed = true
		expires := now.Add(models.LeaseTTL())
		if readErr == nil {
			return tx.Model(&models.SystemLease{}).Where("system_id = ?", systemID).
				Updates(map[string]any{
					"node_id":    nodeID,
					"expires_at": expires,
					"updated_at": now,
				}).Error
		}
		return tx.Create(&models.SystemLease{
			SystemID:  systemID,
			NodeID:    nodeID,
			ExpiresAt: expires,
			UpdatedAt: now,
		}).Error
	})
	if err != nil {
		return false, ErrInternal(err)
	}
	if claimed {
		s.log.Info("this node now owns a system", "system_id", systemID, "node_id", nodeID)
		s.publish("system.owner", map[string]any{"id": systemID, "node_id": nodeID})
	}
	return claimed, nil
}

// RenewSystem extends the lease of a system this node owns.
//
// It reports false when the lease is not ours any more: another node took over
// (this node was unreachable), and the caller must drop the connection instead of
// writing samples nobody asked for.
func (s *SystemService) RenewSystem(ctx context.Context, systemID uint) (bool, error) {
	now := time.Now().UTC()
	result := s.db.WithContext(ctx).Model(&models.SystemLease{}).
		Where("system_id = ? AND node_id = ?", systemID, s.nodeID()).
		Updates(map[string]any{"expires_at": now.Add(models.LeaseTTL()), "updated_at": now})
	if result.Error != nil {
		return false, ErrInternal(fmt.Errorf("renewing the lease of system %d: %w", systemID, result.Error))
	}
	return result.RowsAffected > 0, nil
}

// ReleaseSystem drops the lease of a system (a clean shutdown, a disabled or
// deleted system), so the handover does not wait for the expiry.
func (s *SystemService) ReleaseSystem(ctx context.Context, systemID uint) error {
	result := s.db.WithContext(ctx).
		Where("system_id = ? AND node_id = ?", systemID, s.nodeID()).
		Delete(&models.SystemLease{})
	if result.Error != nil {
		return ErrInternal(fmt.Errorf("releasing the lease of system %d: %w", systemID, result.Error))
	}
	if result.RowsAffected > 0 {
		s.publish("system.owner", map[string]any{"id": systemID, "node_id": ""})
	}
	return nil
}
