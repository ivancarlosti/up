package services

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/ivancarlosti/up/internal/i18n"
	"github.com/ivancarlosti/up/internal/models"
)

// BulkCreate creates one monitor per parsed row using a template.
//
// Every row is independent: a bad line reports its own error and the rest are
// still created. A row whose name or target already exists is reported as a
// duplicate instead of creating a second monitor for the same endpoint, and
// `DryRun` validates everything without writing.
func (s *MonitorService) BulkCreate(ctx context.Context, opts BulkOptions, templates *MonitorTemplateService) (*BulkReport, []uint, error) {
	if templates == nil {
		return nil, nil, ErrInternal(fmt.Errorf("the template service is not wired into the bulk importer"))
	}
	template, err := templates.Get(ctx, opts.TemplateID)
	if err != nil {
		return nil, nil, err
	}
	rows := ParseBulkText(opts.Text)
	if len(rows) == 0 {
		return nil, nil, ErrBadRequest(i18n.CodeMonitorBulkInvalid,
			"no row found: paste one monitor per line as name,target")
	}
	if len(rows) > maxBulkRows {
		return nil, nil, ErrBadRequest(i18n.CodeMonitorBulkInvalid,
			fmt.Sprintf("too many rows: %d, the limit is %d", len(rows), maxBulkRows))
	}

	existingNames, existingTargets, err := s.existingMonitorKeys(ctx)
	if err != nil {
		return nil, nil, err
	}

	report := &BulkReport{Parsed: len(rows), Rows: make([]BulkRowResult, 0, len(rows))}
	createdIDs := []uint{}
	for _, row := range rows {
		result := BulkRowResult{Line: row.Line, Name: row.Name}
		monitor, buildErr := monitorFromBulkRow(template, row, opts)
		if buildErr != nil {
			result.Status = "invalid"
			result.Error = buildErr.Error()
			report.Failed++
			report.Rows = append(report.Rows, result)
			continue
		}
		// The keys are compared against the database AND against the rows seen
		// earlier in this same paste: two identical lines must not create two
		// monitors for the same endpoint.
		nameKey := strings.ToLower(monitor.Name)
		targetKey := monitorTargetKey(monitor)
		if existingNames[nameKey] || existingTargets[targetKey] {
			result.Status = "duplicate"
			result.Error = "a monitor with the same name or target already exists"
			report.Skipped++
			report.Rows = append(report.Rows, result)
			continue
		}
		if validateErr := s.Validate(monitor); validateErr != nil {
			result.Status = "invalid"
			result.Error = validateErr.Error()
			report.Failed++
			report.Rows = append(report.Rows, result)
			continue
		}
		if opts.DryRun {
			result.Status = "dry_run"
			report.DryRun++
			existingNames[nameKey] = true
			existingTargets[targetKey] = true
			report.Rows = append(report.Rows, result)
			continue
		}
		notificationIDs := template.Defaults.NotificationIDs
		groupIDs := append(append([]uint{}, template.Defaults.GroupIDs...), opts.GroupIDs...)
		if createErr := s.Create(ctx, monitor, notificationIDs, groupIDs); createErr != nil {
			result.Status = "failed"
			result.Error = createErr.Error()
			report.Failed++
			report.Rows = append(report.Rows, result)
			continue
		}
		result.Status = "created"
		result.MonitorID = monitor.ID
		report.Created++
		createdIDs = append(createdIDs, monitor.ID)
		existingNames[nameKey] = true
		existingTargets[targetKey] = true
		report.Rows = append(report.Rows, result)
	}
	if !opts.DryRun {
		s.log.Info("bulk monitor creation finished",
			"template", template.ID, "parsed", report.Parsed, "created", report.Created,
			"duplicates", report.Skipped, "failed", report.Failed)
	}
	return report, createdIDs, nil
}

