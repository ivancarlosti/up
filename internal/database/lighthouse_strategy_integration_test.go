// This file holds an OPT-IN integration check of the primary key widening of
// monitor_lighthouses (see internal/database/lighthouse_strategy.go).
//
// It is skipped unless UP_SCRATCH_DSN points at a THROWAWAY MariaDB/MySQL
// database: the test drops and recreates `monitor_lighthouses`, so pointing it at
// a real installation destroys data. The usual way to run it is a disposable
// container:
//
//	docker run --rm -d --name up-lh-verify -p 127.0.0.1:3307:3306 \
//	  -e MARIADB_ROOT_PASSWORD=verify -e MARIADB_DATABASE=up \
//	  -e MARIADB_USER=up -e MARIADB_PASSWORD=verify mariadb:11
//	UP_SCRATCH_DSN='up:verify@tcp(127.0.0.1:3307)/up?charset=utf8mb4&parseTime=true&loc=UTC' \
//	  go test ./internal/database/ -run TestLighthouseStrategyLive -v
//	docker rm -f up-lh-verify
//
// What it pins, none of which a unit test can: that a database written by an
// older release (one row per monitor, the monitor id as the whole primary key)
// really ends up with one row per (monitor, strategy), that the rows it already
// held survive the ALTER, and that the upsert the job uses then writes one row
// per run and updates the right one.
package database_test

import (
	"context"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"github.com/ivancarlosti/up/internal/database"
	"github.com/ivancarlosti/up/internal/models"
	"github.com/ivancarlosti/up/internal/services"
)

// legacyLighthouseTable is the table as an older release created it: the monitor
// id is the whole identity of the row and `strategy` is a plain column.
const legacyLighthouseTable = `
	CREATE TABLE monitor_lighthouses (
	  monitor_id BIGINT UNSIGNED NOT NULL,
	  strategy VARCHAR(20),
	  status VARCHAR(20) NOT NULL DEFAULT 'ok',
	  message VARCHAR(500),
	  final_url VARCHAR(500),
	  ran_at DATETIME(3) NULL,
	  fetch_time DATETIME(3) NULL,
	  ran_by_node VARCHAR(64),
	  performance BIGINT,
	  accessibility BIGINT,
	  best_practices BIGINT,
	  seo BIGINT,
	  first_contentful_paint_ms DOUBLE,
	  largest_contentful_paint_ms DOUBLE,
	  total_blocking_time_ms DOUBLE,
	  cumulative_layout_shift DOUBLE,
	  speed_index_ms DOUBLE,
	  PRIMARY KEY (monitor_id)
	)`

// TestLighthouseStrategyLive checks the widening against a real MariaDB.
func TestLighthouseStrategyLive(t *testing.T) {
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
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	// The state of an installation upgrading from the one-row schema.
	if err := db.Migrator().DropTable(&models.MonitorLighthouse{}, &models.LighthouseRun{}); err != nil {
		t.Fatalf("dropping the web performance tables: %v", err)
	}
	if err := db.Exec(legacyLighthouseTable).Error; err != nil {
		t.Fatalf("creating the legacy table: %v", err)
	}
	if got := primaryKeyOf(t, db); len(got) != 1 || got[0] != "monitor_id" {
		t.Fatalf("legacy primary key = %v", got)
	}
	// One monitor measured mobile before the strategy was written down (an empty
	// value), another one desktop.
	seed := `INSERT INTO monitor_lighthouses (monitor_id, strategy, status, ran_at)
	         VALUES (1, 'desktop', 'ok', NOW()), (2, '', 'ok', NOW())`
	if err := db.Exec(seed).Error; err != nil {
		t.Fatalf("seeding the legacy rows: %v", err)
	}

	if err := database.Migrate(db, log); err != nil {
		t.Fatalf("migrating: %v", err)
	}

	if got := primaryKeyOf(t, db); len(got) != 2 || got[0] != "monitor_id" || got[1] != "strategy" {
		t.Fatalf("primary key after the migration = %v", got)
	}
	// The rows an installation already had must survive: the ALTER rewrites no
	// data, and the empty strategy was filled with the documented default (the
	// column is part of a key now, so it cannot stay NULL).
	var rows []models.MonitorLighthouse
	if err := db.Order("monitor_id").Find(&rows).Error; err != nil {
		t.Fatalf("reading the migrated rows: %v", err)
	}
	if len(rows) != 2 || rows[0].Strategy != models.LighthouseStrategyDesktop ||
		rows[1].Strategy != models.LighthouseStrategyMobile {
		t.Fatalf("migrated rows = %+v", rows)
	}

	// A second boot must find the key already widened and do nothing (the pass is
	// idempotent: it runs on every start).
	if err := database.Migrate(db, log); err != nil {
		t.Fatalf("migrating twice: %v", err)
	}
	if got := primaryKeyOf(t, db); len(got) != 2 {
		t.Fatalf("primary key after the second boot = %v", got)
	}
	if got := countRows(t, db, 1); got != 1 {
		t.Fatalf("monitor 1 holds %d rows after the second boot, want 1", got)
	}

	checkLighthouseUpsert(t, db, log)
}

