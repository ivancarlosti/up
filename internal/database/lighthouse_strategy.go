package database

import (
	"fmt"
	"log/slog"

	"gorm.io/gorm"

	"github.com/ivancarlosti/up/internal/models"
)

// lighthouseStrategyTable is the table the web performance job upserts: the last
// observation of a monitor, one row per run.
const lighthouseStrategyTable = "monitor_lighthouses"

// widenLighthousePrimaryKey promotes monitor_lighthouses from one row per monitor
// to one row per (monitor, strategy).
//
// Why it is needed: the job used to measure a page once, so the monitor id was
// the whole identity of the row. A monitor can now measure both runs (mobile AND
// desktop), which needs one row per run, and the upsert looks the row up by
// (monitor_id, strategy).
//
// Why it is spelled out here: AutoMigrate only ever ADDS what is missing. It
// creates a missing table, adds missing columns and indexes, but it never
// rewrites the primary key of a table that already exists, so the key has to be
// replaced by hand. The pass is idempotent (it only runs while the key is still
// the old one) and it rewrites no row: the table holds at most one row per
// monitor today, and the strategy it already carries is therefore unique per
// monitor.
func widenLighthousePrimaryKey(db *gorm.DB, log *slog.Logger) error {
	columns, err := primaryKeyColumns(db, lighthouseStrategyTable)
	if err != nil {
		return err
	}
	if len(columns) == 0 {
		return nil // no table (or no key): nothing to widen
	}
	if len(columns) == 2 && columns[0] == "monitor_id" && columns[1] == "strategy" {
		return nil // already widened
	}
	// A primary key cannot hold a NULL, and a row written before the strategy was
	// normalized may carry an empty one: it is filled with the documented default
	// first, which is exactly the run the job would have stored for it.
	if err := db.Exec(
		"UPDATE "+lighthouseStrategyTable+" SET strategy = ? WHERE strategy IS NULL OR strategy = ''",
		models.DefaultLighthouseStrategy,
	).Error; err != nil {
		return fmt.Errorf("normalizing the lighthouse strategies: %w", err)
	}
	if err := db.Exec(
		"ALTER TABLE " + lighthouseStrategyTable +
			" MODIFY strategy varchar(20) NOT NULL, DROP PRIMARY KEY, ADD PRIMARY KEY (monitor_id, strategy)",
	).Error; err != nil {
		return fmt.Errorf("widening the primary key of %s: %w", lighthouseStrategyTable, err)
	}
	log.Info("monitor_lighthouses primary key widened",
		"from", columns, "to", []string{"monitor_id", "strategy"})
	return nil
}

// primaryKeyColumns returns the columns of the primary key of a table, in the
// order the key declares them (empty when the table has none).
func primaryKeyColumns(db *gorm.DB, table string) ([]string, error) {
	var columns []string
	err := db.Raw(`SELECT COLUMN_NAME FROM information_schema.KEY_COLUMN_USAGE
		WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ? AND CONSTRAINT_NAME = 'PRIMARY'
		ORDER BY ORDINAL_POSITION`, table).Scan(&columns).Error
	if err != nil {
		return nil, fmt.Errorf("reading the primary key of %s: %w", table, err)
	}
	return columns, nil
}
