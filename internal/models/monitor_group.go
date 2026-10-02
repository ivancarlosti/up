package models

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// MonitorGroup is a named collection of monitors.
//
// Groups are what the operator uses to organise the monitor list and, from the
// status page side, to publish every monitor of a group at once: adding a
// monitor to a group makes it appear on the pages that include the group.
//
// The UUID exists because a group is configuration, not runtime data: the id is
// only meaningful inside one database, while the UUID survives an export/import
// (and the cluster synchronisation of a future release).
type MonitorGroup struct {
	ID   uint   `gorm:"primaryKey" json:"id"`
	UUID string `gorm:"size:36;uniqueIndex;not null" json:"uuid"`
	// OriginNodeID and Revision complete the sync identity of the row: the UUID
	// above is the global id, these two say who wrote it last and how many
	// times (docs/clustering-modes.md).
	OriginNodeID string `gorm:"size:64" json:"origin_node_id"`
	Revision     int64  `gorm:"not null;default:1" json:"revision"`
	Name         string `gorm:"size:150;not null;uniqueIndex" json:"name"`
	Description  string `gorm:"size:500" json:"description"`
	// Color is an optional CSS colour used by the UI badge of the group.
	Color string `gorm:"size:20" json:"color"`
	// SortOrder orders the groups in the UI (equal values fall back to name).
	SortOrder int `gorm:"not null;default:0" json:"sort_order"`

	CreatedAt time.Time `json:"created_at"`
	// Groups are hard deleted, like monitors and notification channels: a soft
	// delete would keep the name busy in the unique index forever (deleting and
	// recreating "Ops" would fail). The UUID is what a future export/import or
	// cluster synchronisation can use to correlate rows.
	UpdatedAt time.Time `json:"updated_at"`

	// Runtime fields: filled by the service for the API responses.
	MonitorIDs   []uint `gorm:"-" json:"monitor_ids"`
	MonitorCount int    `gorm:"-" json:"monitor_count"`
}

// BeforeCreate fills the UUID so every group is globally identifiable even when
// the row is created by a seed or a test.
func (g *MonitorGroup) BeforeCreate(tx *gorm.DB) error {
	if g.UUID == "" {
		g.UUID = uuid.NewString()
	}
	if g.Revision == 0 {
		g.Revision = 1
	}
	return nil
}

// MonitorGroupMember links a monitor to a group. A monitor belongs to at most one
// group: the grouping is how the operator organises the list, and a monitor in two
// groups had two places claiming it (and appeared twice on a page that included
// both). The write paths keep the rule - setMonitorGroup replaces the membership
// of a monitor and replaceMonitorGroupMembers moves the monitors that join a group
// - and the unique index on monitor_id (added once the tables are known to hold no
// duplicate) enforces it in the schema.
type MonitorGroupMember struct {
	GroupID   uint `gorm:"primaryKey;index" json:"group_id"`
	MonitorID uint `gorm:"primaryKey;index" json:"monitor_id"`
	SortOrder int  `gorm:"not null;default:0" json:"sort_order"`
}

// MonitorGroupRef is the single group a monitor belongs to, as the read path
// resolves it: the id that a write uses and the name the UI (and the notification
// bodies) read. A monitor carries at most one of them, and none at all when it
// belongs to no group. It is an internal helper (GroupRefsByMonitor, groupRef,
// notify.NewMessage): the monitor payload exposes the pair flat, as group_id and
// group_name.
type MonitorGroupRef struct {
	ID   uint   `json:"id"`
	Name string `json:"name"`
}
