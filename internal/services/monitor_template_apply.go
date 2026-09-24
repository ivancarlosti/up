package services

import (
	"context"
	"errors"
	"strconv"
	"strings"

	"github.com/ivancarlosti/up/internal/i18n"
	"github.com/ivancarlosti/up/internal/models"
)

// FieldChange is one difference between a monitor and a template.
type FieldChange struct {
	Field string `json:"field"`
	From  string `json:"from"`
	To    string `json:"to"`
}

// ApplyResult reports what happened (or would happen) to one monitor.
type ApplyResult struct {
	MonitorID uint          `json:"monitor_id"`
	Name      string        `json:"name"`
	Changed   []FieldChange `json:"changed,omitempty"`
	Applied   bool          `json:"applied"`
	Error     string        `json:"error,omitempty"`
}

// ApplyOptions are the inputs of a bulk apply.
type ApplyOptions struct {
	TemplateID uint
	MonitorIDs []uint
	// Fields selects what to apply; empty means every default field.
	Fields []string
	// DryRun computes the diff without writing anything.
	DryRun bool
}

// Apply applies a template to a set of monitors (a bulk edit).
//
// A template is about HOW to probe, not about WHAT to probe: even when `config`
// is applied, the target of each monitor (its URL, host or hostname) is kept, so
// applying a template to 40 monitors never makes them all probe the same address.
func (s *MonitorTemplateService) Apply(ctx context.Context, opts ApplyOptions) ([]ApplyResult, error) {
	template, err := s.Get(ctx, opts.TemplateID)
	if err != nil {
		return nil, err
	}
	fields := opts.Fields
	if len(fields) == 0 {
		fields = models.TemplateDefaultFields()
	}
	known := map[string]bool{}
	for _, field := range models.TemplateDefaultFields() {
		known[field] = true
	}
	for _, field := range fields {
		if !known[field] {
			return nil, ErrBadRequest(i18n.CodeMonitorTemplateInvalid, "unknown field "+field)
		}
	}
	if len(opts.MonitorIDs) == 0 {
		return nil, ErrBadRequest(i18n.CodeMonitorTemplateInvalid, "monitor_ids is required")
	}
	if s.mono == nil {
		return nil, ErrInternal(errors.New("the monitor service is not wired into the template service"))
	}

	results := make([]ApplyResult, 0, len(opts.MonitorIDs))
	for _, id := range uniqueIDs(opts.MonitorIDs) {
		monitor, loadErr := s.mono.Get(ctx, id)
		if loadErr != nil {
			results = append(results, ApplyResult{MonitorID: id, Error: loadErr.Error()})
			continue
		}
		changes := planApply(monitor, template, fields)
		result := ApplyResult{MonitorID: id, Name: monitor.Name, Changed: changes}
		if opts.DryRun || len(changes) == 0 {
			results = append(results, result)
			continue
		}
		updated, notificationIDs, groupIDs := applyTemplate(monitor, template, fields)
		if updateErr := s.mono.Update(ctx, updated, notificationIDs, groupIDs); updateErr != nil {
			result.Error = updateErr.Error()
			results = append(results, result)
			continue
		}
		result.Applied = true
		results = append(results, result)
	}
	if !opts.DryRun {
		s.log.Info("monitor template applied",
			"template", template.ID, "monitors", len(opts.MonitorIDs), "fields", strings.Join(fields, ","))
	}
	return results, nil
}

// planApply lists the differences of the selected fields (pure: the dry run and
// the UI preview both render it).
func planApply(monitor *models.Monitor, template *models.MonitorTemplate, fields []string) []FieldChange {
	changes := []FieldChange{}
	for _, field := range fields {
		switch field {
		case "description":
			addChange(&changes, field, monitor.Description, template.Defaults.Description)
		case "interval_seconds":
			addChange(&changes, field, strconv.Itoa(monitor.IntervalSeconds), strconv.Itoa(template.Defaults.IntervalSeconds))
		case "retries":
			addChange(&changes, field, strconv.Itoa(monitor.Retries), strconv.Itoa(template.Defaults.Retries))
		case "retries_interval_seconds":
			addChange(&changes, field, strconv.Itoa(monitor.RetriesIntervalSeconds), strconv.Itoa(template.Defaults.RetriesIntervalSeconds))
		case "timeout_seconds":
			addChange(&changes, field, strconv.Itoa(monitor.TimeoutSeconds), strconv.Itoa(template.Defaults.TimeoutSeconds))
		case "resend_interval_seconds":
			addChange(&changes, field, strconv.Itoa(monitor.ResendIntervalSeconds), strconv.Itoa(template.Defaults.ResendIntervalSeconds))
		case "run_on":
			addChange(&changes, field, monitor.RunOn, template.Defaults.RunOn)
		case "run_on_nodes":
			addChange(&changes, field, monitor.RunOnNodes, template.Defaults.RunOnNodes)
		case "node_id":
			addChange(&changes, field, monitor.NodeID, template.Defaults.NodeID)
		case "tags":
			addChange(&changes, field, monitor.Tags, template.Defaults.Tags)
		case "active":
			if template.Defaults.Active != nil {
				addChange(&changes, field, strconv.FormatBool(monitor.Active), strconv.FormatBool(*template.Defaults.Active))
			}
		case "notification_ids":
			addChange(&changes, field, idsLabel(monitor.NotificationIDs), idsLabel(template.Defaults.NotificationIDs))
		case "group_ids":
			addChange(&changes, field, idsLabel(monitor.GroupIDs), idsLabel(template.Defaults.GroupIDs))
		case "config":
			changes = append(changes, configChanges(monitor, template)...)
		case "cert_watch":
			addChange(&changes, field, strconv.FormatBool(monitor.CertWatch), strconv.FormatBool(template.Defaults.CertWatch))
		case "cert_notify":
			addChange(&changes, field, strconv.FormatBool(monitor.CertNotify), strconv.FormatBool(template.Defaults.CertNotify))
		case "cert_warn_days":
			addChange(&changes, field, monitor.CertWarnDays, template.Defaults.CertWarnDays)
		}
	}
	return changes
}

