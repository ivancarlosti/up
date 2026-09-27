// Package database_test holds an OPT-IN integration check of the heartbeat
// rollups, the derived table the window statistics read (see
// internal/database/rollup.go and internal/services/stats.go).
//
// It is skipped unless UP_SCRATCH_DSN points at a THROWAWAY MariaDB/MySQL
// database: the test drops and recreates `heartbeats` and `heartbeat_rollups`,
// so pointing it at a real installation destroys data. The usual way to run it
// is a disposable container:
//
//	docker run --rm -d --name up-rollup-verify -p 127.0.0.1:3307:3306 \
//	  -e MARIADB_ROOT_PASSWORD=verify -e MARIADB_DATABASE=up \
//	  -e MARIADB_USER=up -e MARIADB_PASSWORD=verify mariadb:11
//	UP_SCRATCH_DSN='up:verify@tcp(127.0.0.1:3307)/up?charset=utf8mb4&parseTime=true&loc=UTC' \
//	  go test ./internal/database/ -run TestRollupLive -v
//	docker rm -f up-rollup-verify
//
// What it pins, none of which the pure unit tests of services can cover: the
// SQL GORM generates for the rollup table, the incremental upsert of every
// stored heartbeat, the boot backfill (DATE_FORMAT grouping, the VALUES()
// upsert, its idempotence and its 30 day horizon) and the promise that the
// rollup-based History returns exactly what the single raw aggregate used to
// return. It is written against a real engine because the first version of that
// code failed on an empty rollup table (MAX() returns NULL) and would have
// prevented the application from booting.
package database_test

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"math"
	"os"
	"testing"
	"time"

	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"github.com/ivancarlosti/up/internal/config"
	"github.com/ivancarlosti/up/internal/database"
	"github.com/ivancarlosti/up/internal/models"
	"github.com/ivancarlosti/up/internal/services"
)

// bucketAgg is the raw aggregate of one (monitor, hour), computed by the
// database. It is the reference both rollup writers must reproduce.
type bucketAgg struct {
	MonitorID      uint      `gorm:"column:monitor_id"`
	Bucket         time.Time `gorm:"column:bucket_at"`
	Up             int64     `gorm:"column:up"`
	Down           int64     `gorm:"column:down"`
	Pending        int64     `gorm:"column:pending"`
	Maintenance    int64     `gorm:"column:maintenance"`
	Total          int64     `gorm:"column:total"`
	UpLatencySum   int64     `gorm:"column:up_latency_sum"`
	UpLatencyCount int64     `gorm:"column:up_latency_count"`
	LatencyMin     int64     `gorm:"column:latency_min"`
	LatencyMax     int64     `gorm:"column:latency_max"`
}

const rawBucketAggQuery = `
	SELECT monitor_id,
	       CAST(DATE_FORMAT(created_at, '%Y-%m-%d %H:00:00') AS DATETIME) AS bucket_at,
	       SUM(CASE WHEN status = 1 THEN 1 ELSE 0 END) AS up,
	       SUM(CASE WHEN status = 0 THEN 1 ELSE 0 END) AS down,
	       SUM(CASE WHEN status = 2 THEN 1 ELSE 0 END) AS pending,
	       SUM(CASE WHEN status = 3 THEN 1 ELSE 0 END) AS maintenance,
	       COUNT(*) AS total,
	       SUM(CASE WHEN status = 1 THEN COALESCE(latency_ms, 0) ELSE 0 END) AS up_latency_sum,
	       SUM(CASE WHEN status = 1 AND latency_ms IS NOT NULL THEN 1 ELSE 0 END) AS up_latency_count,
	       COALESCE(MIN(latency_ms), 0) AS latency_min,
	       COALESCE(MAX(latency_ms), 0) AS latency_max
	FROM heartbeats
	GROUP BY monitor_id, bucket_at
	ORDER BY monitor_id, bucket_at`

func rawBucketAggregates(t *testing.T, db *gorm.DB) []bucketAgg {
	t.Helper()
	var rows []bucketAgg
	if err := db.Raw(rawBucketAggQuery).Scan(&rows).Error; err != nil {
		t.Fatalf("raw bucket aggregate: %v", err)
	}
	return rows
}

