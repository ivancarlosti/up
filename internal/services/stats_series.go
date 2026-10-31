package services

import (
	"context"
	"fmt"
	"time"

	"github.com/ivancarlosti/up/internal/models"
)

// percentile95 samples the up heartbeats of the window and computes the 95th
// percentile in Go: MySQL and MariaDB have no portable percentile function.
func (s *StatsService) percentile95(ctx context.Context, monitorID uint, since time.Time) int64 {
	const sampleSize = 500
	since = queryTime(since)
	var latencies []int64
	if err := s.db.WithContext(ctx).
		Model(&models.Heartbeat{}).
		Where("monitor_id = ? AND status = ? AND created_at >= ?", monitorID, models.StatusUp, since).
		Order("created_at DESC").
		Limit(sampleSize).
		Pluck("latency_ms", &latencies).Error; err != nil || len(latencies) == 0 {
		return 0
	}
	// Insertion sort is fine for the small sample window used here.
	for i := 1; i < len(latencies); i++ {
		for j := i; j > 0 && latencies[j] < latencies[j-1]; j-- {
			latencies[j], latencies[j-1] = latencies[j-1], latencies[j]
		}
	}
	idx := int(float64(len(latencies)) * 0.95)
	if idx >= len(latencies) {
		idx = len(latencies) - 1
	}
	return latencies[idx]
}

// Series returns the most recent heartbeats of a monitor, oldest first, ready
// to be rendered as the status bars of the dashboard.
func (s *StatsService) Series(ctx context.Context, monitorID uint, limit int) ([]models.HeartbeatSummary, error) {
	if limit <= 0 || limit > 1000 {
		limit = 60
	}
	var heartbeats []models.Heartbeat
	if err := s.db.WithContext(ctx).
		Where("monitor_id = ?", monitorID).
		Order("created_at DESC").
		Limit(limit).
		Find(&heartbeats).Error; err != nil {
		return nil, err
	}
	out := make([]models.HeartbeatSummary, 0, len(heartbeats))
	for i := len(heartbeats) - 1; i >= 0; i-- {
		hb := heartbeats[i]
		out = append(out, models.HeartbeatSummary{
			Status:    hb.Status,
			LatencyMS: hb.LatencyMS,
			CreatedAt: hb.CreatedAt,
			NodeID:    hb.NodeID,
		})
	}
	return out, nil
}

// LatestPerNode returns the newest heartbeat of every (monitor, node) pair
// written since the given instant. It feeds the cluster voting logic and the
// per-node breakdown in the UI.
//
// The bound is what keeps the statement cheap. `heartbeats` is indexed on
// (monitor_id, created_at), so a grouped `MAX(id)` cannot seek the newest row of
// each group: without a window it walks the whole retained history (180 days by
// default) to keep one row per node. Only a heartbeat inside the vote window can
// produce a valid verdict (see voteWindow), so the caller passes the widest
// window of the batch and the scan covers the last few minutes instead.
func (s *StatsService) LatestPerNode(ctx context.Context, monitorIDs []uint, since time.Time) ([]models.Heartbeat, error) {
	if len(monitorIDs) == 0 {
		return nil, nil
	}
	since = queryTime(since)
	var heartbeats []models.Heartbeat
	query := `
		SELECT h.* FROM heartbeats h
		JOIN (
			SELECT monitor_id, node_id, MAX(id) AS max_id
			FROM heartbeats
			WHERE monitor_id IN ? AND created_at >= ?
			GROUP BY monitor_id, node_id
		) latest ON latest.max_id = h.id
		ORDER BY h.monitor_id, h.node_id`
	if err := s.db.WithContext(ctx).Raw(query, monitorIDs, since).Scan(&heartbeats).Error; err != nil {
		return nil, fmt.Errorf("loading per node heartbeats: %w", err)
	}
	return heartbeats, nil
}