// addChange appends the change only when the value really differs.
func addChange(changes *[]FieldChange, field, from, to string) {
	if from == to {
		return
	}
	*changes = append(*changes, FieldChange{Field: field, From: from, To: to})
}

// configChanges reports the probe options that differ. The target is never part
// of it: a template does not carry a target.
func configChanges(monitor *models.Monitor, template *models.MonitorTemplate) []FieldChange {
	changes := []FieldChange{}
	from, to := monitor.Config, template.Config
	addChange(&changes, "config.method", from.Method, to.Method)
	addChange(&changes, "config.encoding", from.Encoding, to.Encoding)
	addChange(&changes, "config.accepted_status_codes", from.AcceptedStatusCodes, to.AcceptedStatusCodes)
	addChange(&changes, "config.ignore_tls", strconv.FormatBool(from.IgnoreTLS), strconv.FormatBool(to.IgnoreTLS))
	addChange(&changes, "config.max_redirects", strconv.Itoa(from.MaxRedirects), strconv.Itoa(to.MaxRedirects))
	addChange(&changes, "config.cache_buster", strconv.FormatBool(from.CacheBuster), strconv.FormatBool(to.CacheBuster))
	addChange(&changes, "config.keyword", from.Keyword, to.Keyword)
	addChange(&changes, "config.record_type", from.RecordType, to.RecordType)
	addChange(&changes, "config.resolver_server", from.ResolverServer, to.ResolverServer)
	if len(from.Headers) != len(to.Headers) {
		addChange(&changes, "config.headers", strconv.Itoa(len(from.Headers)), strconv.Itoa(len(to.Headers)))
	}
	return changes
}

// applyTemplate returns a copy of the monitor with the selected fields taken from
// the template, plus the notification and group ids to write (nil = keep).
func applyTemplate(monitor *models.Monitor, template *models.MonitorTemplate, fields []string) (*models.Monitor, []uint, []uint) {
	updated := *monitor
	var notificationIDs, groupIDs []uint
	for _, field := range fields {
		switch field {
		case "description":
			updated.Description = template.Defaults.Description
		case "interval_seconds":
			updated.IntervalSeconds = template.Defaults.IntervalSeconds
		case "retries":
			updated.Retries = template.Defaults.Retries
		case "retries_interval_seconds":
			updated.RetriesIntervalSeconds = template.Defaults.RetriesIntervalSeconds
		case "timeout_seconds":
			updated.TimeoutSeconds = template.Defaults.TimeoutSeconds
		case "resend_interval_seconds":
			updated.ResendIntervalSeconds = template.Defaults.ResendIntervalSeconds
		case "run_on":
			updated.RunOn = template.Defaults.RunOn
		case "node_id":
			updated.NodeID = template.Defaults.NodeID
		case "tags":
			updated.Tags = template.Defaults.Tags
		case "active":
			if template.Defaults.Active != nil {
				updated.Active = *template.Defaults.Active
			}
		case "notification_ids":
			notificationIDs = template.Defaults.NotificationIDs
			if notificationIDs == nil {
				notificationIDs = []uint{}
			}
		case "group_ids":
			groupIDs = template.Defaults.GroupIDs
			if groupIDs == nil {
				groupIDs = []uint{}
			}
		case "config":
			updated.Config = withProbeTarget(template.Config, monitor.Config, monitor.Type)
		case "cert_watch":
			updated.CertWatch = template.Defaults.CertWatch
		case "cert_notify":
			updated.CertNotify = template.Defaults.CertNotify
		case "cert_warn_days":
			updated.CertWarnDays = template.Defaults.CertWarnDays
		}
	}
	return &updated, notificationIDs, groupIDs
}

// withProbeTarget copies the template configuration but keeps the target fields
// of the monitor (url, host, port, hostname), so a bulk apply never repoints a
// monitor at another address.
func withProbeTarget(config models.MonitorConfig, target models.MonitorConfig, monitorType models.MonitorType) models.MonitorConfig {
	switch monitorType {
	case models.MonitorTypeHTTP, models.MonitorTypeKeyword:
		config.URL = target.URL
	case models.MonitorTypeTCP:
		config.Host = target.Host
		config.Port = target.Port
	case models.MonitorTypeDNS:
		config.Hostname = target.Hostname
	}
	return config
}

// idsLabel renders an id list for the change log ("6,7").
func idsLabel(ids []uint) string {
	parts := make([]string, 0, len(ids))
	for _, id := range ids {
		parts = append(parts, strconv.FormatUint(uint64(id), 10))
	}
	return strings.Join(parts, ",")
}