// checkLighthouseUpsert drives the real storage path: the upsert of the job must
// write one row per run of the same monitor, update the row of the run it
// re-measures (never append a second one) and leave the other run alone.
func checkLighthouseUpsert(t *testing.T, db *gorm.DB, log *slog.Logger) {
	t.Helper()
	service := services.NewLighthouseService(db, nil, log)
	ctx := context.Background()
	score := func(monitor uint, strategy string, performance int) {
		t.Helper()
		result := &models.LighthouseResult{
			Status:   models.LighthouseStatusOK,
			Strategy: strategy,
			RanAt:    time.Now().UTC(),
			LighthouseMeasurements: models.LighthouseMeasurements{
				Performance: &performance,
			},
		}
		if err := service.Record(ctx, monitor, result); err != nil {
			t.Fatalf("recording %s of monitor %d: %v", strategy, monitor, err)
		}
	}
	score(1, models.LighthouseStrategyDesktop, 70)
	score(1, models.LighthouseStrategyMobile, 40)
	score(1, models.LighthouseStrategyMobile, 42)
	if got := countRows(t, db, 1); got != 2 {
		t.Fatalf("monitor 1 holds %d rows, want 2 (one per run)", got)
	}
	// The page header counts MONITORS, not rows: two runs of one monitor are one
	// monitor measured (COUNT(DISTINCT monitor_id)), and every run is a point of
	// the history.
	stats, err := service.Stats(ctx)
	if err != nil {
		t.Fatalf("reading the stats: %v", err)
	}
	if stats.Measuring != 2 || stats.Runs != 3 {
		t.Fatalf("stats = %+v, want 2 monitors measured and 3 runs", stats)
	}
	stored, err := service.Get(ctx, 1)
	if err != nil {
		t.Fatalf("reading the stored runs: %v", err)
	}
	if len(stored) != 2 {
		t.Fatalf("stored runs = %+v", stored)
	}
	if mobile := stored[models.LighthouseStrategyMobile]; mobile == nil ||
		mobile.Performance == nil || *mobile.Performance != 42 {
		t.Fatalf("the mobile row was not updated in place: %+v", mobile)
	}
	if desktop := stored[models.LighthouseStrategyDesktop]; desktop == nil ||
		desktop.Performance == nil || *desktop.Performance != 70 {
		t.Fatalf("the desktop row was not kept: %+v", desktop)
	}

	// Dropping one run of a monitor must leave the other one alone (the strategy
	// of a monitor can be narrowed from "both" to a single run).
	if err := service.ClearExcept(ctx, 1, []string{models.LighthouseStrategyMobile}); err != nil {
		t.Fatalf("clearing the dropped run: %v", err)
	}
	if got := countRows(t, db, 1); got != 1 {
		t.Fatalf("monitor 1 holds %d rows after clearing, want 1", got)
	}
}

// primaryKeyOf reads the primary key columns of monitor_lighthouses in order.
func primaryKeyOf(t *testing.T, db *gorm.DB) []string {
	t.Helper()
	var columns []string
	err := db.Raw(`SELECT COLUMN_NAME FROM information_schema.KEY_COLUMN_USAGE
		WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'monitor_lighthouses'
		  AND CONSTRAINT_NAME = 'PRIMARY'
		ORDER BY ORDINAL_POSITION`).Scan(&columns).Error
	if err != nil {
		t.Fatalf("reading the primary key: %v", err)
	}
	return columns
}

// countRows counts the stored runs of a monitor.
func countRows(t *testing.T, db *gorm.DB, monitorID uint) int64 {
	t.Helper()
	var count int64
	if err := db.Model(&models.MonitorLighthouse{}).
		Where("monitor_id = ?", monitorID).Count(&count).Error; err != nil {
		t.Fatalf("counting the rows of monitor %d: %v", monitorID, err)
	}
	return count
}
