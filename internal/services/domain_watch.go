package services

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/ivancarlosti/up/internal/models"
)

// Evaluate decides whether a domain notification must be sent and, when it must,
// delivers it at most once (the thresholds are recorded as alerted and the daily
// reminder is pinned to the day bucket).
//
// It mirrors CertificateService.Evaluate exactly: same cadence, same memory, same
// cluster election. Rows without a usable date (unsupported TLD, failed lookup)
// are silent on purpose — there is nothing to age, and the UI is where the
// operator fixes it.
func (s *DomainService) Evaluate(ctx context.Context, row *models.MonitorDomain, monitor *models.Monitor, now time.Time) (models.NotificationEvent, bool) {
	if row == nil || monitor == nil {
		return "", false
	}
	if row.Status != string(models.DomainStatusOK) || row.ExpiresAt.IsZero() {
		return "", false
	}
	warn := domainWarnDays(monitor)
	daysLeft := models.DaysLeft(row.ExpiresAt, now)
	expired := row.ExpiresAt.Before(now)
	day := now.UTC().Unix() / 86400

	plan := planDomainAlerts(daysLeft, expired, warn, row.NotifiedThresholds(), row.LastNotifiedDay, day)
	if plan.Event == "" {
		return "", false
	}

	// The operator may want the badge without the inbox traffic.
	if !monitor.DomainNotify || s.notifications == nil {
		return "", false
	}

	if s.cluster != nil {
		allowed, err := s.cluster.ClaimExpiryEvent(ctx, monitor.ID, plan.Event, day)
		if err != nil {
			s.log.Error("could not claim the domain notification",
				"monitor_id", monitor.ID, "event", plan.Event, "error", err)
			return "", false
		}
		if !allowed {
			return "", false
		}
	}

	status := models.AggregateDegraded
	if plan.Event == models.EventDomainExpired {
		status = models.AggregateDown
	}
	detail := domainMessage(row, daysLeft, expired, plan)
	logs := s.notifications.Dispatch(ctx, monitor, plan.Event, status, detail, 0, s.cfg.NodeID)
	s.log.Info("domain notification processed",
		"monitor_id", monitor.ID, "monitor", monitor.Name, "event", plan.Event,
		"domain", row.Domain, "days_left", daysLeft, "thresholds", plan.Thresholds,
		"daily", plan.Daily, "channels", len(logs))

	// The memory is written only after a decision, so a channel outage does not
	// silence the next reminder.
	s.markNotified(ctx, row, plan)
	return plan.Event, true
}

// markNotified records the thresholds and the day of the last reminder.
func (s *DomainService) markNotified(ctx context.Context, row *models.MonitorDomain, plan ExpiryPlan) {
	merged := append([]int{}, row.NotifiedThresholds()...)
	merged = append(merged, plan.Mark...)
	updates := map[string]any{
		"last_notified_day": row.LastNotifiedDay,
	}
	if len(plan.Mark) > 0 {
		label := thresholdsLabel(merged)
		updates["notified_days"] = label
		row.NotifiedDays = label
	}
	if plan.Daily || len(plan.Mark) > 0 {
		day := time.Now().UTC().Unix() / 86400
		updates["last_notified_day"] = day
		row.LastNotifiedDay = day
	}
	if err := s.db.WithContext(ctx).Model(&models.MonitorDomain{}).
		Where("monitor_id = ?", row.MonitorID).Updates(updates).Error; err != nil {
		s.log.Error("could not store the domain notification state", "monitor_id", row.MonitorID, "error", err)
	}
}

// domainMessage renders the human readable detail of the notification.
func domainMessage(row *models.MonitorDomain, daysLeft int, expired bool, plan ExpiryPlan) string {
	domain := strings.TrimSpace(row.Domain)
	suffix := ""
	if registrar := strings.TrimSpace(row.Registrar); registrar != "" {
		suffix = fmt.Sprintf(" (registrar: %s)", registrar)
	}
	until := row.ExpiresAt.UTC().Format("2006-01-02")
	if expired {
		return fmt.Sprintf("domain %s EXPIRED on %s%s", domain, until, suffix)
	}
	prefix := "domain " + domain
	if plan.Daily {
		prefix = "domain " + domain + " still"
	}
	return fmt.Sprintf("%s expires in %d days, on %s%s", prefix, daysLeft, until, suffix)
}

// Refresh ages the stored domains and sends the reminders that are due, without
// opening a socket. It runs at boot (and after a settings change) so a date
// entered while the process was down is evaluated immediately.
func (s *DomainService) Refresh(ctx context.Context) error {
	now := time.Now().UTC()
	// The operator's manual dates come first: they need no lookup, so a date
	// typed while the process was down (or one that was just typed) is applied
	// instead of waiting for this pass to age a row that does not exist yet.
	if _, err := s.applyManual(ctx, now, ""); err != nil {
		return err
	}
	rows, monitors, err := s.All(ctx)
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		s.log.Debug("domain watcher: nothing to watch")
		return nil
	}
	byID := map[uint]*models.Monitor{}
	for _, monitor := range monitors {
		byID[monitor.ID] = monitor
	}
	sent := 0
	evaluated := 0
	for i := range rows {
		row := &rows[i]
		monitor, ok := byID[row.MonitorID]
		if !ok || !monitor.DomainWatch || !monitor.Active {
			continue
		}
		if row.Status == string(models.DomainStatusOK) && !row.ExpiresAt.IsZero() {
			daysLeft := models.DaysLeft(row.ExpiresAt, now)
			if daysLeft != row.DaysLeft {
				row.DaysLeft = daysLeft
				if err := s.db.WithContext(ctx).Model(&models.MonitorDomain{}).
					Where("monitor_id = ?", row.MonitorID).
					Update("days_left", daysLeft).Error; err != nil {
					s.log.Warn("could not refresh the remaining validity", "monitor_id", row.MonitorID, "error", err)
				}
			}
		}
		evaluated++
		if event, ok := s.Evaluate(ctx, row, monitor, now); ok {
			sent++
			s.publish("monitor.domain", map[string]any{
				"monitor_id": row.MonitorID,
				"event":      event,
				"domain":     row.Domain,
				"days_left":  row.DaysLeft,
				"expires_at": row.ExpiresAt,
			})
		}
	}
	s.log.Info("domain watcher finished", "domains", evaluated, "notifications", sent)
	return nil
}
