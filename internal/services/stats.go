package services

import (
	"context"
	"fmt"
	"time"

	"gorm.io/gorm"

	"github.com/ivancarlosti/up/internal/models"
)

// StatsService computes uptime percentages, latency figures and heartbeat
// series. It is the single implementation used by the dashboard, the monitor
// detail page, the public status pages and the public API, so the numbers are
// always consistent.
type StatsService struct {
	db *gorm.DB
}

// NewStatsService builds the statistics service.
func NewStatsService(db *gorm.DB) *StatsService { return &StatsService{db: db} }

// windowRow is the raw aggregate row of the Windows query.
type windowRow struct {
	MonitorID  uint     `gorm:"column:monitor_id"`
	Up24h      int64    `gorm:"column:up_24h"`
	Total24h   int64    `gorm:"column:total_24h"`
	Down24h    int64    `gorm:"column:down_24h"`
	Pending24h int64    `gorm:"column:pending_24h"`
	Up7d       int64    `gorm:"column:up_7d"`
	Total7d    int64    `gorm:"column:total_7d"`
	Up30d      int64    `gorm:"column:up_30d"`
	Total30d   int64    `gorm:"column:total_30d"`
	AvgMS24h   *float64 `gorm:"column:avg_ms_24h"`
	MinMS24h   *int64   `gorm:"column:min_ms_24h"`
	MaxMS24h   *int64   `gorm:"column:max_ms_24h"`
}

// Windows returns the 24h/7d/30d statistics of a set of monitors using a
// single grouped query.
func (s *StatsService) Windows(ctx context.Context, monitorIDs []uint) (map[uint]*models.UptimeStats, error) {
	out := map[uint]*models.UptimeStats{}
	if len(monitorIDs) == 0 {
		return out, nil
	}
	now := time.Now().UTC()
	query := `
		SELECT monitor_id,
		       SUM(CASE WHEN status = 1 AND created_at >= ? THEN 1 ELSE 0 END)    AS up_24h,
		       SUM(CASE WHEN created_at >= ? THEN 1 ELSE 0 END)                   AS total_24h,
		       SUM(CASE WHEN status = 0 AND created_at >= ? THEN 1 ELSE 0 END)    AS down_24h,
		       SUM(CASE WHEN status = 2 AND created_at >= ? THEN 1 ELSE 0 END)    AS pending_24h,
		       SUM(CASE WHEN status = 1 AND created_at >= ? THEN 1 ELSE 0 END)    AS up_7d,
		       SUM(CASE WHEN created_at >= ? THEN 1 ELSE 0 END)                   AS total_7d,
		       SUM(CASE WHEN status = 1 AND created_at >= ? THEN 1 ELSE 0 END)    AS up_30d,
		       SUM(CASE WHEN created_at >= ? THEN 1 ELSE 0 END)                   AS total_30d,
		       AVG(CASE WHEN status = 1 AND created_at >= ? THEN latency_ms END)  AS avg_ms_24h,
		       MIN(CASE WHEN created_at >= ? THEN latency_ms END)                 AS min_ms_24h,
		       MAX(CASE WHEN created_at >= ? THEN latency_ms END)                 AS max_ms_24h
		FROM heartbeats
		WHERE created_at >= ? AND monitor_id IN ?
		GROUP BY monitor_id`

	day := now.Add(-24 * time.Hour)
	week := now.Add(-7 * 24 * time.Hour)
	month := now.Add(-30 * 24 * time.Hour)

	var rows []windowRow
	if err := s.db.WithContext(ctx).Raw(query,
		day, day, day, day,
		week, week,
		month, month,
		day, day, day,
		month, monitorIDs,
	).Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("computing window statistics: %w", err)
	}

	for _, row := range rows {
		stats := &models.UptimeStats{
			MonitorID: row.MonitorID,
			Hours:     24,
			Up:        int(row.Up24h),
			Down:      int(row.Down24h),
			Pending:   int(row.Pending24h),
			Total:     int(row.Total24h),
		}
		stats.Uptime = percentage(row.Up24h, row.Total24h)
		if row.AvgMS24h != nil {
			stats.AvgMS = *row.AvgMS24h
		}
		if row.MinMS24h != nil {
			stats.MinMS = *row.MinMS24h
		}
		if row.MaxMS24h != nil {
			stats.MaxMS = *row.MaxMS24h
		}
		out[row.MonitorID] = stats
	}
	return out, nil
}

// Window returns the statistics of a single monitor for the last N hours.
func (s *StatsService) Window(ctx context.Context, monitorID uint, hours int) (*models.UptimeStats, error) {
	since := time.Now().UTC().Add(-time.Duration(hours) * time.Hour)

	type aggRow struct {
		Up      int64    `gorm:"column:up"`
		Down    int64    `gorm:"column:down"`
		Pending int64    `gorm:"column:pending"`
		Total   int64    `gorm:"column:total"`
		AvgMS   *float64 `gorm:"column:avg_ms"`
		MinMS   *int64   `gorm:"column:min_ms"`
		MaxMS   *int64   `gorm:"column:max_ms"`
	}
	query := `
		SELECT SUM(CASE WHEN status = 1 THEN 1 ELSE 0 END)      AS up,
		       SUM(CASE WHEN status = 0 THEN 1 ELSE 0 END)      AS down,
		       SUM(CASE WHEN status = 2 THEN 1 ELSE 0 END)      AS pending,
		       COUNT(*)                                         AS total,
		       AVG(CASE WHEN status = 1 THEN latency_ms END)    AS avg_ms,
		       MIN(latency_ms)                                  AS min_ms,
		       MAX(latency_ms)                                  AS max_ms
		FROM heartbeats
		WHERE monitor_id = ? AND created_at >= ?`

	var row aggRow
	if err := s.db.WithContext(ctx).Raw(query, monitorID, since).Scan(&row).Error; err != nil {
		return nil, fmt.Errorf("computing statistics: %w", err)
	}

	stats := &models.UptimeStats{
		MonitorID: monitorID,
		Hours:     hours,
		Up:        int(row.Up),
		Down:      int(row.Down),
		Pending:   int(row.Pending),
		Total:     int(row.Total),
	}
	stats.Uptime = percentage(row.Up, row.Total)
	if row.AvgMS != nil {
		stats.AvgMS = *row.AvgMS
	}
	if row.MinMS != nil {
		stats.MinMS = *row.MinMS
	}
	if row.MaxMS != nil {
		stats.MaxMS = *row.MaxMS
	}
	stats.P95MS = s.percentile95(ctx, monitorID, since)
	return stats, nil
}

func percentage(part, total int64) float64 {
	if total <= 0 {
		return 0
	}
	return float64(part) / float64(total) * 100
}