// List returns the heartbeats of a monitor inside a time window (newest first).
func (s *StatsService) List(ctx context.Context, monitorID uint, hours, limit int) ([]models.Heartbeat, error) {
	if limit <= 0 || limit > 5000 {
		limit = 500
	}
	if hours <= 0 {
		hours = 24
	}
	since := queryTime(time.Now().UTC().Add(-time.Duration(hours) * time.Hour))
	var heartbeats []models.Heartbeat
	err := s.db.WithContext(ctx).
		Where("monitor_id = ? AND created_at >= ?", monitorID, since).
		Order("created_at DESC").
		Limit(limit).
		Find(&heartbeats).Error
	return heartbeats, err
}

// purgeChunk is how many rows one DELETE of the retention job removes.
//
// The purge used to be a single DELETE over everything the policy had to drop.
// On an installation that had never purged - the first boot after an upgrade, or
// a deployment whose retention had been disabled for a while - that is hundreds
// of millions of rows in one statement: minutes of undo log growth, one very long
// lock, and the probes of every monitor queuing behind it. Deleting the same rows
// in short statements keeps the lock of each one bounded and lets the probes
// write. Note that GORM only builds the LIMIT of a DELETE when the dialector
// declares it (the MySQL one does, see driver/mysql.DeleteClauses), so the
// statement really is `DELETE ... LIMIT 5000`.
const purgeChunk = 5000

// Purge deletes heartbeats older than the given date (retention job) together
// with the hourly rollups of the same period: History reads the rollups, so a
// rollup that outlives the heartbeats it was computed from would keep reporting
// a period the retention policy already dropped.
//
// Both tables are pruned in chunks of purgeChunk rows. The function returns the
// number of heartbeats it deleted, and stops early when the context is cancelled,
// so a shutdown never waits for a purge that has hours of work left (the next one
// resumes where this one stopped).
func (s *StatsService) Purge(ctx context.Context, before time.Time) (int64, error) {
	before = queryTime(before)
	// Rollups first: a heartbeat deleted while its bucket is still readable would
	// leave the rollup reporting a period the retention policy already dropped.
	if _, err := s.deleteInChunks(ctx, &models.HeartbeatRollup{}, "bucket_at < ?", before); err != nil {
		return 0, fmt.Errorf("purging the heartbeat rollups: %w", err)
	}
	deleted, err := s.deleteInChunks(ctx, &models.Heartbeat{}, "created_at < ?", before)
	if err != nil {
		return deleted, fmt.Errorf("purging the heartbeats: %w", err)
	}
	return deleted, nil
}

// deleteInChunks removes the rows matching a condition purgeChunk at a time and
// returns how many it deleted.
func (s *StatsService) deleteInChunks(ctx context.Context, model any, condition string, args ...any) (int64, error) {
	var deleted int64
	for {
		if err := ctx.Err(); err != nil {
			return deleted, err
		}
		result := s.db.WithContext(ctx).Where(condition, args...).Limit(purgeChunk).Delete(model)
		if result.Error != nil {
			return deleted, result.Error
		}
		deleted += result.RowsAffected
		if result.RowsAffected < purgeChunk {
			return deleted, nil
		}
	}
}

// HeartbeatWindowStats is the operator facing summary of the history table used
// by Admin > Settings: what is stored right now.
type HeartbeatWindowStats struct {
	Total  int64      `json:"total"`
	Oldest *time.Time `json:"oldest"`
	Newest *time.Time `json:"newest"`
}

// heartbeatExactCountLimit is the largest history whose size is counted exactly.
//
// There is no index that answers COUNT(*) without walking it: the server reads
// every entry of the narrowest index of the table, from the first row to the last.
// At the shipped retention and 1 000 monitors that is hundreds of millions of
// entries - tens of seconds on the deployment that reported the problem. Above
// this many rows the card reports InnoDB's own estimate instead
// (information_schema.TABLES.TABLE_ROWS, refreshed with the index statistics), and
// below it the exact count, which is instant.
const heartbeatExactCountLimit = 2_000_000

