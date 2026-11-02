package models

import (
	"encoding/json"
	"time"
)

// SystemPoint is one point of a hardware chart.
//
// The rows are downsampled when they are read: a 30 day chart asks for one point
// every two hours, so the payload never depends on how much history exists and
// the browser never renders 43 000 points. Its fields are the averages (or the
// maxima, for the values where a peak is what matters) of the bucket.
type SystemPoint struct {
	CreatedAt   time.Time `json:"created_at"`
	CPU         float64   `json:"cpu"`
	MemPct      float64   `json:"mem_pct"`
	MemUsed     uint64    `json:"mem_used"`
	MemTotal    uint64    `json:"mem_total"`
	SwapPct     float64   `json:"swap_pct"`
	DiskPct     float64   `json:"disk_pct"`
	DiskUsed    uint64    `json:"disk_used"`
	DiskTotal   uint64    `json:"disk_total"`
	NetSent     uint64    `json:"net_sent"`
	NetRecv     uint64    `json:"net_recv"`
	DiskRead    uint64    `json:"disk_read"`
	DiskWrite   uint64    `json:"disk_write"`
	LoadAvg     float64   `json:"load_avg"`
	Temperature float64   `json:"temperature"`
	Uptime      uint64    `json:"uptime"`
	Containers  int       `json:"containers"`
}

// ContainerPoint is one point of a container chart.
type ContainerPoint struct {
	CreatedAt time.Time `json:"created_at"`
	CPU       float64   `json:"cpu"`
	MemMB     float64   `json:"mem_mb"`
	NetSent   uint64    `json:"net_sent"`
	NetRecv   uint64    `json:"net_recv"`
}

// ContainerRow is one row of the "all containers" page: the snapshot of a
// container plus the name of the system it runs on.
//
// The two extra fields are filled by the SELECT of AllContainers, so they carry
// no `gorm:"-"` (which would tell GORM to skip the column and leave them empty).
type ContainerRow struct {
	SystemContainer
	SystemName string `json:"system_name"`
	SystemUUID string `json:"system_uuid,omitempty"`
}

// SystemHistory is everything the detail page needs in one answer.
//
// It is one payload instead of four because the four are always rendered
// together: four round trips would let the charts disagree with the container
// list for as long as the slowest of them takes.
type SystemHistory struct {
	System     System            `json:"system"`
	Metrics    []SystemPoint     `json:"metrics"`
	Containers []SystemContainer `json:"containers"`
	Smart      json.RawMessage   `json:"smart,omitempty"`
	SmartAt    *time.Time        `json:"smart_at,omitempty"`
	SmartOK    bool              `json:"smart_complete"`
	// ContainerSeries is only filled for a specific container (the detail of one
	// container), so the "all containers" page stays small.
	ContainerSeries []ContainerPoint `json:"container_series,omitempty"`
	// Remote is true when the answer came from the node that owns the agent,
	// instead of from this node's own database.
	Remote bool `json:"remote,omitempty"`
}

// SystemSeriesResponse is the wire form of a history read across the cluster.
//
// It is served by the node that owns the agent connection and consumed by the
// nodes that do not (docs/hardware-monitoring.md): the samples stay local to the
// owner, exactly like the heartbeat stream, and a peer reads them instead of
// replicating them.
type SystemSeriesResponse struct {
	ProtocolVersion int               `json:"protocol_version"`
	UUID            string            `json:"uuid"`
	Hours           int               `json:"hours"`
	Metrics         []SystemPoint     `json:"metrics"`
	Containers      []SystemContainer `json:"containers"`
	Smart           json.RawMessage   `json:"smart,omitempty"`
	SmartAt         *time.Time        `json:"smart_at,omitempty"`
	SmartOK         bool              `json:"smart_complete"`
	// System carries the identity the owner sees (hostname, cores, agent
	// version), which the peers do not have: it is an observation of the machine,
	// not configuration.
	System *System `json:"system,omitempty"`
}
