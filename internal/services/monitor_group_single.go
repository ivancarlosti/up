package services

import (
	"context"
	"fmt"
	"log/slog"

	"gorm.io/gorm"

	"github.com/ivancarlosti/up/internal/config"
	"github.com/ivancarlosti/up/internal/models"
)

// ConsolidateSingleGroupMembership collapses the membership of every monitor to a
// single group, keeping the first group the monitor is shown under.
//
// It is the boot step that brings the data in line with the rule the rest of this
// release already enforces: a monitor belongs to at most one group. The write paths
// truncate the deprecated group_ids list to its first entry, so a monitor that
// belonged to three groups kept belonging to three until something rewrote it - and
// the only thing that rewrites a membership is an edit of that monitor, one monitor
// at a time. That is not a migration, hence this pass.
//
// The keeper is the group that comes FIRST in the group list the operator sees
// (`monitor_groups.sort_order`, then the id), so the monitor stays under the heading
// it is used to appearing under. The position inside a group is deliberately NOT the
// key: it orders the members of one group and says nothing about which group a
// monitor is "in". Every other membership is deleted, and each delete is published as
// a tombstone in the same transaction.
//
// The tombstones are not a formality. A federated peer keeps the memberships it was
// told about, and the reconciliation that compares the two inventories pulls the
// peer snapshot on a mismatch - so a membership that was never tombstoned is a
// membership the peer still believes in, and the next reconcile hands it back. On a
// standalone node the emitter is disabled and both calls are no-ops.
//
// Idempotent: a monitor with one membership (or none) is left completely alone, so
// every boot after the first one changes nothing.
//
// The unique index on monitor_group_members.monitor_id is deliberately NOT added by
// this release: it has to be created AFTER this pass (an index over a table that
// still holds duplicates fails and takes the boot with it) while this pass needs the
// uuid backfill that runs before it (the tombstones name uuids). The next release,
// which drops the deprecated keys, adds the index on top of data that is already
// clean.
func ConsolidateSingleGroupMembership(ctx context.Context, db *gorm.DB, cfg *config.Config, log *slog.Logger) error {
	if !db.Migrator().HasTable(&models.MonitorGroupMember{}) {
		// A fresh database: the table is created by this very boot and every write
		// takes the single group path, so there is nothing to consolidate.
		return nil
	}
	var monitorIDs []uint
	if err := db.WithContext(ctx).Model(&models.MonitorGroupMember{}).
		Select("monitor_id").
		Group("monitor_id").
		Having("COUNT(*) > 1").
		Order("monitor_id ASC").
		Pluck("monitor_id", &monitorIDs).Error; err != nil {
		return fmt.Errorf("looking for the monitors that belong to more than one group: %w", err)
	}
	if len(monitorIDs) == 0 {
		return nil
	}

	emit := NewSyncEmitter(db, cfg, log)
	removed := 0
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, monitorID := range monitorIDs {
			// The keeper is the group that comes FIRST in Admin > Monitor groups, so
			// the operator finds the monitor under the heading it is used to seeing.
			// The position inside a group (the membership sort_order) says nothing
			// about which group the monitor is "in": it orders the members of one
			// group, so it cannot decide between two of them. A membership whose
			// group row is gone (a dangling row an older release could leave) sorts
			// last, and the id breaks a tie between two groups that share a position.
			rows := []struct {
				GroupID uint `gorm:"column:group_id"`
			}{}
			if err := tx.Table("monitor_group_members AS m").
				Select("m.group_id").
				Joins("LEFT JOIN monitor_groups AS g ON g.id = m.group_id").
				Where("m.monitor_id = ?", monitorID).
				Order("COALESCE(g.sort_order, 2147483647) ASC, m.group_id ASC").
				Scan(&rows).Error; err != nil {
				return err
			}
			if len(rows) < 2 {
				// The row was consolidated by another node (or by a concurrent
				// boot) between the two queries: nothing left to do for it.
				continue
			}
			// The uuid of both ends, read BEFORE the deletes: a tombstone names
			// them, and once the rows are gone they cannot be read.
			monitorUUID, err := emit.RowUUID(ctx, tx, models.EntityMonitor, monitorID)
			if err != nil {
				return err
			}
			for _, row := range rows[1:] {
				groupUUID, err := emit.RowUUID(ctx, tx, models.EntityMonitorGroup, row.GroupID)
				if err != nil {
					return err
				}
				if err := tx.Where("monitor_id = ? AND group_id = ?", monitorID, row.GroupID).
					Delete(&models.MonitorGroupMember{}).Error; err != nil {
					return err
				}
				if err := emit.EmitMonitorGroupMember(ctx, tx, monitorUUID, groupUUID, false); err != nil {
					return err
				}
				removed++
			}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("consolidating the monitor groups: %w", err)
	}
	log.Info("monitor memberships consolidated to one group per monitor",
		"monitors", len(monitorIDs), "memberships_removed", removed)
	return nil
}