func storedRollups(t *testing.T, db *gorm.DB) []bucketAgg {
	t.Helper()
	var rows []bucketAgg
	query := `SELECT monitor_id, bucket_at, up, down, pending, maintenance, total,
	                 up_latency_sum, up_latency_count, latency_min, latency_max
	          FROM heartbeat_rollups ORDER BY monitor_id, bucket_at`
	if err := db.Raw(query).Scan(&rows).Error; err != nil {
		t.Fatalf("reading the rollups: %v", err)
	}
	return rows
}

func sameRollup(a, b bucketAgg) bool {
	return a.MonitorID == b.MonitorID &&
		a.Bucket.Equal(b.Bucket) &&
		a.Up == b.Up && a.Down == b.Down && a.Pending == b.Pending &&
		a.Maintenance == b.Maintenance && a.Total == b.Total &&
		a.UpLatencySum == b.UpLatencySum && a.UpLatencyCount == b.UpLatencyCount &&
		a.LatencyMin == b.LatencyMin && a.LatencyMax == b.LatencyMax
}

func compareRollups(t *testing.T, label string, got, want []bucketAgg) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s: %d rollup buckets, want %d", label, len(got), len(want))
	}
	compareRollupValues(t, label, got, want)
}

// compareRollupValues checks that every stored bucket reproduces the raw
// aggregate of its hour, without constraining how many buckets there are.
func compareRollupValues(t *testing.T, label string, got, want []bucketAgg) {
	t.Helper()
	byKey := make(map[string]bucketAgg, len(want))
	for _, row := range want {
		byKey[fmt.Sprintf("%d|%s", row.MonitorID, row.Bucket.UTC().Format(time.RFC3339))] = row
	}
	for _, row := range got {
		key := fmt.Sprintf("%d|%s", row.MonitorID, row.Bucket.UTC().Format(time.RFC3339))
		expected, ok := byKey[key]
		if !ok {
			t.Errorf("%s: unexpected bucket %s", label, key)
			continue
		}
		if !sameRollup(row, expected) {
			t.Errorf("%s: bucket %s = %+v, want %+v", label, key, row, expected)
		}
	}
}

// legacyRow is the payload of the aggregate History used to run.
type legacyRow struct {
	MonitorID   uint     `gorm:"column:monitor_id"`
	Up24h       int64    `gorm:"column:up_24h"`
	Total24h    int64    `gorm:"column:total_24h"`
	Down24h     int64    `gorm:"column:down_24h"`
	Pending24h  int64    `gorm:"column:pending_24h"`
	Up7d        int64    `gorm:"column:up_7d"`
	Total7d     int64    `gorm:"column:total_7d"`
	Up30d       int64    `gorm:"column:up_30d"`
	Total30d    int64    `gorm:"column:total_30d"`
	UpWindow    int64    `gorm:"column:up_window"`
	TotalWindow int64    `gorm:"column:total_window"`
	AvgMS24h    *float64 `gorm:"column:avg_ms_24h"`
	MinMS24h    *int64   `gorm:"column:min_ms_24h"`
	MaxMS24h    *int64   `gorm:"column:max_ms_24h"`
}

const legacyHistoryQuery = `
	SELECT monitor_id,
	       SUM(CASE WHEN status = 1 AND created_at >= ? THEN 1 ELSE 0 END)    AS up_24h,
	       SUM(CASE WHEN created_at >= ? THEN 1 ELSE 0 END)                   AS total_24h,
	       SUM(CASE WHEN status = 0 AND created_at >= ? THEN 1 ELSE 0 END)    AS down_24h,
	       SUM(CASE WHEN status = 2 AND created_at >= ? THEN 1 ELSE 0 END)    AS pending_24h,
	       SUM(CASE WHEN status = 1 AND created_at >= ? THEN 1 ELSE 0 END)    AS up_7d,
	       SUM(CASE WHEN created_at >= ? THEN 1 ELSE 0 END)                   AS total_7d,
	       SUM(CASE WHEN status = 1 AND created_at >= ? THEN 1 ELSE 0 END)    AS up_30d,
	       SUM(CASE WHEN created_at >= ? THEN 1 ELSE 0 END)                   AS total_30d,
	       SUM(CASE WHEN status = 1 AND created_at >= ? THEN 1 ELSE 0 END)    AS up_window,
	       SUM(CASE WHEN created_at >= ? THEN 1 ELSE 0 END)                   AS total_window,
	       AVG(CASE WHEN status = 1 AND created_at >= ? THEN latency_ms END)  AS avg_ms_24h,
	       MIN(CASE WHEN created_at >= ? THEN latency_ms END)                 AS min_ms_24h,
	       MAX(CASE WHEN created_at >= ? THEN latency_ms END)                 AS max_ms_24h
	FROM heartbeats
	WHERE created_at >= ? AND monitor_id IN ?
	GROUP BY monitor_id`

