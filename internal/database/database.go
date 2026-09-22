// Package database owns the MariaDB/MySQL connection, the automatic GORM
// migrations and the first boot seed data.
//
// The database is always external to the container (DB_HOST defaults to
// host.docker.internal), and every node of a cluster points to the same
// database, which is what keeps monitors and heartbeats synchronised.
package database

import (
	"database/sql"
	"fmt"
	"log/slog"
	"time"

	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"github.com/ivancarlosti/up/internal/config"
	"github.com/ivancarlosti/up/internal/models"
)

// Connect opens the database, retrying with an exponential backoff so the
// container can start together with its database.
func Connect(cfg *config.Config, log *slog.Logger) (*gorm.DB, error) {
	logLevel := gormlogger.Warn
	if cfg.LogLevel == "debug" {
		logLevel = gormlogger.Info
	}

	gormCfg := &gorm.Config{
		// Every timestamp in Up is UTC; the UI converts to the local zone.
		NowFunc:                func() time.Time { return time.Now().UTC() },
		SkipDefaultTransaction: true,
		Logger:                 gormlogger.Default.LogMode(logLevel),
	}

	var (
		db      *gorm.DB
		err     error
		backoff = time.Second
	)
	deadline := time.Now().Add(90 * time.Second)

	for attempt := 1; ; attempt++ {
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
				log.Info("database connection established", "dsn", cfg.SafeDSN(), "attempts", attempt)
				return db, nil
			}
		}

		if time.Now().After(deadline) {
			return nil, fmt.Errorf("could not connect to the database at %s after 90s: %w", cfg.SafeDSN(), err)
		}
		log.Warn("database not ready yet, retrying", "attempt", attempt, "in", backoff.String(), "error", err)
		time.Sleep(backoff)
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
		&models.MonitorState{},
		&models.Heartbeat{},
		&models.Notification{},
		&models.NotificationLog{},
		&models.NotificationLock{},
		&models.Node{},
		&models.ClusterSettings{},
		&models.StatusPage{},
		&models.StatusPageMonitor{},
		&models.APIToken{},
		&models.IPRule{},
	}
}

// Migrate runs the automatic GORM migrations.
func Migrate(db *gorm.DB, log *slog.Logger) error {
	if err := db.AutoMigrate(MigrationModels()...); err != nil {
		return fmt.Errorf("running automatic migrations: %w", err)
	}
	log.Info("database schema up to date", "tables", len(MigrationModels()))
	return nil
}
