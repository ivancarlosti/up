package database

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/ivancarlosti/up/internal/config"
	"github.com/ivancarlosti/up/internal/models"
)

// ClaimSelf registers this node (or refreshes its liveness metadata) and
// guarantees that exactly one node of the cluster carries the primary role.
//
// It is the single implementation of the claim: Seed() uses it on the boot path
// and ClusterService.EnsureSelf() on the runtime path. The two used to duplicate
// the logic, and the copy drifted.
//
// Why it needs a transaction and a lock: the original code counted the primaries
// and then inserted, with nothing between the two statements (and with
// SkipDefaultTransaction the count and the insert ran as two separate autocommit
// transactions). Two instances booting at the same time could therefore both read
// "there is no primary yet" and both register themselves as primary. A cluster
// with two primaries sends every notification twice under PRIMARY_ONLY, probes
// the run_on=primary monitors on two nodes at once, and shows two primaries in
// Admin > Cluster.
func ClaimSelf(ctx context.Context, db *gorm.DB, cfg *config.Config, log *slog.Logger) (*models.Node, error) {
	var (
		claimed models.Node
		created bool
	)

	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockPrimaryClaim(ctx, tx); err != nil {
			return err
		}
		// Repair before claiming: a deployment that already ended up with two
		// primaries (through the race this function closes) heals on next boot
		// instead of alerting twice forever.
		if err := reconcilePrimaries(ctx, tx, log); err != nil {
			return err
		}

		now := time.Now().UTC()
		var self models.Node
		err := tx.WithContext(ctx).Where("node_id = ?", cfg.NodeID).First(&self).Error
		switch {
		case err == nil:
			if err := tx.WithContext(ctx).Model(&models.Node{}).Where("id = ?", self.ID).
				Updates(map[string]any{
					"name":           cfg.NodeName,
					"api_url":        cfg.AppURL,
					"last_heartbeat": now,
					"status":         models.NodeStatusOnline,
					"updated_at":     now,
				}).Error; err != nil {
				return err
			}
			self.Name = cfg.NodeName
			self.APIURL = cfg.AppURL
			self.Status = models.NodeStatusOnline
			self.LastHeartbeat = &now
			claimed = self
			return nil
		case err != gorm.ErrRecordNotFound:
			return err
		}

		// First boot of this node: it takes the primary role only when no other
		// node holds it (the lock above makes the count reliable).
		var primaries int64
		if err := tx.WithContext(ctx).Model(&models.Node{}).
			Where("is_primary = ?", true).Count(&primaries).Error; err != nil {
			return err
		}
		node := models.Node{
			NodeID:        cfg.NodeID,
			Name:          cfg.NodeName,
			APIURL:        cfg.AppURL,
			LastHeartbeat: &now,
			Status:        models.NodeStatusOnline,
			IsPrimary:     primaries == 0,
		}
		if err := tx.WithContext(ctx).Create(&node).Error; err != nil {
			return fmt.Errorf("registering the node: %w", err)
		}
		claimed = node
		created = true
		return nil
	})
	if err != nil {
		return nil, err
	}

	// Logged after the commit so a rolled back claim is never announced.
	switch {
	case created && claimed.IsPrimary:
		log.Info("registered as the PRIMARY node of the cluster", "node_id", claimed.NodeID)
	case created:
		log.Info("node registered", "node_id", claimed.NodeID)
	default:
		log.Debug("node liveness refreshed", "node_id", claimed.NodeID)
	}
	return &claimed, nil
}

// lockPrimaryClaim serialises every primary claim on the single
// cluster_settings row.
//
// A locking read of a row that exists blocks a second transaction until the
// first commits, which is exactly the mutex the check-then-act inside ClaimSelf
// needs. The row is created by Seed() before any node is registered; it is
// created defensively here too, because a locking read over a missing row would
// lock nothing and silently give the race back.
func lockPrimaryClaim(ctx context.Context, tx *gorm.DB) error {
	if err := tx.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).
		Create(models.DefaultClusterSettings()).Error; err != nil {
		return fmt.Errorf("ensuring the cluster settings row: %w", err)
	}
	var id uint
	if err := tx.WithContext(ctx).
		Raw("SELECT id FROM cluster_settings WHERE id = ? FOR UPDATE", uint(1)).
		Row().Scan(&id); err != nil {
		return fmt.Errorf("locking the cluster settings row: %w", err)
	}
	return nil
}

// reconcilePrimaries demotes the extra primaries of a cluster that ended up with
// more than one. The oldest node keeps the role (node_id breaks a tie), so the
// choice is the same on every node and stable across boots.
func reconcilePrimaries(ctx context.Context, tx *gorm.DB, log *slog.Logger) error {
	var primaries []models.Node
	if err := tx.WithContext(ctx).Where("is_primary = ?", true).Find(&primaries).Error; err != nil {
		return err
	}
	keep, demote := pickPrimary(primaries)
	if len(demote) == 0 {
		return nil
	}
	ids := make([]uint, 0, len(demote))
	for _, node := range demote {
		ids = append(ids, node.ID)
	}
	if err := tx.WithContext(ctx).Model(&models.Node{}).Where("id IN ?", ids).
		Updates(map[string]any{"is_primary": false, "updated_at": time.Now().UTC()}).Error; err != nil {
		return err
	}
	log.Warn("more than one primary node found: keeping the oldest and demoting the rest",
		"kept", keep.NodeID, "demoted", len(ids))
	return nil
}

// pickPrimary is the pure core of the repair: given every row that claims the
// primary role it returns the one to keep (the oldest, node_id breaking a tie)
// and the ones to demote. It sorts a copy, so the caller does not depend on any
// particular row order.
func pickPrimary(primaries []models.Node) (keep models.Node, demote []models.Node) {
	if len(primaries) == 0 {
		return models.Node{}, nil
	}
	sorted := make([]models.Node, len(primaries))
	copy(sorted, primaries)
	sort.SliceStable(sorted, func(i, j int) bool {
		if !sorted[i].CreatedAt.Equal(sorted[j].CreatedAt) {
			return sorted[i].CreatedAt.Before(sorted[j].CreatedAt)
		}
		return sorted[i].NodeID < sorted[j].NodeID
	})
	return sorted[0], sorted[1:]
}
