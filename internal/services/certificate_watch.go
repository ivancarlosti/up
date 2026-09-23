package services

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/ivancarlosti/up/internal/models"
)

// Evaluate decides whether a certificate notification must be sent and, when it
// must, delivers it at most once (the thresholds are recorded as alerted and the
// daily reminder is pinned to the day bucket).
//
// It is the entry point of both the watcher job and the moment a probe finds a
// certificate for the first time, so a monitor added against an already expired
// certificate tells the operator immediately instead of waiting for the next
// cycle.
func (s *CertificateService) Evaluate(ctx context.Context, row *models.MonitorCertificate, monitor *models.Monitor, now time.Time) (models.NotificationEvent, bool) {
	if row == nil || monitor == nil {
		return "", false
	}
	warn := certWarnDays(monitor)
	daysLeft := models.DaysLeft(row.NotAfter, now)
	expired := row.NotAfter.Before(now)
	day := now.UTC().Unix() / 86400

	plan := planCertificateAlerts(daysLeft, expired, warn, row.NotifiedThresholds(), row.LastNotifiedDay, day)
	if plan.Event == "" {
		return "", false
	}

	// The operator may want the badge without the inbox traffic.
	if !monitor.CertNotify || s.notifications == nil {
		return "", false
	}

	if s.cluster != nil {
		allowed, err := s.cluster.ClaimCertificateEvent(ctx, monitor.ID, plan.Event, day)
		if err != nil {
			s.log.Error("could not claim the certificate notification",
				"monitor_id", monitor.ID, "event", plan.Event, "error", err)
			return "", false
		}
		if !allowed {
			return "", false
		}
	}

	status := models.AggregateDegraded
	if plan.Event == models.EventCertExpired {
		status = models.AggregateDown
	}
	detail := certificateMessage(row, daysLeft, expired, plan)
	logs := s.notifications.Dispatch(ctx, monitor, plan.Event, status, detail, 0, s.cfg.NodeID)
	s.log.Info("certificate notification processed",
		"monitor_id", monitor.ID, "monitor", monitor.Name, "event", plan.Event,
		"days_left", daysLeft, "thresholds", plan.Thresholds, "daily", plan.Daily, "channels", len(logs))

	// The memory is written only after a decision, so a channel outage does not
	// silence the next reminder.
	s.markNotified(ctx, row, plan)
	return plan.Event, true
}

// markNotified records the thresholds and the day of the last reminder.
func (s *CertificateService) markNotified(ctx context.Context, row *models.MonitorCertificate, plan CertPlan) {
	merged := append([]int{}, row.NotifiedThresholds()...)
	merged = append(merged, plan.Mark...)
	updates := map[string]any{
		"last_notified_day": row.LastNotifiedDay,
	}
	if len(plan.Mark) > 0 {
		label := certThresholdsLabel(merged)
		updates["notified_days"] = label
		row.NotifiedDays = label
	}
	if plan.Daily || len(plan.Mark) > 0 {
		day := time.Now().UTC().Unix() / 86400
		updates["last_notified_day"] = day
		row.LastNotifiedDay = day
	}
	if err := s.db.WithContext(ctx).Model(&models.MonitorCertificate{}).
		Where("monitor_id = ?", row.MonitorID).Updates(updates).Error; err != nil {
		s.log.Error("could not store the certificate notification state", "monitor_id", row.MonitorID, "error", err)
	}
}

// certificateMessage renders the human readable detail of the notification.
func certificateMessage(row *models.MonitorCertificate, daysLeft int, expired bool, plan CertPlan) string {
	issuer := strings.TrimSpace(row.Issuer)
	suffix := ""
	if issuer != "" {
		suffix = fmt.Sprintf(" (issuer: %s)", issuer)
	}
	until := row.NotAfter.UTC().Format("2006-01-02")
	if expired {
		return fmt.Sprintf("TLS certificate EXPIRED on %s%s", until, suffix)
	}
	prefix := "TLS certificate"
	if plan.Daily {
		prefix = "TLS certificate still"
	}
	return fmt.Sprintf("%s expires in %d days, on %s%s", prefix, daysLeft, until, suffix)
}

// Refresh is the watcher job: it recomputes the remaining validity of every
// watched certificate and sends the reminders that are due.
//
// It never opens a socket: the certificate is captured by the probes, so the job
// only ages what is already stored (a monitor whose target is unreachable still
// gets its reminder, which is exactly what an operator wants to know).
func (s *CertificateService) Refresh(ctx context.Context) error {
	rows, monitors, err := s.All(ctx)
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		s.log.Debug("certificate watcher: nothing to watch")
		return nil
	}
	now := time.Now().UTC()
	byID := map[uint]*models.Monitor{}
	for _, monitor := range monitors {
		byID[monitor.ID] = monitor
	}

	sent := 0
	evaluated := 0
	for i := range rows {
		row := &rows[i]
		monitor, ok := byID[row.MonitorID]
		if !ok || !monitor.CertWatch {
			continue
		}
		daysLeft := models.DaysLeft(row.NotAfter, now)
		if daysLeft != row.DaysLeft {
			row.DaysLeft = daysLeft
			if err := s.db.WithContext(ctx).Model(&models.MonitorCertificate{}).
				Where("monitor_id = ?", row.MonitorID).
				Update("days_left", daysLeft).Error; err != nil {
				s.log.Warn("could not refresh the remaining validity", "monitor_id", row.MonitorID, "error", err)
			}
		}
		evaluated++
		if event, ok := s.Evaluate(ctx, row, monitor, now); ok {
			sent++
			s.publish("monitor.certificate", map[string]any{
				"monitor_id": row.MonitorID,
				"event":      event,
				"days_left":  daysLeft,
				"not_after":  row.NotAfter,
			})
		}
	}
	s.log.Info("certificate watcher finished", "certificates", evaluated, "notifications", sent)
	return nil
}
