// This file holds an OPT-IN integration check of the rdap_status column written by
// DomainService.Record (see domain.go, models.MonitorDomain and docs/api.md).
//
// The registry status list is stored as a CSV whose entries contain spaces
// ("client transfer prohibited"), and it is written through an upsert that lists
// the column explicitly. A unit test cannot tell a misspelled column, a missing
// entry in the ON CONFLICT assignment or a split that shreds the phrases from a
// working one - all of them would quietly lose the status while the API keeps
// answering. It is skipped unless UP_SCRATCH_DSN points at a throwaway
// MariaDB/MySQL database. The tables are created when they are missing, and every
// row it writes lives inside a transaction that is rolled back at the end. The
// usual way to run it:
//
//	docker run --rm -d --name up-domain-verify -p 127.0.0.1:3309:3306 \
//	  -e MARIADB_ROOT_PASSWORD=verify -e MARIADB_DATABASE=up \
//	  -e MARIADB_USER=up -e MARIADB_PASSWORD=verify mariadb:11
//	UP_SCRATCH_DSN='up:verify@tcp(127.0.0.1:3309)/up?charset=utf8mb4&parseTime=true&loc=UTC' \
//	  go test ./internal/services/ -run TestDomainRecordRegistryStatusLive -v
//	docker rm -f up-domain-verify
package services

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

	"github.com/ivancarlosti/up/internal/config"
	"github.com/ivancarlosti/up/internal/models"
)

// TestDomainRecordRegistryStatusLive pins the storage of the RDAP registry status:
// the upsert writes the column, the phrases keep their spaces, a re-observation
// only reports a change when the status really moved, and a lookup without a
// status (manual date, WHOIS, error) clears it instead of leaving a stale one.
func TestDomainRecordRegistryStatusLive(t *testing.T) {
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
	if err := db.AutoMigrate(&models.Monitor{}, &models.MonitorDomain{}); err != nil {
		t.Fatalf("migrating the scratch database: %v", err)
	}

	tx := db.Begin()
	if tx.Error != nil {
		t.Fatalf("starting the transaction: %v", tx.Error)
	}
	defer func() {
		if err := tx.Rollback().Error; err != nil {
			t.Errorf("rolling back: %v", err)
		}
	}()

	suffix := time.Now().UTC().Format("20060102150405.000000")
	monitor := &models.Monitor{Name: "zz-verify-domain-" + suffix, Type: models.MonitorTypeHTTP}
	if err := tx.Create(monitor).Error; err != nil {
		t.Fatalf("creating the monitor: %v", err)
	}

	service := NewDomainService(tx, &config.Config{NodeID: "verify"},
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	// First observation: the RDAP answer carries the locked status.
	changed, err := service.Record(ctx, monitor.ID, "verify", &models.DomainInfo{
		Domain:     "example.com",
		Registrar:  "ACME Registrar",
		ExpiresAt:  now.AddDate(2, 0, 0),
		Source:     models.DomainSourceRDAP,
		Status:     models.DomainStatusOK,
		RDAPStatus: []string{"client transfer prohibited", "active"},
		CheckedAt:  now,
	})
	if err != nil {
		t.Fatalf("recording the first observation: %v", err)
	}
	if !changed {
		t.Fatal("the first observation must report a change")
	}
	row, err := service.Get(ctx, monitor.ID)
	if err != nil || row == nil {
		t.Fatalf("reading back the row: %v (%v)", err, row)
	}
	if row.RDAPStatus != "client transfer prohibited,active" {
		t.Fatalf("rdap_status = %q, want the phrases joined on the comma", row.RDAPStatus)
	}
	if got := row.RDAPStatusList(); len(got) != 2 || got[0] != "client transfer prohibited" {
		t.Fatalf("RDAPStatusList() = %v, want the phrases preserved", got)
	}

	// The bookkeeping is written after the observation, then a second identical
	// observation must not look like a change nor reset the memory.
	if err := tx.Model(&models.MonitorDomain{}).Where("monitor_id = ?", monitor.ID).
		UpdateColumns(map[string]any{"notified_days": "30,7"}).Error; err != nil {
		t.Fatalf("storing the notification memory: %v", err)
	}
	changed, err = service.Record(ctx, monitor.ID, "verify", &models.DomainInfo{
		Domain:     "example.com",
		Registrar:  "ACME Registrar",
		ExpiresAt:  now.AddDate(2, 0, 0),
		Source:     models.DomainSourceRDAP,
		Status:     models.DomainStatusOK,
		RDAPStatus: []string{"client transfer prohibited", "active"},
		CheckedAt:  now.Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("recording the repeat observation: %v", err)
	}
	if changed {
		t.Fatal("the same status must not report a change (the comparison uses the stored CSV)")
	}
	row, err = service.Get(ctx, monitor.ID)
	if err != nil || row == nil {
		t.Fatalf("reading back the row after the repeat: %v (%v)", err, row)
	}
	if row.NotifiedDays != "30,7" {
		t.Fatalf("notified_days = %q, the observation must not reset the memory", row.NotifiedDays)
	}

	// The registry lifts the lock: the upsert must update the column.
	changed, err = service.Record(ctx, monitor.ID, "verify", &models.DomainInfo{
		Domain:     "example.com",
		Registrar:  "ACME Registrar",
		ExpiresAt:  now.AddDate(2, 0, 0),
		Source:     models.DomainSourceRDAP,
		Status:     models.DomainStatusOK,
		RDAPStatus: []string{"active"},
		CheckedAt:  now.Add(2 * time.Hour),
	})
	if err != nil {
		t.Fatalf("recording the unlocked observation: %v", err)
	}
	if !changed {
		t.Fatal("a moved status must report a change")
	}
	row, err = service.Get(ctx, monitor.ID)
	if err != nil || row == nil {
		t.Fatalf("reading back the row after the update: %v (%v)", err, row)
	}
	if row.RDAPStatus != "active" {
		t.Fatalf("rdap_status = %q, want the upsert to have updated the column", row.RDAPStatus)
	}

	// A manual/WHOIS observation advertises no registry status: the column is
	// cleared, not left with the stale RDAP list.
	changed, err = service.Record(ctx, monitor.ID, "verify", &models.DomainInfo{
		Domain:    "example.com",
		ExpiresAt: now.AddDate(2, 0, 0),
		Source:    models.DomainSourceWHOIS,
		Status:    models.DomainStatusOK,
		CheckedAt: now.Add(3 * time.Hour),
	})
	if err != nil {
		t.Fatalf("recording the WHOIS observation: %v", err)
	}
	if !changed {
		t.Fatal("dropping the status must report a change")
	}
	row, err = service.Get(ctx, monitor.ID)
	if err != nil || row == nil {
		t.Fatalf("reading back the row after the WHOIS observation: %v (%v)", err, row)
	}
	if row.RDAPStatus != "" {
		t.Fatalf("rdap_status = %q, a lookup without a status must clear the column", row.RDAPStatus)
	}
}
