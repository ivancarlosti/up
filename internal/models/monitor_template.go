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
	ID   uint   `gorm:"primaryKey" json:"id"`
	UUID string `gorm:"size:36;uniqueIndex;not null" json:"uuid"`
	// OriginNodeID and Revision complete the sync identity of the row (the UUID
	// is the global id; see docs/clustering-modes.md).
	OriginNodeID string      `gorm:"size:64" json:"origin_node_id"`
	Revision     int64       `gorm:"not null;default:1" json:"revision"`
	Name         string      `gorm:"size:150;not null;uniqueIndex" json:"name"`
	Description  string      `gorm:"size:500" json:"description"`
	Type         MonitorType `gorm:"size:20;not null" json:"type"`
	// Config carries the probe options of the blueprint and NEVER its
	// authentication: `auth_type`, `basic_user`, `basic_pass` and `bearer_token`
	// belong to the monitor (two monitors can follow one template with different
	// credentials), so Normalize strips them on every write, AfterFind strips
	// them on every read and the apply path keeps the credentials of the monitor
	// it writes. See models.MonitorConfig.WithoutAuth.
	Config   MonitorConfig    `gorm:"serializer:json;type:json" json:"config"`
	Defaults TemplateDefaults `gorm:"serializer:json;type:json" json:"defaults"`
	// Propagate pushes the defaults to every monitor that follows this template
	// each time it is edited (see MonitorTemplateService.Propagate).
	Propagate bool `gorm:"not null;default:true" json:"propagate"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// BeforeCreate fills the UUID (globally identifiable rows, like the groups).
func (t *MonitorTemplate) BeforeCreate(tx *gorm.DB) error {
	if t.UUID == "" {
		t.UUID = uuid.NewString()
	}
	if t.Revision == 0 {
		t.Revision = 1
	}
	return nil
}

// AfterFind makes the invariant hold on the READ path as well: a credential can
// only be absent from a template, so a row written before this rule existed (or
// by a peer running an older release) can never hand one back to the UI, to the
// apply path or to a propagation.
func (t *MonitorTemplate) AfterFind(tx *gorm.DB) error {
	t.Config = t.Config.WithoutAuth()
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
	// RunOnNodes is the subset used by run_on=some, comma separated.
	RunOnNodes string `json:"run_on_nodes"`
	// Tags is kept for compatibility with templates stored before the link
	// feature: the tags of a monitor belong to the operator, a template never sets
	// them (two monitors can follow one template with different tags).
	Tags        string `json:"tags"`
	Description string `json:"description"`
	// Active is a pointer so an omitted value keeps the monitor default (true).
	Active *bool `json:"active,omitempty"`
	// Certificate watching applied to the monitors created from the template (or
	// overwritten by a bulk edit that selects these fields).
	CertWatch    bool   `json:"cert_watch"`
	CertNotify   bool   `json:"cert_notify"`
	CertWarnDays string `json:"cert_warn_days"`
	// Domain expiration watching applied to the monitors created from the
	// template. DomainExpiresAt is intentionally NOT part of a template: a manual
	// date belongs to one specific domain, never to a blueprint.
	DomainWatch    bool   `json:"domain_watch"`
	DomainNotify   bool   `json:"domain_notify"`
	DomainWarnDays string `json:"domain_warn_days"`
	// NotificationIDs and GroupIDs are the links applied to the new monitors.
	NotificationIDs []uint `json:"notification_ids"`
	// GroupIDs is kept for compatibility for the same reason: the groups of a
	// monitor are the operator's choice.
	GroupIDs []uint `json:"group_ids"`
}

// TemplateDefaultFields lists the fields that can be applied to existing
// monitors (the API uses it to validate the request and the UI to build the
// checklist).
func TemplateDefaultFields() []string {
	return []string{
		"description", "interval_seconds", "retries", "retries_interval_seconds",
		"timeout_seconds", "resend_interval_seconds", "run_on", "run_on_nodes", "node_id",
		"active", "notification_ids", "config",
		"cert_watch", "cert_notify", "cert_warn_days",
		"domain_watch", "domain_notify", "domain_warn_days",
	}
}

// Normalize fills the defaults of a template.
//
// It also enforces the one rule a template cannot break: authentication belongs
// to the monitor, so the configuration of a template is stored without
// credentials whatever the request carried (the UI does not offer them any more
// and the API keeps accepting the field for compatibility). Two monitors can
// therefore follow the same template with different credentials, and editing the
// template never touches what the operator typed on a monitor.
func (t *MonitorTemplate) Normalize() {
	t.Name = strings.TrimSpace(t.Name)
	t.Description = strings.TrimSpace(t.Description)
	if t.Defaults.IntervalSeconds <= 0 {
		t.Defaults.IntervalSeconds = 60
	}
	if t.Defaults.TimeoutSeconds <= 0 {
		t.Defaults.TimeoutSeconds = 10
	}
	if t.Defaults.RetriesIntervalSeconds <= 0 {
		t.Defaults.RetriesIntervalSeconds = 60
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
	t.Config = t.Config.WithoutAuth()
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
	// The run_on rules mirror the monitor ones (MonitorService.Validate): a
	// template must not describe a monitor the API would refuse to create.
	switch t.Defaults.RunOn {
	case "all", "primary":
		t.Defaults.NodeID = ""
		t.Defaults.RunOnNodes = ""
	case "node":
		if strings.TrimSpace(t.Defaults.NodeID) == "" {
			return "node_id is required when run_on=node"
		}
		t.Defaults.RunOnNodes = ""
	case "some":
		t.Defaults.NodeID = ""
		t.Defaults.RunOnNodes = NormalizeRunOnNodes(t.Defaults.RunOnNodes)
		if t.Defaults.RunOnNodes == "" {
			return "run_on_nodes is required when run_on=some"
		}
	default:
		return "run_on must be all, primary, node or some"
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
	if t.Defaults.Retries > 0 && (t.Defaults.RetriesIntervalSeconds < 1 || t.Defaults.RetriesIntervalSeconds > 3600) {
		return "retries_interval_seconds must be between 1 and 3600"
	}
	// The switches must describe a monitor that can actually exist: the monitor
	// validation rejects a certificate watch on a type that cannot read one and a
	// domain watch on a type without a registrable domain, so a template that
	// carried them would only ever produce rows that cannot be created.
	if t.Defaults.CertWatch && !t.Type.SupportsCertificate() {
		return "cert_watch is only available for http, keyword and ssl monitors"
	}
	if t.Defaults.CertNotify && !t.Defaults.CertWatch {
		return "cert_notify requires cert_watch"
	}
	if _, err := ParseCertWarnDays(t.Defaults.CertWarnDays); err != nil {
		return "cert_warn_days must be a list of days before expiry: " + err.Error()
	}
	if t.Defaults.DomainWatch && !t.Type.SupportsDomainWatch() {
		return "domain_watch is not available for " + string(t.Type) + " monitors"
	}
	if t.Defaults.DomainNotify && !t.Defaults.DomainWatch {
		return "domain_notify requires domain_watch"
	}
	if _, err := ParseCertWarnDays(t.Defaults.DomainWarnDays); err != nil {
		return "domain_warn_days must be a list of days before expiry: " + err.Error()
	}
	return ""
}

// targetField returns the configuration field that carries the target of a
// monitor of this type (the field the bulk loader fills per row).
func (t MonitorType) TargetField() string {
	switch t {
	case MonitorTypeHTTP, MonitorTypeKeyword:
		return "url"
	case MonitorTypeTCP, MonitorTypeSSL:
		return "host"
	case MonitorTypeDNS:
		return "hostname"
	}
	return "url"
}
