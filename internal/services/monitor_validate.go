package services

import (
	"fmt"
	"strings"

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
	case "primary":
		monitor.NodeID = ""
	case "node":
		if strings.TrimSpace(monitor.NodeID) == "" {
			return ErrBadRequest(i18n.CodeValidation, "node_id is required when run_on=node")
		}
	default:
		return ErrBadRequest(i18n.CodeValidation, "run_on must be all, primary or node")
	}

	monitor.Config.Normalize(monitor.Type)
	if problem := monitor.Config.Validate(monitor.Type); problem != "" {
		return ErrBadRequest(i18n.CodeMonitorConfig, problem)
	}
	return nil
}
