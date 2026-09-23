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
	ID          uint   `gorm:"primaryKey" json:"id"`
	UUID        string `gorm:"size:36;uniqueIndex;not null" json:"uuid"`
	Name        string `gorm:"size:150;not null;uniqueIndex" json:"name"`
	Description string `gorm:"size:500" json:"description"`
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
	return nil
}

// MonitorGroupMember links a monitor to a group. A monitor can belong to any
// number of groups (the status page and the dashboard filters rely on it).
type MonitorGroupMember struct {
	GroupID   uint `gorm:"primaryKey;index" json:"group_id"`
	MonitorID uint `gorm:"primaryKey;index" json:"monitor_id"`
	SortOrder int  `gorm:"not null;default:0" json:"sort_order"`
}
