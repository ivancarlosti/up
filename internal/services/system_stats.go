package services

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/ivancarlosti/up/internal/models"
)

// seriesPoints is how many points a chart asks for. The bucket of a window is
// derived from it, so every window renders at the same cost: one point per pixel
// column is already more than a 900 px chart can show.
const seriesPoints = 240

// seriesStep returns the bucket size of a window in seconds.
func seriesStep(hours int) int {
	seconds := hours * 3600
	step := seconds / seriesPoints
	if step < models.SystemSampleInterval {
		// A one hour chart is one sample per minute: downsampling it further would
		// only hide the spikes it exists to show.
		return models.SystemSampleInterval
	}
	return step
}

// Series returns the downsampled history of a system.
//
// The bucket is computed in SQL (one query, one index range): the rows of a 30 day
// window are 43 000 per machine and shipping them to the browser to average them
// there would be both slow and pointless.
func (s *SystemService) Series(ctx context.Context, systemID uint, hours int) ([]models.SystemPoint, error) {
	hours = models.NormalizeSystemChartWindow(hours)
	step := seriesStep(hours)
	since := time.Now().UTC().Add(-time.Duration(hours) * time.Hour)

	var points []models.SystemPoint
	err := s.db.WithContext(ctx).Model(&models.SystemMetric{}).
		Select(`FROM_UNIXTIME(FLOOR(UNIX_TIMESTAMP(created_at)/?)*?) AS created_at,
			AVG(cpu) AS cpu, AVG(mem_pct) AS mem_pct,
			CAST(AVG(mem_used) AS UNSIGNED) AS mem_used,
			MAX(mem_total) AS mem_total, AVG(swap_pct) AS swap_pct,
			AVG(disk_pct) AS disk_pct,
			CAST(AVG(disk_used) AS UNSIGNED) AS disk_used, MAX(disk_total) AS disk_total,
			CAST(AVG(net_sent) AS UNSIGNED) AS net_sent, CAST(AVG(net_recv) AS UNSIGNED) AS net_recv,
			CAST(AVG(disk_read) AS UNSIGNED) AS disk_read, CAST(AVG(disk_write) AS UNSIGNED) AS disk_write,
			AVG(load_avg) AS load_avg, MAX(temperature) AS temperature,
			MAX(uptime) AS uptime, MAX(containers) AS containers`, step, step).
		Where("system_id = ? AND created_at >= ?", systemID, since).
		Group(fmt.Sprintf("FLOOR(UNIX_TIMESTAMP(created_at)/%d)", step)).
		Order("created_at ASC").
		Find(&points).Error
	if err != nil {
		return nil, ErrInternal(fmt.Errorf("reading the history of system %d: %w", systemID, err))
	}
	return points, nil
}

// ContainerSeries returns the downsampled history of one container.
func (s *SystemService) ContainerSeries(ctx context.Context, systemID uint, name string, hours int) ([]models.ContainerPoint, error) {
	hours = models.NormalizeSystemChartWindow(hours)
	step := seriesStep(hours)
	since := time.Now().UTC().Add(-time.Duration(hours) * time.Hour)

	var points []models.ContainerPoint
	err := s.db.WithContext(ctx).Model(&models.SystemContainerMetric{}).
		Select(`FROM_UNIXTIME(FLOOR(UNIX_TIMESTAMP(created_at)/?)*?) AS created_at,
			AVG(cpu) AS cpu, AVG(mem_mb) AS mem_mb,
			CAST(AVG(net_sent) AS UNSIGNED) AS net_sent, CAST(AVG(net_recv) AS UNSIGNED) AS net_recv`, step, step).
		Where("system_id = ? AND name = ? AND created_at >= ?", systemID, name, since).
		Group(fmt.Sprintf("FLOOR(UNIX_TIMESTAMP(created_at)/%d)", step)).
		Order("created_at ASC").
		Find(&points).Error
	if err != nil {
		return nil, ErrInternal(fmt.Errorf("reading the history of container %q: %w", name, err))
	}
	return points, nil
}

// Containers lists the containers of one system.
func (s *SystemService) Containers(ctx context.Context, systemID uint) ([]models.SystemContainer, error) {
	var rows []models.SystemContainer
	if err := s.db.WithContext(ctx).Where("system_id = ?", systemID).
		Order("name ASC").Find(&rows).Error; err != nil {
		return nil, ErrInternal(fmt.Errorf("listing the containers of system %d: %w", systemID, err))
	}
	return rows, nil
}

// AllContainers lists every container of every system, for the "all containers"
// page. The rows carry the name of their system, so the page needs no join of its
// own.
func (s *SystemService) AllContainers(ctx context.Context) ([]models.ContainerRow, error) {
	var rows []models.ContainerRow
	err := s.db.WithContext(ctx).Model(&models.SystemContainer{}).
		Select("system_containers.*, systems.name AS system_name, systems.uuid AS system_uuid").
		Joins("JOIN systems ON systems.id = system_containers.system_id").
		Order("systems.name ASC, system_containers.name ASC").
		Find(&rows).Error
	if err != nil {
		return nil, ErrInternal(fmt.Errorf("listing every container: %w", err))
	}
	return rows, nil
}

// SmartReport is one system SMART row.
type SmartReport struct {
	SystemID   uint            `json:"system_id"`
	SystemName string          `json:"system_name"`
	Disks      json.RawMessage `json:"disks"`
	// Failed is the number of disks the report classified as failing: the
	// `smart` alert counts it, and the S.M.A.R.T. page can badge it without
	// walking the attribute list of every disk.
	Failed      int       `json:"failed"`
	Complete    bool      `json:"complete"`
	CollectedAt time.Time `json:"collected_at"`
}

// Smart lists the stored SMART reports, for the S.M.A.R.T. page.
func (s *SystemService) Smart(ctx context.Context, systemID uint) ([]SmartReport, error) {
	var rows []struct {
		models.SystemSmart
		SystemName string `gorm:"column:system_name"`
	}
	query := s.db.WithContext(ctx).Model(&models.SystemSmart{}).
		Select("system_smart.*, systems.name AS system_name").
		Joins("JOIN systems ON systems.id = system_smart.system_id").
		Order("systems.name ASC")
	if systemID != 0 {
		query = query.Where("system_smart.system_id = ?", systemID)
	}
	if err := query.Scan(&rows).Error; err != nil {
		return nil, ErrInternal(fmt.Errorf("listing the SMART reports: %w", err))
	}
	out := make([]SmartReport, 0, len(rows))
	for _, row := range rows {
		report := SmartReport{
			SystemID:    row.SystemID,
			SystemName:  row.SystemName,
			Failed:      row.Failed,
			Complete:    row.Complete,
			CollectedAt: row.CollectedAt,
		}
		if json.Valid([]byte(row.Disks)) {
			report.Disks = json.RawMessage(row.Disks)
		}
		out = append(out, report)
	}
	return out, nil
}

// Latest returns the most recent sample of every system.
//
// It is one query with a join on the last id per system instead of a query per
// machine: the listing asks for it on every render, and a dashboard of thirty
// systems would otherwise pay thirty round trips for thirty rows.
func (s *SystemService) Latest(ctx context.Context) ([]models.SystemMetric, error) {
	var rows []models.SystemMetric
	err := s.db.WithContext(ctx).Model(&models.SystemMetric{}).
		Joins("JOIN (SELECT system_id, MAX(id) AS id FROM system_metrics GROUP BY system_id) latest ON latest.id = system_metrics.id").
		Find(&rows).Error
	if err != nil {
		return nil, ErrInternal(fmt.Errorf("reading the latest samples: %w", err))
	}
	return rows, nil
}
