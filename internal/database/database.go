// Package database owns the MariaDB/MySQL connection, the automatic GORM
// migrations and the first boot seed data.
//
// The database is always external to the container (DB_HOST defaults to
// host.docker.internal), and every node of a cluster points to the same
// database, which is what keeps monitors and heartbeats synchronised.
package database

import (
	"context"
	"database/sql"
	"fmt"
	stdlog "log"
	"log/slog"
	"os"
	"time"

	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"github.com/ivancarlosti/up/internal/boot"
	"github.com/ivancarlosti/up/internal/config"
	"github.com/ivancarlosti/up/internal/i18n"
	"github.com/ivancarlosti/up/internal/models"
)

// Connect opens the database, retrying with an exponential backoff so the
// container can start together with its database.
//
// The retry never gives up: a process without a database cannot serve anything,
// but it can SAY what it is waiting for. The listener is already open and the
// bootstrap engine answers /api/boot with the classified error (see
// internal/boot), so exiting after a fixed window would only replace a readable
// page with a crash loop that restarts the 90 seconds over and over. It returns
// only when the context is cancelled (the process was asked to stop).
func Connect(ctx context.Context, cfg *config.Config, log *slog.Logger, state *boot.State) (*gorm.DB, error) {
	logLevel := gormlogger.Warn
	if cfg.LogLevel == "debug" {
		logLevel = gormlogger.Info
	}

	// A "record not found" is a normal answer for the lookups that guard the
	// first boot seed or a deleted row, so it must not be printed as an error
	// (gormlogger.Default logs it together with its SQL statement).
	dbLogger := gormlogger.New(
		stdlog.New(os.Stdout, "", stdlog.LstdFlags),
		gormlogger.Config{
			// 500 ms keeps a busy cluster quiet: with the periodic reconciliation
			// fanning out, a 200 ms threshold warns about queries that are merely
			// unlucky, not slow.
			SlowThreshold:             500 * time.Millisecond,
			LogLevel:                  logLevel,
			IgnoreRecordNotFoundError: true,
			Colorful:                  false, // docker logs have no TTY colours
		},
	)

	gormCfg := &gorm.Config{
		// Every timestamp in Up is UTC; the UI converts to the local zone.
		NowFunc:                func() time.Time { return time.Now().UTC() },
		SkipDefaultTransaction: true,
		Logger:                 dbLogger,
	}

	var (
		db      *gorm.DB
		err     error
		backoff = time.Second
		start   = time.Now()
	)

	for attempt := 1; ; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		db, err = gorm.Open(mysql.Open(cfg.DSN()), gormCfg)
		if err == nil {
			var sqlDB *sql.DB
			if sqlDB, err = db.DB(); err == nil {
				err = sqlDB.Ping()
			}
			if err == nil {
				sqlDB.SetMaxOpenConns(30)
				sqlDB.SetMaxIdleConns(5)
				sqlDB.SetConnMaxLifetime(30 * time.Minute)
				log.Info("database connection established",
					"dsn", cfg.SafeDSN(), "attempts", attempt,
					"took_ms", time.Since(start).Milliseconds())
				return db, nil
			}
		}

		code, detail := boot.Classify(err)
		if code == "" {
			// An error the classifier does not know is still a connection that
			// did not work: report it as unreachable, with the driver message.
			code, detail = i18n.CodeDatabaseUnreachable, err.Error()
		}
		state.Retry(attempt, code, detail, backoff)
		log.Warn("database not ready yet, retrying",
			"attempt", attempt, "in", backoff.String(), "code", code, "error", err)

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(backoff):
		}
		if backoff < 8*time.Second {
			backoff *= 2
		}
	}
}

// MigrationModels is the ordered list of tables created/updated on boot.
func MigrationModels() []any {
	return []any{
		&models.Setting{},
		&models.Monitor{},
		&models.MonitorNotification{},
		&models.MonitorGroup{},
		&models.MonitorGroupMember{},
		&models.MonitorTemplate{},
		&models.MonitorCertificate{},
		&models.MonitorDomain{},
		&models.WhoisParser{},
		&models.MonitorState{},
		&models.Heartbeat{},
		&models.HeartbeatRollup{},
		&models.Notification{},
		&models.NotificationLog{},
		&models.NotificationLock{},
		&models.Node{},
		&models.ClusterSettings{},
		&models.SyncPeer{},
		// Synchronisation (docs/clustering-modes.md, phase 2).
		&models.SyncOutbox{},
		&models.SyncObject{},
		&models.SyncConflict{},
		&models.SyncPendingLink{},
		&models.SyncDeadLetter{},
		&models.PeerVote{},
		&models.StatusPage{},
		&models.StatusPageMonitor{},
		&models.StatusPageGroupLink{},
		&models.APIToken{},
		&models.IPRule{},
	}
}

// Migrate runs the automatic GORM migrations.
func Migrate(db *gorm.DB, log *slog.Logger) error {
	if err := db.AutoMigrate(MigrationModels()...); err != nil {
		return fmt.Errorf("running automatic migrations: %w", err)
	}
	if err := dropLegacyIndexes(db, log); err != nil {
		return err
	}
	log.Info("database schema up to date", "tables", len(MigrationModels()))
	return nil
}

// legacyIndexes are the indexes an older schema carried and the current models
// no longer declare. AutoMigrate only ever ADDS what is missing - it never drops
// anything, so a rename or a merge of two indexes has to be spelled out.
var legacyIndexes = []struct {
	model any
	name  string
}{
	// idx_heartbeats_status indexed `status` on its own. No query filters on a
	// status without a monitor and a time range, so the index was never picked
	// by the optimizer and only made every insert more expensive. idx_hb_stats
	// now carries `status` as its third column, where it is actually read.
	{model: &models.Heartbeat{}, name: "idx_heartbeats_status"},
}

// dropLegacyIndexes removes the indexes AutoMigrate would leave behind.
// Idempotent: an index that is already gone is skipped.
func dropLegacyIndexes(db *gorm.DB, log *slog.Logger) error {
	for _, legacy := range legacyIndexes {
		if !db.Migrator().HasIndex(legacy.model, legacy.name) {
			continue
		}
		if err := db.Migrator().DropIndex(legacy.model, legacy.name); err != nil {
			return fmt.Errorf("dropping legacy index %s: %w", legacy.name, err)
		}
		log.Info("legacy index dropped", "index", legacy.name)
	}
	return nil
}