func sameFloat(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

// TestScratchRollupLive validates the SQL of Phase 2 against a throwaway
// MariaDB: the incremental upsert, the boot backfill and the rollup-based
// History must all agree with the raw heartbeats and with the aggregate History
// used to run.
func TestRollupLive(t *testing.T) {
	dsn := os.Getenv("UP_SCRATCH_DSN")
	if dsn == "" {
		t.Skip("UP_SCRATCH_DSN is not set")
	}
	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{
		NowFunc:                func() time.Time { return time.Now().UTC() },
		SkipDefaultTransaction: true,
		Logger:                 gormlogger.Default.LogMode(gormlogger.Silent),
	})
	if err != nil {
		t.Fatalf("connecting: %v", err)
	}
	for _, model := range []any{&models.Heartbeat{}, &models.HeartbeatRollup{}} {
		if err := db.Migrator().DropTable(model); err != nil {
			t.Fatalf("dropping %T: %v", model, err)
		}
	}
	if err := db.AutoMigrate(&models.Heartbeat{}, &models.HeartbeatRollup{}); err != nil {
		t.Fatalf("migrating: %v", err)
	}
	if rows, err := db.Raw("SHOW CREATE TABLE heartbeat_rollups").Rows(); err == nil {
		if rows.Next() {
			var name, ddl string
			if err := rows.Scan(&name, &ddl); err == nil {
				t.Logf("heartbeat_rollups DDL:\n%s", ddl)
			}
		}
		rows.Close()
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx := context.Background()
	heartbeats := services.NewHeartbeatService(db, &config.Config{NodeID: "verify"}, logger)
	stats := services.NewStatsService(db)

	base := time.Now().UTC()
	monitors := []uint{101, 202}
	statuses := []models.HeartbeatStatus{
		models.StatusUp, models.StatusUp, models.StatusDown,
		models.StatusUp, models.StatusPending, models.StatusMaintenance,
	}
	latency := int64(1)
	record := func(monitorID uint, at time.Time, status models.HeartbeatStatus) {
		latency = latency*37%1500 + 1
		if err := heartbeats.Record(ctx, &models.Heartbeat{
			MonitorID: monitorID, Status: status, LatencyMS: latency, CreatedAt: at,
		}); err != nil {
			t.Fatalf("recording heartbeat: %v", err)
		}
	}
	for _, monitorID := range monitors {
		// 31 days of half hourly checks (the 30 day window has to clip them),
		// then a burst inside the hour in progress.
		for k := 0; k < 1500; k++ {
			at := base.Add(-time.Duration(k) * 30 * time.Minute).Add(-13 * time.Second)
			record(monitorID, at, statuses[k%len(statuses)])
		}
		for k := 0; k < 40; k++ {
			record(monitorID, base.Add(-time.Duration(k)*30*time.Second), statuses[(k+1)%len(statuses)])
		}
	}

	want := rawBucketAggregates(t, db)
	compareRollups(t, "incremental upsert", storedRollups(t, db), want)

	if err := db.Exec("DELETE FROM heartbeat_rollups").Error; err != nil {
		t.Fatalf("clearing the rollups: %v", err)
	}
	if err := database.BackfillHeartbeatRollups(ctx, db, logger); err != nil {
		t.Fatalf("boot backfill: %v", err)
	}
	compareRollupValues(t, "boot backfill", storedRollups(t, db), want)
	// The backfill only covers the horizon History can read, while the fixture
	// also holds checks older than that: one bucket per monitor per hour.
	if got, expected := len(storedRollups(t, db)), 2*models.HeartbeatRollupHorizonHours; got != expected {
		t.Errorf("boot backfill stored %d buckets, want %d (the 30 day horizon)", got, expected)
	}

	// A second run must change nothing (idempotence).
	before := storedRollups(t, db)
	if err := database.BackfillHeartbeatRollups(ctx, db, logger); err != nil {
		t.Fatalf("second backfill: %v", err)
	}
	compareRollups(t, "second backfill", storedRollups(t, db), before)

	for _, windowHours := range []int{24, 168, 336, 720} {
		now := time.Now().UTC()
		history, err := stats.History(ctx, monitors, windowHours)
		if err != nil {
			t.Fatalf("history(%d h): %v", windowHours, err)
		}
		day := now.Add(-24 * time.Hour)
		week := now.Add(-7 * 24 * time.Hour)
		month := now.Add(-30 * 24 * time.Hour)
		window := now.Add(-time.Duration(windowHours) * time.Hour)

		var rows []legacyRow
		if err := db.Raw(legacyHistoryQuery,
			day, day, day, day,
			week, week,
			month, month,
			window, window,
			day, day, day,
			month, monitors,
		).Scan(&rows).Error; err != nil {
			t.Fatalf("legacy aggregate: %v", err)
		}
		if len(rows) != len(monitors) {
			t.Fatalf("legacy aggregate returned %d rows, want %d", len(rows), len(monitors))
		}
		for _, row := range rows {
			got, ok := history[row.MonitorID]
			if !ok {
				t.Fatalf("%d h: monitor %d missing from the history", windowHours, row.MonitorID)
			}
			stat := got.Window
			if stat.Up != int(row.UpWindow) || stat.Total != int(row.TotalWindow) ||
				stat.Down != int(row.Down24h) || stat.Pending != int(row.Pending24h) {
				t.Errorf("%d h monitor %d window = %+v, want up=%d total=%d down=%d pending=%d",
					windowHours, row.MonitorID, stat, row.UpWindow, row.TotalWindow, row.Down24h, row.Pending24h)
			}
			for label, pair := range map[string][2]float64{
				"uptime":    {stat.Uptime, percentage(row.UpWindow, row.TotalWindow)},
				"uptime24h": {got.Uptime24h, percentage(row.Up24h, row.Total24h)},
				"uptime7d":  {got.Uptime7d, percentage(row.Up7d, row.Total7d)},
				"uptime30d": {got.Uptime30d, percentage(row.Up30d, row.Total30d)},
			} {
				if !sameFloat(pair[0], pair[1]) {
					t.Errorf("%d h monitor %d %s = %v, want %v", windowHours, row.MonitorID, label, pair[0], pair[1])
				}
			}
			var wantAvg float64
			if row.AvgMS24h != nil {
				wantAvg = *row.AvgMS24h
			}
			if !sameFloat(stat.AvgMS, wantAvg) {
				t.Errorf("%d h monitor %d avg = %v, want %v", windowHours, row.MonitorID, stat.AvgMS, wantAvg)
			}
			var wantMin, wantMax int64
			if row.MinMS24h != nil {
				wantMin = *row.MinMS24h
			}
			if row.MaxMS24h != nil {
				wantMax = *row.MaxMS24h
			}
			if stat.MinMS != wantMin || stat.MaxMS != wantMax {
				t.Errorf("%d h monitor %d min/max = %d/%d, want %d/%d",
					windowHours, row.MonitorID, stat.MinMS, stat.MaxMS, wantMin, wantMax)
			}
			t.Logf("%d h monitor %d: up=%d total=%d uptime=%.6f avg=%.4f min=%d max=%d (30d up=%d/%d)",
				windowHours, row.MonitorID, stat.Up, stat.Total, stat.Uptime, stat.AvgMS, stat.MinMS, stat.MaxMS,
				row.Up30d, row.Total30d)
		}
	}
}

// percentage mirrors the unexported helper of services.
func percentage(part, total int64) float64 {
	if total <= 0 {
		return 0
	}
	return float64(part) / float64(total) * 100
}
