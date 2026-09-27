package database

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"time"

	"gorm.io/gorm"

	"github.com/ivancarlosti/up/internal/models"
)

// rollupHealHours is how many complete hours the boot pass recomputes even when
// the rollups already look up to date. It heals the two cases the incremental
// update cannot: the heartbeats stored before this release (first boot after the
// upgrade, when the rollup table is empty) and a crash between the heartbeat
// insert and its rollup update.
const rollupHealHours = 6

// rollupBackfillQuery rebuilds the hourly rollups of a time range from the raw
// heartbeats. It is the exact reduction the incremental update performs one
// heartbeat at a time (see heartbeatRollupUpsert), so both paths produce the
// same numbers.
//
// The statement is written for MariaDB/MySQL only (the two engines the project
// supports): VALUES(col) is what makes the INSERT ... SELECT ... ON DUPLICATE
// KEY UPDATE replace the bucket with the values just computed. It is deprecated
// in MySQL 8.0.20+, not removed, and it is the only portable spelling.
const rollupBackfillQuery = `
	INSERT INTO heartbeat_rollups
		(monitor_id, bucket_at, up, down, pending, maintenance, total,
		 up_latency_sum, up_latency_count, latency_min, latency_max, updated_at)
	SELECT monitor_id,
	       DATE_FORMAT(created_at, '%Y-%m-%d %H:00:00')                       AS bucket_at,
	       SUM(CASE WHEN status = 1 THEN 1 ELSE 0 END)                        AS up,
	       SUM(CASE WHEN status = 0 THEN 1 ELSE 0 END)                        AS down,
	       SUM(CASE WHEN status = 2 THEN 1 ELSE 0 END)                        AS pending,
	       SUM(CASE WHEN status = 3 THEN 1 ELSE 0 END)                        AS maintenance,
	       COUNT(*)                                                           AS total,
	       SUM(CASE WHEN status = 1 THEN COALESCE(latency_ms, 0) ELSE 0 END)  AS up_latency_sum,
	       SUM(CASE WHEN status = 1 AND latency_ms IS NOT NULL THEN 1 ELSE 0 END) AS up_latency_count,
	       COALESCE(MIN(latency_ms), 0)                                       AS latency_min,
	       COALESCE(MAX(latency_ms), 0)                                       AS latency_max,
	       ?                                                                  AS updated_at
	FROM heartbeats
	WHERE created_at >= ? AND created_at < ?
	GROUP BY monitor_id, bucket_at
	ON DUPLICATE KEY UPDATE
		up = VALUES(up),
		down = VALUES(down),
		pending = VALUES(pending),
		maintenance = VALUES(maintenance),
		total = VALUES(total),
		up_latency_sum = VALUES(up_latency_sum),
		up_latency_count = VALUES(up_latency_count),
		latency_min = VALUES(latency_min),
		latency_max = VALUES(latency_max),
		updated_at = VALUES(updated_at)`

// BackfillHeartbeatRollups reconciles the hourly rollups the window statistics
// read with the raw heartbeats they are derived from.
//
// It runs on every boot, after the schema exists and before the scheduler
// starts, and it is idempotent: it writes absolute values, never increments, so
// running it twice changes nothing and running it after a crash repairs the
// trailing hours.
//
// A fresh installation (empty rollup table) rolls up the whole 30 day horizon -
// the widest window History can report - because the heartbeats stored before
// this release have no rollup at all. Afterwards only the last few hours plus
// whatever gap separates the newest bucket from now are recomputed, which is a
// few thousand rows instead of a full scan.
func BackfillHeartbeatRollups(ctx context.Context, db *gorm.DB, log *slog.Logger) error {
	floor := time.Now().UTC().Truncate(time.Hour)
	horizon := floor.Add(-time.Duration(models.HeartbeatRollupHorizonHours) * time.Hour)

	var newest sql.NullTime
	if err := db.WithContext(ctx).Model(&models.HeartbeatRollup{}).
		Select("MAX(bucket_at)").Scan(&newest).Error; err != nil {
		return fmt.Errorf("reading the newest heartbeat rollup: %w", err)
	}

	// An empty table is the first boot after the upgrade: the heartbeats stored
	// before the release have no rollup at all, so the whole horizon is rebuilt.
	// Otherwise only the healing window, extended back to the newest bucket when
	// that is older than it (the node was down for a while), is recomputed.
	start := horizon
	if newest.Valid {
		start = floor.Add(-time.Duration(rollupHealHours) * time.Hour)
		if newest.Time.Before(start) {
			start = newest.Time
		}
		if start.Before(horizon) {
			start = horizon
		}
	}
	if !start.Before(floor) {
		return nil
	}

	result := db.WithContext(ctx).Exec(rollupBackfillQuery, time.Now().UTC(), start, floor)
	if result.Error != nil {
		return fmt.Errorf("rebuilding the heartbeat rollups: %w", result.Error)
	}
	log.Info("heartbeat rollups reconciled",
		"from", start, "to", floor, "rows", result.RowsAffected)
	return nil
}
