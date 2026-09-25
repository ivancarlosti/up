package services

import (
	"errors"
	"fmt"
	"strings"

	"gorm.io/gorm"

	"github.com/ivancarlosti/up/internal/i18n"
	"github.com/ivancarlosti/up/internal/models"
)

// Validate checks the scheduler related fields and the type specific config.
func (s *MonitorService) Validate(monitor *models.Monitor) error {
	if strings.TrimSpace(monitor.Name) == "" {
		return ErrBadRequest(i18n.CodeValidation, "name is required")
	}
	if len(monitor.Name) > 200 {
		return ErrBadRequest(i18n.CodeValidation, "name must be at most 200 characters")
	}
	if !monitor.Type.Valid() {
		return ErrBadRequest(i18n.CodeMonitorTypeInvalid,
			fmt.Sprintf("type must be one of %v", models.AllMonitorTypes()))
	}
	if monitor.IntervalSeconds < 5 || monitor.IntervalSeconds > 86400 {
		return ErrBadRequest(i18n.CodeValidation, "interval_seconds must be between 5 and 86400")
	}
	if monitor.Retries < 0 || monitor.Retries > 50 {
		return ErrBadRequest(i18n.CodeValidation, "retries must be between 0 and 50")
	}
	if monitor.Retries > 0 && (monitor.RetriesIntervalSeconds < 1 || monitor.RetriesIntervalSeconds > 3600) {
		return ErrBadRequest(i18n.CodeValidation, "retries_interval_seconds must be between 1 and 3600")
	}
	if monitor.TimeoutSeconds < 1 || monitor.TimeoutSeconds > 300 {
		return ErrBadRequest(i18n.CodeValidation, "timeout_seconds must be between 1 and 300")
	}
	if monitor.ResendIntervalSeconds < 0 {
		return ErrBadRequest(i18n.CodeValidation, "resend_interval_seconds must not be negative")
	}
	switch monitor.RunOn {
	case "", "all":
		monitor.RunOn = "all"
		monitor.NodeID = ""
		monitor.RunOnNodes = ""
	case "primary":
		monitor.NodeID = ""
		monitor.RunOnNodes = ""
	case "node":
		if strings.TrimSpace(monitor.NodeID) == "" {
			return ErrBadRequest(i18n.CodeValidation, "node_id is required when run_on=node")
		}
		monitor.RunOnNodes = ""
	case "some":
		monitor.NodeID = ""
		monitor.RunOnNodes = models.NormalizeRunOnNodes(monitor.RunOnNodes)
		if monitor.RunOnNodes == "" {
			return ErrBadRequest(i18n.CodeValidation, "run_on_nodes is required when run_on=some")
		}
	default:
		return ErrBadRequest(i18n.CodeValidation, "run_on must be all, primary, node or some")
	}

	monitor.Config.Normalize(monitor.Type)
	if problem := monitor.Config.Validate(monitor.Type); problem != "" {
		return ErrBadRequest(i18n.CodeMonitorConfig, problem)
	}

	// Certificate watching: the thresholds are a free form list, the switches
	// must be coherent (notifying without watching would be a silent no-op).
	if _, err := models.ParseCertWarnDays(monitor.CertWarnDays); err != nil {
		return ErrBadRequest(i18n.CodeMonitorCert, "cert_warn_days must be a list of days before expiry: "+err.Error())
	}
	if monitor.CertWatch && !monitor.Type.SupportsCertificate() {
		return ErrBadRequest(i18n.CodeMonitorCert,
			"cert_watch is only available for http, keyword and ssl monitors")
	}
	if monitor.CertNotify && !monitor.CertWatch {
		return ErrBadRequest(i18n.CodeMonitorCert, "cert_notify requires cert_watch")
	}

	// Domain expiration watching: the same rules as the certificate watcher (the
	// switches must be coherent, the thresholds are a free form list) plus the
	// manual date, which is the fallback for the TLDs that publish no date at all.
	if _, err := models.ParseCertWarnDays(monitor.DomainWarnDays); err != nil {
		return ErrBadRequest(i18n.CodeMonitorDomain, "domain_warn_days must be a list of days before expiry: "+err.Error())
	}
	if monitor.DomainWatch && !monitor.Type.SupportsDomainWatch() {
		return ErrBadRequest(i18n.CodeMonitorDomain,
			"domain_watch is not available for "+string(monitor.Type)+" monitors")
	}
	if monitor.DomainNotify && !monitor.DomainWatch {
		return ErrBadRequest(i18n.CodeMonitorDomain, "domain_notify requires domain_watch")
	}
	if monitor.DomainExpiresAt != nil && monitor.DomainExpiresAt.IsZero() {
		monitor.DomainExpiresAt = nil
	}
	if !monitor.DomainWatch {
		// A monitor that does not watch its domain keeps no stale notification
		// configuration. The manual date is NOT part of this: it belongs to the
		// DOMAIN (the registry registration), so the watch switch must never
		// erase a truth the operator typed (turning the switch off used to take
		// the date away from every sibling of the domain).
		monitor.DomainNotify = false
		monitor.DomainWarnDays = ""
	}

	// The template link: the template must exist and describe the same kind of
	// probe (a template never reshapes a monitor of another type). A link that
	// points nowhere — the template was deleted on another node — is cleared
	// instead of blocking the edit, so a dangling reference heals itself.
	if uuid := strings.TrimSpace(monitor.TemplateUUID); uuid != "" {
		var template models.MonitorTemplate
		switch err := s.db.Where("uuid = ?", uuid).First(&template).Error; {
		case errors.Is(err, gorm.ErrRecordNotFound):
			monitor.TemplateUUID = ""
		case err != nil:
			return ErrInternal(err)
		case template.Type != monitor.Type:
			return ErrBadRequest(i18n.CodeMonitorTemplateInvalid,
				"the template type must match the monitor type")
		}
	}
	return nil
}
