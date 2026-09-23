package database

import (
	"context"
	"fmt"
	"log/slog"

	"gorm.io/gorm"

	"github.com/ivancarlosti/up/internal/config"
)

// backfillTarget is one table that needs its sync identity stamped. The names
// are the real table names (monitor_groups, not monitor_groups' Go type), so
// the statements can be written once and driven from this table.
type backfillTarget struct {
	// table is the physical table name.
	table string
	// needsUUID is true for the tables whose uuid column was introduced with
	// the sync identity (monitors, status_pages). The other two already had a
	// uuid, filled by their BeforeCreate hook since the first release.
	needsUUID bool
}

// backfillTargets is the ordered list of tables carrying the sync identity of
// docs/clustering-federated.md.
var backfillTargets = []backfillTarget{
	{table: "monitors", needsUUID: true},
	{table: "status_pages", needsUUID: true},
	{table: "notifications", needsUUID: true},
	{table: "monitor_groups"},
	{table: "monitor_templates"},
}

// Backfill stamps the sync identity (uuid, origin_node_id, revision) on the rows
// that predate federated mode. It runs on every boot and is idempotent by
// construction: every statement selects only the rows that are still missing a
// value, so the second run matches nothing.
//
// Why the bootstrap needs it at all: the UUID is what makes a row
// identifiable across databases (the auto-increment id is meaningful inside one
// database only), and origin_node_id/revision are the two inputs of the
// last-writer-wins merge. Rows created after this release get all three from
// the BeforeCreate hook of their model.
//
// The UUID statement relies on MySQL evaluating UUID() once per row, which
// keeps the whole pass to three statements per table: no paging, no batching
// and no partial state to recover from.
func Backfill(ctx context.Context, db *gorm.DB, cfg *config.Config, log *slog.Logger) error {
	total := 0
	for _, target := range backfillTargets {
		changed, err := backfillTable(ctx, db, target, cfg.NodeID)
		if err != nil {
			return err
		}
		if changed > 0 {
			log.Info("sync identity backfilled", "table", target.table, "rows", changed)
		}
		total += changed
	}
	if total == 0 {
		log.Debug("sync identity backfill: nothing to do")
	} else {
		log.Info("sync identity backfill finished", "rows", total, "origin_node_id", cfg.NodeID)
	}
	return nil
}

// backfillTable stamps one table and returns how many rows it changed.
func backfillTable(ctx context.Context, db *gorm.DB, target backfillTarget, nodeID string) (int, error) {
	changed := 0

	if target.needsUUID {
		// UUID() is evaluated for every row the WHERE clause selects, so a
		// single statement gives each row a distinct value. The column is
		// nullable (see models.Monitor): the rows created before this release
		// hold NULL, which is what makes the unique index accept them.
		result := db.WithContext(ctx).Exec(
			fmt.Sprintf("UPDATE %s SET uuid = UUID() WHERE uuid IS NULL", target.table))
		if result.Error != nil {
			return changed, fmt.Errorf("backfilling %s.uuid: %w", target.table, result.Error)
		}
		changed += int(result.RowsAffected)
	}

	result := db.WithContext(ctx).Exec(
		fmt.Sprintf("UPDATE %s SET origin_node_id = ? WHERE origin_node_id = '' OR origin_node_id IS NULL", target.table),
		nodeID)
	if result.Error != nil {
		return changed, fmt.Errorf("backfilling %s.origin_node_id: %w", target.table, result.Error)
	}
	changed += int(result.RowsAffected)

	result = db.WithContext(ctx).Exec(
		fmt.Sprintf("UPDATE %s SET revision = 1 WHERE revision = 0 OR revision IS NULL", target.table))
	if result.Error != nil {
		return changed, fmt.Errorf("backfilling %s.revision: %w", target.table, result.Error)
	}
	changed += int(result.RowsAffected)

	return changed, nil
}
