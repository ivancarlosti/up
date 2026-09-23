package models

import "time"

// StatusPage is a public, read-only dashboard of a selection of monitors.
type StatusPage struct {
	ID   uint   `gorm:"primaryKey" json:"id"`
	Slug string `gorm:"size:120;uniqueIndex;not null" json:"slug"`
	// Title is shown as the page heading.
	Title       string `gorm:"size:200;not null" json:"title"`
	Description string `gorm:"size:500" json:"description"`
	FooterText  string `gorm:"size:500" json:"footer_text"`
	// Theme: light | dark | system
	Theme string `gorm:"size:20;not null;default:system" json:"theme"`
	// IsPublic=false hides the page from anonymous visitors: it can still be
	// reached by authenticated administrators (preview mode).
	IsPublic   bool `gorm:"not null;default:true" json:"is_public"`
	ShowUptime bool `gorm:"not null;default:true" json:"show_uptime"`
	ShowCharts bool `gorm:"not null;default:true" json:"show_charts"`
	ShowTags   bool `gorm:"not null;default:false" json:"show_tags"`
	// CustomCSS is injected in the public page (advanced users only).
	CustomCSS string `gorm:"type:text" json:"custom_css"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`

	// Runtime fields -------------------------------------------------------
	Monitors      []*Monitor        `gorm:"-" json:"monitors,omitempty"`
	Groups        []StatusPageGroup `gorm:"-" json:"groups,omitempty"`
	ItemCount     int               `gorm:"-" json:"monitors_count"`
	GroupCount    int               `gorm:"-" json:"groups_count"`
	OverallStatus AggregateStatus   `gorm:"-" json:"overall_status"`
	UpMonitors    int               `gorm:"-" json:"up_monitors"`
	DownMonitors  int               `gorm:"-" json:"down_monitors"`
}

// StatusPageMonitor links a monitor to a status page, optionally renaming it
// and grouping it with other monitors.
type StatusPageMonitor struct {
	ID           uint `gorm:"primaryKey" json:"id"`
	StatusPageID uint `gorm:"not null;uniqueIndex:idx_status_page_monitor,priority:1" json:"status_page_id"`
	MonitorID    uint `gorm:"not null;uniqueIndex:idx_status_page_monitor,priority:2" json:"monitor_id"`

	DisplayName string `gorm:"size:200" json:"display_name"`
	GroupName   string `gorm:"size:120" json:"group_name"`
	SortOrder   int    `gorm:"not null;default:0" json:"sort_order"`
	ShowUptime  bool   `gorm:"not null;default:true" json:"show_uptime"`
	ShowChart   bool   `gorm:"not null;default:true" json:"show_chart"`
}

// TableName keeps the join table name stable.
func (StatusPageMonitor) TableName() string { return "status_page_monitors" }

// StatusPageGroupLink adds a monitor group to a status page: every monitor of
// the group is rendered on the page, so adding a monitor to the group publishes
// it everywhere the group is included.
//
// The table is `status_page_groups`; the type is called Link to keep the name
// StatusPageGroup for the payload above (the rendered section).
type StatusPageGroupLink struct {
	ID           uint `gorm:"primaryKey" json:"id"`
	StatusPageID uint `gorm:"not null;uniqueIndex:idx_status_page_group,priority:1" json:"status_page_id"`
	GroupID      uint `gorm:"not null;uniqueIndex:idx_status_page_group,priority:2" json:"group_id"`
	// DisplayName overrides the group name on this page (optional).
	DisplayName string `gorm:"size:150" json:"display_name"`
	SortOrder   int    `gorm:"not null;default:0" json:"sort_order"`

	// Runtime fields: the group name, filled by the service.
	GroupName string `gorm:"-" json:"group_name,omitempty"`
}

// TableName keeps the join table name stable.
func (StatusPageGroupLink) TableName() string { return "status_page_groups" }

// StatusPageGroup is the aggregated payload returned by the public endpoint,
// already grouped and ordered for rendering.
type StatusPageGroup struct {
	Name     string     `json:"name"`
	Monitors []*Monitor `json:"monitors"`
}
