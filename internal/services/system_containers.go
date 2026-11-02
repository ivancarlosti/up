package services

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/ivancarlosti/up/internal/beszel"
	"github.com/ivancarlosti/up/internal/models"
)

// storeContainers refreshes the container snapshot and appends the history of the
// containers that are running.
//
// The snapshot is a mirror of what the agent reported: a container that is gone
// from the answer is gone from the listing, but its history stays (the chart of a
// container that was removed last week is still a legitimate chart).
func (s *SystemService) storeContainers(ctx context.Context, tx *gorm.DB, systemID uint, containers []*beszel.Container, now time.Time) error {
	seen := make(map[string]bool, len(containers))
	for _, container := range containers {
		if container == nil || strings.TrimSpace(container.Name) == "" {
			continue
		}
		name := trimmed(container.Name, 150)
		if seen[name] {
			continue
		}
		seen[name] = true
		netSent, netRecv := container.Bandwidth[0], container.Bandwidth[1]
		if netSent == 0 && netRecv == 0 {
			netSent = uint64(container.NetworkSent * 1024 * 1024)
			netRecv = uint64(container.NetworkRecv * 1024 * 1024)
		}
		row := models.SystemContainer{
			SystemID:        systemID,
			Name:            name,
			ContainerID:     trimmed(container.ID, 64),
			Image:           trimmed(container.Image, 255),
			Status:          trimmed(container.Status, 32),
			Health:          beszel.HealthName(container.Health),
			Ports:           trimmed(container.Ports, 255),
			CPU:             container.Cpu,
			MemMB:           container.Mem,
			NetSent:         netSent,
			NetRecv:         netRecv,
			UpdateAvailable: container.UpdateAvailable,
			LastSeenAt:      now,
			CreatedAt:       now,
			UpdatedAt:       now,
		}
		if err := tx.WithContext(ctx).Clauses(clause.OnConflict{
			Columns: []clause.Column{{Name: "system_id"}, {Name: "name"}},
			DoUpdates: clause.AssignmentColumns([]string{
				"container_id", "image", "status", "health", "ports", "cpu", "mem_mb",
				"net_sent", "net_recv", "update_available", "last_seen_at", "updated_at",
			}),
		}).Create(&row).Error; err != nil {
			return fmt.Errorf("storing container %q: %w", name, err)
		}
		// Only a running container is charted: a stopped one has no counters, and a
		// flat zero line in the middle of an incident is worse than a gap.
		if !ContainerRunning(row.Status) {
			continue
		}
		point := models.SystemContainerMetric{
			SystemID:  systemID,
			Name:      name,
			CreatedAt: now,
			CPU:       row.CPU,
			MemMB:     row.MemMB,
			NetSent:   row.NetSent,
			NetRecv:   row.NetRecv,
		}
		if err := tx.WithContext(ctx).Create(&point).Error; err != nil {
			return fmt.Errorf("storing the history of container %q: %w", name, err)
		}
	}
	if len(seen) == 0 {
		return tx.WithContext(ctx).Where("system_id = ?", systemID).Delete(&models.SystemContainer{}).Error
	}
	names := make([]string, 0, len(seen))
	for name := range seen {
		names = append(names, name)
	}
	return tx.WithContext(ctx).Where("system_id = ? AND name NOT IN ?", systemID, names).
		Delete(&models.SystemContainer{}).Error
}

// ContainerRunning reports whether the status the agent reported means the
// container is running.
//
// The agent sends Docker's human status (`/containers/json` -> Status), not the
// state field: a running container reads "Up 2 days" and a stopped one "Exited
// (0) 3 hours ago". Comparing against the literal "running" therefore classified
// every container as stopped, and no container history was ever written - the bug
// this comment exists to keep fixed (verified against a real agent 0.21.1).
func ContainerRunning(status string) bool {
	trimmed := strings.ToLower(strings.TrimSpace(status))
	return trimmed == "running" || strings.HasPrefix(trimmed, "up")
}

// RefreshSmart stores the SMART report of a system.
//
// The report is a JSON blob: its shape is the agent's (a map of device to
// attributes that differ per protocol: ATA, NVMe and SCSI report different
// things), the dashboard only renders it, and nothing queries inside it.
func (s *SystemService) RefreshSmart(ctx context.Context, systemID uint, fetch func() (beszel.SmartReport, error)) {
	report, err := fetch()
	if err != nil {
		s.log.Debug("could not read the SMART report", "system_id", systemID, "error", err)
		return
	}
	encoded, err := json.Marshal(report.Disks)
	if err != nil {
		s.log.Warn("could not encode the SMART report", "system_id", systemID, "error", err)
		return
	}
	// The count the `smart` alert watches is denormalised here: a failing disk
	// must not require parsing the blob on every tick of the sweep.
	failed := 0
	for _, disk := range report.Disks {
		if !beszel.SmartDiskHealthy(disk.Status) {
			failed++
		}
	}
	row := models.SystemSmart{
		SystemID:    systemID,
		Disks:       string(encoded),
		Failed:      failed,
		Complete:    report.Complete,
		CollectedAt: report.At,
		UpdatedAt:   report.At,
	}
	if err := s.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "system_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"disks", "failed", "complete", "collected_at", "updated_at"}),
	}).Create(&row).Error; err != nil {
		s.log.Warn("could not store the SMART report", "system_id", systemID, "error", err)
		return
	}
	s.publish("system.smart", map[string]any{"id": systemID, "disks": len(report.Disks), "failed": failed})
	// The report refreshes on its own slow cadence, so the rule is evaluated
	// right here: waiting for the next sample would delay a failing disk by
	// minutes (and the sample may belong to another node entirely).
	if s.alerts != nil {
		s.alerts.EvaluateMetric(ctx, systemID, models.SystemAlertSmart)
	}
}
