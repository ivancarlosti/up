package models

import "time"

// SystemMetric is one sample of one machine: the series the charts read.
//
// The columns are the charts, not the protocol: a value the agent reports but
// this table does not need (the ZFS pools, the per interface counters) is
// skipped instead of stored as a JSON blob, so a chart is a plain column read.
// Extra carries the small map shaped leftovers (the temperature sensors) as JSON.
type SystemMetric struct {
	ID       uint64 `gorm:"primaryKey" json:"-"`
	SystemID uint   `gorm:"not null;index:idx_system_metrics_lookup,priority:1" json:"system_id"`
	// CreatedAt is the sample time (the poll that produced it), indexed with the
	// system so the range scan of a chart is one index range.
	CreatedAt time.Time `gorm:"index:idx_system_metrics_lookup,priority:2" json:"created_at"`

	CPU         float64 `json:"cpu"`
	MemPct      float64 `json:"mem_pct"`
	MemUsed     uint64  `json:"mem_used"`
	MemTotal    uint64  `json:"mem_total"`
	SwapPct     float64 `json:"swap_pct"`
	DiskPct     float64 `json:"disk_pct"`
	DiskUsed    uint64  `json:"disk_used"`
	DiskTotal   uint64  `json:"disk_total"`
	NetSent     uint64  `json:"net_sent"`
	NetRecv     uint64  `json:"net_recv"`
	DiskRead    uint64  `json:"disk_read"`
	DiskWrite   uint64  `json:"disk_write"`
	LoadAvg     float64 `json:"load_avg"`
	Temperature float64 `json:"temperature"`
	Uptime      uint64  `json:"uptime"`
	Containers  int     `json:"containers"`
	// Extra is a JSON object with the per sensor temperatures and the fan
	// readings, so a machine with ten cores of sensors stays one row.
	Extra string `gorm:"type:text" json:"extra,omitempty"`
}

// TableName keeps the table name stable.
func (SystemMetric) TableName() string { return "system_metrics" }

// SystemContainer is the latest known state of one container of one system.
//
// It is a snapshot table, not a history: the containers page lists what is
// running now, and the per container chart reads SystemContainerMetric. A
// container that disappears is deleted, so the listing never shows a ghost.
type SystemContainer struct {
	ID       uint   `gorm:"primaryKey" json:"-"`
	SystemID uint   `gorm:"not null;uniqueIndex:idx_system_containers_identity,priority:1" json:"system_id"`
	Name     string `gorm:"size:150;not null;uniqueIndex:idx_system_containers_identity,priority:2" json:"name"`

	ContainerID     string    `gorm:"size:64" json:"container_id,omitempty"`
	Image           string    `gorm:"size:255" json:"image,omitempty"`
	Status          string    `gorm:"size:32" json:"status,omitempty"`
	Health          string    `gorm:"size:16" json:"health,omitempty"`
	Ports           string    `gorm:"size:255" json:"ports,omitempty"`
	CPU             float64   `json:"cpu"`
	MemMB           float64   `json:"mem_mb"`
	NetSent         uint64    `json:"net_sent"`
	NetRecv         uint64    `json:"net_recv"`
	UpdateAvailable bool      `json:"update_available"`
	LastSeenAt      time.Time `json:"last_seen_at"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// TableName keeps the table name stable.
func (SystemContainer) TableName() string { return "system_containers" }

// SystemContainerMetric is one sample of one container: the history behind the
// per container charts and the "all containers" page.
type SystemContainerMetric struct {
	ID        uint64    `gorm:"primaryKey" json:"-"`
	SystemID  uint      `gorm:"not null;index:idx_container_metrics_lookup,priority:1" json:"system_id"`
	Name      string    `gorm:"size:150;not null;index:idx_container_metrics_lookup,priority:2" json:"name"`
	CreatedAt time.Time `gorm:"index:idx_container_metrics_lookup,priority:3" json:"created_at"`

	CPU     float64 `json:"cpu"`
	MemMB   float64 `json:"mem_mb"`
	NetSent uint64  `json:"net_sent"`
	NetRecv uint64  `json:"net_recv"`
}

// TableName keeps the table name stable.
func (SystemContainerMetric) TableName() string { return "system_container_metrics" }

// SystemSmart is the last SMART report of one machine, as the agent sent it.
//
// It is one row per system holding the devices as JSON: the shape is the agent's
// (a map of device to attributes that differ per protocol), the dashboard only
// renders it, and nothing queries inside it.
type SystemSmart struct {
	ID       uint `gorm:"primaryKey" json:"-"`
	SystemID uint `gorm:"not null;uniqueIndex" json:"system_id"`
	// Disks is a JSON object keyed by device name (beszel.SmartReport).
	Disks       string    `gorm:"type:longtext" json:"-"`
	Complete    bool      `json:"complete"`
	CollectedAt time.Time `json:"collected_at"`
	UpdatedAt   time.Time `json:"-"`
}

// TableName keeps the table name stable.
func (SystemSmart) TableName() string { return "system_smart" }
