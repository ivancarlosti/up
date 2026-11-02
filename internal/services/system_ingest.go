package services

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"gorm.io/gorm"

	"github.com/ivancarlosti/up/internal/beszel"
	"github.com/ivancarlosti/up/internal/models"
)

// gib is the byte size of one GiB. The agent reports the root filesystem in GB
// and the memory either in bytes (0.18+) or in GB (older builds).
const gib = 1024 * 1024 * 1024

// IngestSample stores one sample of a machine: the history row, the containers
// and the identity the agent reported.
//
// Everything happens in one transaction: a sample that stored its counters but
// not its containers would make the containers page disagree with the charts for
// a whole minute, and the two are read together.
func (s *SystemService) IngestSample(ctx context.Context, system *models.System, data *beszel.CombinedData) error {
	if data == nil {
		return nil
	}
	now := time.Now().UTC()
	metric := metricFromSample(system.ID, data, now)

	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&metric).Error; err != nil {
			return fmt.Errorf("storing the sample: %w", err)
		}
		if err := s.storeContainers(ctx, tx, system.ID, data.Containers, now); err != nil {
			return err
		}
		columns := map[string]any{
			"last_sample_at": now,
			"last_seen_at":   now,
			"last_error":     "",
			"updated_at":     now,
		}
		// The identity is only rewritten when the agent sent it (IncludeDetails):
		// an empty value would erase what a previous sample already knows.
		if details := data.Details; details != nil {
			columns["hostname"] = trimmed(details.Hostname, 150)
			columns["kernel"] = trimmed(details.Kernel, 120)
			columns["os_name"] = trimmed(details.OSName, 120)
			columns["arch"] = trimmed(details.Arch, 32)
			columns["cpu_model"] = trimmed(details.CpuModel, 160)
			columns["cores"] = details.Cores
			columns["threads"] = details.Threads
			columns["memory_total"] = details.MemoryTotal
			columns["smart_interval"] = int(details.SmartInterval / int64(time.Second))
		} else if system.Hostname == "" && data.Info.Hostname != "" {
			// A pre-0.18 agent has no Details: its identity lives in Info.
			columns["hostname"] = trimmed(data.Info.Hostname, 150)
			columns["kernel"] = trimmed(data.Info.KernelVersion, 120)
			columns["cores"] = data.Info.Cores
			columns["cpu_model"] = trimmed(data.Info.CpuModel, 160)
		}
		if version := trimmed(data.Info.AgentVersion, 32); version != "" {
			columns["agent_version"] = version
		}
		if err := tx.Model(&models.System{}).Where("id = ?", system.ID).Updates(columns).Error; err != nil {
			return fmt.Errorf("updating the system row: %w", err)
		}
		return nil
	})
	if err != nil {
		return err
	}

	// A per sample event keeps the cards alive without re-reading the listing;
	// the first sample of a system is flagged so the UI can drop its "pending".
	s.publish("system.sample", map[string]any{
		"id":         system.ID,
		"created_at": now,
		"cpu":        metric.CPU,
		"mem_pct":    metric.MemPct,
		"disk_pct":   metric.DiskPct,
		"containers": metric.Containers,
		"first":      system.LastSampleAt == nil,
	})
	return nil
}

// metricFromSample converts one sample into the row the charts read.
func metricFromSample(systemID uint, data *beszel.CombinedData, at time.Time) models.SystemMetric {
	stats := data.Stats
	info := data.Info

	memTotal := stats.MemBytes[0]
	memUsed := stats.MemBytes[1]
	if memTotal == 0 {
		// An agent older than the byte sized fields reports GB only.
		memTotal = uint64(stats.Mem * gib)
		memUsed = uint64(stats.MemUsed * gib)
	}
	memPct := stats.MemPct
	if memPct == 0 && memTotal > 0 {
		memPct = float64(memUsed) / float64(memTotal) * 100
	}

	netSent, netRecv := stats.Bandwidth[0], stats.Bandwidth[1]
	if netSent == 0 && netRecv == 0 {
		netSent = uint64(stats.NetworkSent * 1024 * 1024)
		netRecv = uint64(stats.NetworkRecv * 1024 * 1024)
	}

	swapPct := 0.0
	if stats.SwapBytes[0] > 0 {
		swapPct = float64(stats.SwapBytes[1]) / float64(stats.SwapBytes[0]) * 100
	} else if stats.Swap > 0 {
		swapPct = stats.SwapUsed / stats.Swap * 100
	}

	loadAvg := stats.LoadAvg[0]
	if loadAvg == 0 {
		loadAvg = info.LoadAvg[0]
	}

	extra, _ := json.Marshal(map[string]any{
		"temperatures": stats.Temperatures,
		"fans":         stats.Fans,
		"cpu":          stats.CpuBreakdown,
		"wifi":         stats.WiFi,
	})

	return models.SystemMetric{
		SystemID:    systemID,
		CreatedAt:   at,
		CPU:         stats.CPU,
		MemPct:      memPct,
		MemUsed:     memUsed,
		MemTotal:    memTotal,
		SwapPct:     swapPct,
		DiskPct:     stats.DiskPct,
		DiskUsed:    uint64(stats.DiskUsed * gib),
		DiskTotal:   uint64(stats.DiskTotal * gib),
		NetSent:     netSent,
		NetRecv:     netRecv,
		DiskRead:    stats.DiskIO[0],
		DiskWrite:   stats.DiskIO[1],
		LoadAvg:     loadAvg,
		Temperature: maxTemperature(stats.Temperatures),
		Uptime:      info.Uptime,
		Containers:  len(data.Containers),
		Extra:       string(extra),
	}
}

// maxTemperature returns the hottest sensor of a sample, which is what the chart
// and the card show: the per sensor detail is in the Extra column.
func maxTemperature(temperatures map[string]float64) float64 {
	hottest := 0.0
	for _, value := range temperatures {
		if value > hottest {
			hottest = value
		}
	}
	return hottest
}
