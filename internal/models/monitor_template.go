package models

import (
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// MonitorTemplate is a reusable monitor blueprint: the probe type and its
// configuration plus the defaults applied to every monitor created or edited
// from it. It is what turns "the same monitor for 40 endpoints" into a single
// definition (and the bulk endpoint into a one-liner).
//
// The target of the probe (the URL, the host:port, the hostname) is NOT part of
// the template: it is what the operator provides per monitor, either in the form
// or in the bulk text.
type MonitorTemplate struct {
	ID          uint             `gorm:"primaryKey" json:"id"`
	UUID        string           `gorm:"size:36;uniqueIndex;not null" json:"uuid"`
	Name        string           `gorm:"size:150;not null;uniqueIndex" json:"name"`
	Description string           `gorm:"size:500" json:"description"`
	Type        MonitorType      `gorm:"size:20;not null" json:"type"`
	Config      MonitorConfig    `gorm:"serializer:json;type:json" json:"config"`
	Defaults    TemplateDefaults `gorm:"serializer:json;type:json" json:"defaults"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// BeforeCreate fills the UUID (globally identifiable rows, like the groups).
func (t *MonitorTemplate) BeforeCreate(tx *gorm.DB) error {
	if t.UUID == "" {
		t.UUID = uuid.NewString()
	}
	return nil
}

// TemplateDefaults is the scheduling part of a template: the fields that are not
// part of the probe itself.
type TemplateDefaults struct {
	IntervalSeconds        int    `json:"interval_seconds"`
	Retries                int    `json:"retries"`
	RetriesIntervalSeconds int    `json:"retries_interval_seconds"`
	TimeoutSeconds         int    `json:"timeout_seconds"`
	ResendIntervalSeconds  int    `json:"resend_interval_seconds"`
	RunOn                  string `json:"run_on"`
	NodeID                 string `json:"node_id"`
	Tags                   string `json:"tags"`
	Description            string `json:"description"`
	// Active is a pointer so an omitted value keeps the monitor default (true).
	Active *bool `json:"active,omitempty"`
	// NotificationIDs and GroupIDs are the links applied to the new monitors.
	NotificationIDs []uint `json:"notification_ids"`
	GroupIDs        []uint `json:"group_ids"`
}

// TemplateDefaultFields lists the fields that can be applied to existing
// monitors (the API uses it to validate the request and the UI to build the
// checklist).
func TemplateDefaultFields() []string {
	return []string{
		"description", "interval_seconds", "retries", "retries_interval_seconds",
		"timeout_seconds", "resend_interval_seconds", "run_on", "node_id", "tags",
		"active", "notification_ids", "group_ids", "config",
	}
}

// Normalize fills the defaults of a template.
func (t *MonitorTemplate) Normalize() {
	t.Name = strings.TrimSpace(t.Name)
	t.Description = strings.TrimSpace(t.Description)
	if t.Defaults.IntervalSeconds <= 0 {
		t.Defaults.IntervalSeconds = 60
	}
	if t.Defaults.TimeoutSeconds <= 0 {
		t.Defaults.TimeoutSeconds = 10
	}
	if t.Defaults.RunOn == "" {
		t.Defaults.RunOn = "all"
	}
	if t.Defaults.NotificationIDs == nil {
		t.Defaults.NotificationIDs = []uint{}
	}
	if t.Defaults.GroupIDs == nil {
		t.Defaults.GroupIDs = []uint{}
	}
	t.Config.Normalize(t.Type)
}

// Validate checks the template (empty string means "valid").
func (t *MonitorTemplate) Validate() string {
	if t.Name == "" {
		return "name is required"
	}
	if !t.Type.Valid() {
		types := make([]string, 0, len(AllMonitorTypes()))
		for _, candidate := range AllMonitorTypes() {
			types = append(types, string(candidate))
		}
		return "type must be " + strings.Join(types, ", ")
	}
	if t.Defaults.RunOn != "all" && t.Defaults.RunOn != "primary" && t.Defaults.RunOn != "node" {
		return "run_on must be all, primary or node"
	}
	if t.Defaults.IntervalSeconds < 5 {
		return "interval_seconds must be at least 5"
	}
	if t.Defaults.TimeoutSeconds < 1 || t.Defaults.TimeoutSeconds > 300 {
		return "timeout_seconds must be between 1 and 300"
	}
	if t.Defaults.Retries < 0 || t.Defaults.Retries > 50 {
		return "retries must be between 0 and 50"
	}
	return ""
}

// targetField returns the configuration field that carries the target of a
// monitor of this type (the field the bulk loader fills per row).
func (t MonitorType) TargetField() string {
	switch t {
	case MonitorTypeHTTP, MonitorTypeKeyword:
		return "url"
	case MonitorTypeTCP:
		return "host"
	case MonitorTypeDNS:
		return "hostname"
	}
	return "url"
}