// monitorFromBulkRow builds the monitor of one row: the template provides the type
// and the options, the row provides the name and the target.
func monitorFromBulkRow(template *models.MonitorTemplate, row BulkRow, opts BulkOptions) (*models.Monitor, error) {
	if row.Error != "" {
		return nil, fmt.Errorf("%s", row.Error)
	}
	monitorType := template.Type
	if row.Type != "" {
		monitorType = models.MonitorType(row.Type)
	}
	config := template.Config
	target := strings.TrimSpace(row.Target)

	switch monitorType {
	case models.MonitorTypeHTTP, models.MonitorTypeKeyword:
		if !strings.HasPrefix(target, "http://") && !strings.HasPrefix(target, "https://") {
			return nil, fmt.Errorf("the url must start with http:// or https:// (got %q)", target)
		}
		config.URL = target
		if monitorType == models.MonitorTypeKeyword {
			if row.Keyword != "" {
				config.Keyword = row.Keyword
			}
			if strings.TrimSpace(config.Keyword) == "" {
				return nil, fmt.Errorf("a keyword monitor needs the keyword column")
			}
		}
	case models.MonitorTypeTCP:
		host, port, splitErr := splitHostPort(target, template.Config.Port)
		if splitErr != nil {
			return nil, splitErr
		}
		config.Host = host
		config.Port = port
	case models.MonitorTypeDNS:
		config.Hostname = target
	}

	monitor := &models.Monitor{
		Name:                   row.Name,
		Type:                   monitorType,
		Active:                 true,
		Description:            template.Defaults.Description,
		IntervalSeconds:        template.Defaults.IntervalSeconds,
		Retries:                template.Defaults.Retries,
		RetriesIntervalSeconds: template.Defaults.RetriesIntervalSeconds,
		TimeoutSeconds:         template.Defaults.TimeoutSeconds,
		ResendIntervalSeconds:  template.Defaults.ResendIntervalSeconds,
		RunOn:                  template.Defaults.RunOn,
		RunOnNodes:             template.Defaults.RunOnNodes,
		NodeID:                 template.Defaults.NodeID,
		Tags:                   template.Defaults.Tags,
		CertWatch:              template.Defaults.CertWatch,
		CertNotify:             template.Defaults.CertNotify,
		CertWarnDays:           template.Defaults.CertWarnDays,
		Config:                 config,
	}
	if template.Defaults.Active != nil {
		monitor.Active = *template.Defaults.Active
	}
	if opts.Active != nil {
		monitor.Active = *opts.Active
	}
	if row.Tags != "" {
		monitor.Tags = row.Tags
	}
	if row.Interval != "" {
		seconds, parseErr := strconv.Atoi(row.Interval)
		if parseErr != nil || seconds < 5 {
			return nil, fmt.Errorf("interval_seconds must be a number >= 5 (got %q)", row.Interval)
		}
		monitor.IntervalSeconds = seconds
	}
	monitor.Config.Normalize(monitorType)
	return monitor, nil
}

// splitHostPort reads a `host` or `host:port` target.
func splitHostPort(target string, fallbackPort int) (string, int, error) {
	host, portText := target, ""
	if index := strings.LastIndex(target, ":"); index > 0 {
		host, portText = target[:index], target[index+1:]
	}
	host = strings.TrimSpace(host)
	if host == "" {
		return "", 0, fmt.Errorf("the host is required")
	}
	port := fallbackPort
	if strings.TrimSpace(portText) != "" {
		parsed, err := strconv.Atoi(strings.TrimSpace(portText))
		if err != nil || parsed < 1 || parsed > 65535 {
			return "", 0, fmt.Errorf("invalid port %q", portText)
		}
		port = parsed
	}
	if port < 1 || port > 65535 {
		return "", 0, fmt.Errorf("the template does not define a port and %q does not include one", target)
	}
	return host, port, nil
}

// existingMonitorKeys loads the names and the targets already in use.
func (s *MonitorService) existingMonitorKeys(ctx context.Context) (map[string]bool, map[string]bool, error) {
	var monitors []*models.Monitor
	if err := s.db.WithContext(ctx).Find(&monitors).Error; err != nil {
		return nil, nil, ErrInternal(fmt.Errorf("loading the monitors: %w", err))
	}
	names := map[string]bool{}
	targets := map[string]bool{}
	for _, monitor := range monitors {
		names[strings.ToLower(monitor.Name)] = true
		targets[monitorTargetKey(monitor)] = true
	}
	return names, targets, nil
}

// monitorTargetKey is what makes two monitors "the same endpoint" for the bulk
// importer: the URL, the host:port or the hostname.
func monitorTargetKey(monitor *models.Monitor) string {
	switch monitor.Type {
	case models.MonitorTypeHTTP, models.MonitorTypeKeyword:
		return strings.ToLower(strings.TrimSpace(monitor.Config.URL))
	case models.MonitorTypeTCP:
		return fmt.Sprintf("%s:%d", strings.ToLower(strings.TrimSpace(monitor.Config.Host)), monitor.Config.Port)
	case models.MonitorTypeDNS:
		return strings.ToLower(strings.TrimSpace(monitor.Config.Hostname))
	}
	return ""
}
