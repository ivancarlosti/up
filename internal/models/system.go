package models

import (
	"strings"
	"time"

	"github.com/google/uuid"
)

// ---------------------------------------------------------------------------
// Systems (hardware monitoring through a Beszel agent)
// ---------------------------------------------------------------------------
//
// A system is one machine: the operator installs the Beszel agent on it and
// pastes the token and KEY the dashboard shows (docs/hardware-monitoring.md).
// Up is then the hub: the agent dials us, and we ask it for the counters.
//
// The four tables below follow the same split as the rest of the project:
//
//	systems                 configuration + identity (synchronised)
//	system_leases           who is currently holding the agent connection
//	system_metrics          the per sample history (local to that owner)
//	system_containers       the latest state of each container (local)
//	system_container_metrics the per container history (local)
//	system_smart            the last SMART report (local)

// SystemStatus is the state of a system in the listing.
type SystemStatus string

// System states. They mirror the monitor states on purpose, so a badge means
// the same thing everywhere in the UI.
const (
	SystemStatusUp      SystemStatus = "up"
	SystemStatusDown    SystemStatus = "down"
	SystemStatusPending SystemStatus = "pending"
)

// System is one monitored machine.
type System struct {
	ID uint `gorm:"primaryKey" json:"id"`
	// UUID is the identity the peer protocol uses (never the local id).
	UUID         string `gorm:"size:36;uniqueIndex;not null" json:"uuid"`
	OriginNodeID string `gorm:"size:64" json:"origin_node_id,omitempty"`
	Revision     int64  `gorm:"not null;default:1" json:"revision,omitempty"`

	Name string `gorm:"size:120;not null;uniqueIndex" json:"name"`
	// Host is the operator's note about where the agent runs ("proxmox-02",
	// "10.0.0.14"). It is informational: the agent dials us, we never dial it.
	Host    string `gorm:"size:255" json:"host"`
	Enabled bool   `gorm:"not null;default:true" json:"enabled"`

	// TokenHash is the SHA-256 of the token the agent sends in X-Token. The
	// token itself is shown once, when the system is created, and never stored:
	// the hash is what a lookup needs, and it cannot be replayed as a credential.
	TokenHash   string `gorm:"size:64;index" json:"-"`
	TokenPrefix string `gorm:"size:12" json:"token_prefix,omitempty"`
	// Fingerprint is the identity the agent reported on its first handshake. It
	// is the machine, not the token: a second machine started with the same
	// token is refused instead of silently replacing the first.
	Fingerprint  string `gorm:"size:64" json:"fingerprint,omitempty"`
	AgentVersion string `gorm:"size:32" json:"agent_version,omitempty"`

	// Identity reported by the agent (ScreenSystemDetails).
	Hostname    string `gorm:"size:150" json:"hostname,omitempty"`
	OSName      string `gorm:"size:120" json:"os_name,omitempty"`
	Kernel      string `gorm:"size:120" json:"kernel,omitempty"`
	Arch        string `gorm:"size:32" json:"arch,omitempty"`
	CPUModel    string `gorm:"size:160" json:"cpu_model,omitempty"`
	Cores       int    `json:"cores,omitempty"`
	Threads     int    `json:"threads,omitempty"`
	MemoryTotal uint64 `json:"memory_total,omitempty"`
	// SmartInterval is how often the agent refreshes its SMART cache (seconds).
	SmartInterval int `json:"smart_interval,omitempty"`

	// LastSampleAt is when a sample was last stored (the listing's freshness)
	// and LastSeenAt when the agent was last connected at all.
	LastSampleAt *time.Time `json:"last_sample_at,omitempty"`
	LastSeenAt   *time.Time `json:"last_seen_at,omitempty"`
	// LastError is the last failure of the connection or of the agent, so the
	// operator sees why a system is down without reading the server log.
	LastError string `gorm:"size:500" json:"last_error,omitempty"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`

	// Runtime decoration (never stored).
	Status    SystemStatus `gorm:"-" json:"status"`
	OwnerNode string       `gorm:"-" json:"owner_node,omitempty"`
	Connected bool         `gorm:"-" json:"connected"`
	// OwnerNodeName is the display name of the node holding the connection.
	OwnerNodeName string `gorm:"-" json:"owner_node_name,omitempty"`
}

// TableName keeps the table name stable.
func (System) TableName() string { return "systems" }

// NewSystemUUID returns the identity of a new system.
func NewSystemUUID() string { return uuid.NewString() }

// SystemSampleInterval is the cadence of the poll of every system, in seconds.
//
// 60 s is what the dashboard charts expect and what the agent caches by default:
// asking more often would only re-read a cache, asking less often would make the
// charts coarse. It is a constant, not a setting, because the agent's own
// resolution is fixed by its collector.
const SystemSampleInterval = 60

// SystemDownAfter is how long a system may go without a sample before the
// listing calls it down: three missed polls, so one slow collection or one
// reconnect does not flip the badge.
const SystemDownAfter = 3 * SystemSampleInterval * time.Second

// SystemDetailsRefresh is how often the identity (cores, memory total, kernel)
// is asked for again: it never changes, but a reboot after a hardware upgrade
// is exactly when the operator expects the numbers to follow.
const SystemDetailsRefresh = time.Hour

// SystemSmartRefresh is how often the SMART report is requested. The agent
// refreshes its own cache on SMART_INTERVAL (or hourly) and answers from it, so
// asking every 10 minutes is cheap and keeps a failing disk visible.
const SystemSmartRefresh = 10 * time.Minute

// NormalizeSystemName trims and clamps the operator input.
func NormalizeSystemName(name string) string {
	return strings.TrimSpace(name)
}