// HeartbeatStats reads the size and the age range of the heartbeat history, with
// one aggregate query over the indexes.
//
// The age range is exact: MIN and MAX of an indexed column are an index seek, so
// that part of the query stays instant at any size (it is the reason
// idx_hb_created_at exists, see models.Heartbeat). The size is exact while the
// table is small and an estimate above heartbeatExactCountLimit - see the
// constant - because the exact number is what made Admin > Settings one of the
// slowest pages of a large installation.
func (s *StatsService) HeartbeatStats(ctx context.Context) (HeartbeatWindowStats, error) {
	row := struct {
		Oldest *time.Time `gorm:"column:oldest"`
		Newest *time.Time `gorm:"column:newest"`
	}{}
	query := "SELECT MIN(created_at) AS oldest, MAX(created_at) AS newest FROM heartbeats"
	if err := s.db.WithContext(ctx).Raw(query).Scan(&row).Error; err != nil {
		return HeartbeatWindowStats{}, fmt.Errorf("summarising the heartbeat history: %w", err)
	}
	total, err := s.heartbeatCount(ctx)
	if err != nil {
		return HeartbeatWindowStats{}, err
	}
	return HeartbeatWindowStats{Total: total, Oldest: row.Oldest, Newest: row.Newest}, nil
}

// heartbeatCount counts the stored heartbeats, exactly while that is cheap and
// from the table statistics when it is not.
func (s *StatsService) heartbeatCount(ctx context.Context) (int64, error) {
	estimate := struct {
		Rows int64 `gorm:"column:table_rows"`
	}{}
	// TABLE_SCHEMA = DATABASE() keeps the lookup on the schema the DSN selected.
	query := `
		SELECT COALESCE(TABLE_ROWS, 0) AS table_rows
		FROM information_schema.TABLES
		WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'heartbeats'`
	if err := s.db.WithContext(ctx).Raw(query).Scan(&estimate).Error; err != nil {
		return 0, fmt.Errorf("reading the size of the heartbeat history: %w", err)
	}
	if estimate.Rows >= heartbeatExactCountLimit {
		return estimate.Rows, nil
	}
	var count int64
	if err := s.db.WithContext(ctx).Model(&models.Heartbeat{}).Count(&count).Error; err != nil {
		return 0, fmt.Errorf("counting the heartbeat history: %w", err)
	}
	return count, nil
}

// CountOlderThan counts the heartbeats a purge would delete: it is the preview
// shown before the operator confirms the destructive action.
func (s *StatsService) CountOlderThan(ctx context.Context, before time.Time) (int64, error) {
	var count int64
	if err := s.db.WithContext(ctx).Model(&models.Heartbeat{}).
		Where("created_at < ?", before).
		Count(&count).Error; err != nil {
		return 0, fmt.Errorf("counting the heartbeat history: %w", err)
	}
	return count, nil
}

// HeartbeatBarSlots is the widest heartbeat bar of the monitors table: the column
// draws one bar per whole hour of the selected window, so a 24 h window gets one
// per hour and a 30 d window the full 30 (plus, in every case, the hour in
// progress). Capping the window at this many whole hours is what keeps the bars
// readable on a wide window - one bar per slot instead of one per day - and the
// query bounded by monitors x slots rows.
const HeartbeatBarSlots = 30

// heartbeatBucketRow is one (monitor, slot) aggregate of RecentBars.
type heartbeatBucketRow struct {
	MonitorID    uint  `gorm:"column:monitor_id"`
	Bucket       int   `gorm:"column:bucket"`
	Downs        int64 `gorm:"column:downs"`
	Pendings     int64 `gorm:"column:pendings"`
	Maintenances int64 `gorm:"column:maintenances"`
	Total        int64 `gorm:"column:total"`
}

// status reduces a slot to the status that deserves attention: a single failed
// check marks the slot as down even when the slot also saw successes.
func (r heartbeatBucketRow) status() string {
	switch {
	case r.Downs > 0:
		return models.StatusDown.String()
	case r.Pendings > 0:
		return models.StatusPending.String()
	case r.Maintenances > 0:
		return models.StatusMaintenance.String()
	case r.Total > 0:
		return models.StatusUp.String()
	}
	return ""
}

// heartbeatSlotPlan is the layout of one RecentBars call: the whole hours each
// bar covers.
type heartbeatSlotPlan struct {
	// start is the first hour of the first bar.
	start time.Time
	// slotHours is how many whole hours one bar covers.
	slotHours int
	// bars is the number of bars: the whole hour slots of the window plus the
	// bar of the hour in progress, which is always the last one.
	bars int
}

// planHeartbeatSlots lays the bars out on whole hours, oldest first, one bar per
// slot of the window plus the hour in progress.
//
// A bar used to cover windowHours/slots (48 minutes of a 24 h window), a period
// no rollup can answer: an hourly bucket then falls in the middle of two bars, so
// the bars had to aggregate the raw heartbeats of the whole window - 2 000 slots
// of checks for a 24 h window and 60 times that for a 30 d one, which is what
// made the column the slowest query of a dashboard load. Aligning every bar on
// the hour keeps its meaning (one status per slot of the window) and lets the
// rollups answer it: they hold the same reduction of the same heartbeats.
func planHeartbeatSlots(now time.Time, windowHours, slots int) heartbeatSlotPlan {
	if slots <= 0 || slots > 120 {
		slots = HeartbeatBarSlots
	}
	windowHours = models.NormalizeUptimeWindowHours(windowHours)
	// The bar is the smallest whole number of hours that keeps the column within
	// slots bars. Every window the application accepts is a multiple of 24 h and
	// the default width is 30 bars, so the division is exact on the shipped
	// values: a 24 h window gets one bar per hour, 7 d and 14 d six and twelve,
	// 30 d one per day.
	slotHours := (windowHours + slots - 1) / slots
	if slotHours < 1 {
		slotHours = 1
	}
	whole := windowHours / slotHours
	if whole < 1 {
		whole = 1
	}
	endHour := now.UTC().Truncate(time.Hour)
	return heartbeatSlotPlan{
		start:     endHour.Add(-time.Duration(whole*slotHours) * time.Hour),
		slotHours: slotHours,
		bars:      whole + 1,
	}
}

// RecentBars returns the compact, bucketed heartbeat history of a set of
// monitors: one status slot per whole hour of the window plus the hour in
// progress, oldest first, empty when the slot saw no heartbeat (a paused monitor,
// or a gap in the history).
//
// It is ONE grouped query over the hourly rollups, so the cost is bounded by
// monitors x slots rows and a dashboard load never reads the heartbeat history
// itself. That is what makes the column affordable on every load; the value is a
// snapshot, never a live feed.
func (s *StatsService) RecentBars(ctx context.Context, monitorIDs []uint, windowHours, slots int) (map[uint][]string, error) {
	out := map[uint][]string{}
	if len(monitorIDs) == 0 {
		return out, nil
	}
	plan := planHeartbeatSlots(time.Now(), windowHours, slots)

	// The slot index is the whole hours between the first bar and the bucket
	// divided by the width of a bar; every stored timestamp is UTC and
	// TIMESTAMPDIFF compares two DATETIME values without converting them through
	// the session timezone. The bucket of the hour in progress lands on the last
	// bar exactly (start + slotHours x whole hours is that hour).
	query := `
		SELECT monitor_id,
		       FLOOR(TIMESTAMPDIFF(HOUR, ?, bucket_at) / ?) AS bucket,
		       SUM(down)                                    AS downs,
		       SUM(pending)                                 AS pendings,
		       SUM(maintenance)                             AS maintenances,
		       SUM(total)                                   AS total
		FROM heartbeat_rollups
		WHERE bucket_at >= ? AND monitor_id IN ?
		GROUP BY monitor_id, bucket`

	var rows []heartbeatBucketRow
	if err := s.db.WithContext(ctx).Raw(query, plan.start, plan.slotHours, plan.start, monitorIDs).Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("computing the heartbeat bars: %w", err)
	}
	for _, row := range rows {
		if row.Bucket < 0 || row.Bucket >= plan.bars {
			continue
		}
		bars, ok := out[row.MonitorID]
		if !ok {
			bars = make([]string, plan.bars)
			out[row.MonitorID] = bars
		}
		bars[row.Bucket] = row.status()
	}
	return out, nil
}
