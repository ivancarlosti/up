package services

import (
	"context"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/ivancarlosti/up/internal/models"
)

// EvaluateAndNotify is the heart of the notification pipeline. It is called by
// the scheduler right after a heartbeat is stored and it:
//
//  1. merges the votes of every online node into a single status;
//  2. compares it with the shared monitor_state row to detect a transition;
//  3. honours the re-notification interval to avoid notification spam;
//  4. applies the notification_sender strategy (PRIMARY_ONLY / ANY_WITH_LOCK);
//  5. dispatches the event and records the outcome.
//
// Because the state row lives in the shared database, two nodes observing the
// same transition produce a single notification.
func (s *ClusterService) EvaluateAndNotify(ctx context.Context, monitorID uint) error {
	var monitor models.Monitor
	if err := s.db.WithContext(ctx).First(&monitor, monitorID).Error; err != nil {
		return err
	}

	status, votes, err := s.Evaluate(ctx, &monitor)
	if err != nil {
		return err
	}

	now := time.Now().UTC()
	detail, latency, nodeID := s.latestDetail(ctx, monitorID)

	var state models.MonitorState
	err = s.db.WithContext(ctx).First(&state, monitorID).Error
	switch {
	case err == gorm.ErrRecordNotFound:
		state = models.MonitorState{MonitorID: monitorID, Status: models.AggregateUnknown, ChangedAt: now}
	case err != nil:
		return err
	}

	previous := state.Status
	transition := previous != "" && previous != models.AggregateUnknown && previous != status

	notify := transition
	if !notify && monitor.ResendIntervalSeconds > 0 && state.NotifiedAt != nil &&
		(status == models.AggregateDown || status == models.AggregateDegraded) &&
		now.Sub(*state.NotifiedAt) >= time.Duration(monitor.ResendIntervalSeconds)*time.Second {
		notify = true
	}

	// dispatched is true only on the node that actually sent the notification:
	// only that node may advance the notification bookkeeping (see
	// stateUpdateColumns).
	dispatched := false
	if notify && s.notifications != nil {
		event := normalizeEvent(status)
		allowed, claimErr := s.claimNotification(ctx, monitor.ID, event)
		if claimErr != nil {
			s.log.Error("could not claim the notification", "monitor_id", monitor.ID, "error", claimErr)
		} else if allowed {
			logs := s.notifications.Dispatch(ctx, &monitor, event, status, detail, latency, nodeID)
			s.log.Info("notification event processed",
				"monitor_id", monitor.ID, "monitor", monitor.Name,
				"from", previous, "to", status, "channels", len(logs))
			state.Notified = status
			state.NotifiedAt = &now
			dispatched = true
		}
	}

	if previous != status {
		state.ChangedAt = now
		s.log.Info("monitor status changed", "monitor_id", monitor.ID, "monitor", monitor.Name,
			"from", previous, "to", status)
	}
	state.Status = status
	state.LastMessage = detail
	state.LastLatency = latency
	state.LastCheckAt = &now
	state.UpdatedAt = now

	if err := s.saveState(ctx, &state, dispatched); err != nil {
		return err
	}

	s.publish("monitor.status", map[string]any{
		"monitor_id": monitor.ID,
		"status":     status,
		"previous":   previous,
		"changed":    previous != status,
		"votes":      votes,
		"message":    detail,
		"latency_ms": latency,
		"checked_at": now,
	})
	return nil
}

// claimNotification implements the notification_sender strategy:
//
//	PRIMARY_ONLY : only the primary node is allowed to send.
//	ANY_WITH_LOCK: any node can send, but a unique insert on
//	               (monitor_id, event, bucket) elects a single sender.
func (s *ClusterService) claimNotification(ctx context.Context, monitorID uint, event models.NotificationEvent) (bool, error) {
	return s.claimEvent(ctx, monitorID, event, time.Now().UTC().Unix()/models.NotificationLockWindowSeconds)
}

// ClaimCertificateEvent elects the node that sends a certificate notification.
//
// The bucket is the day (and not the 60 second window used by the status
// events): the reminder is daily, and the unique index of the lock table is what
// keeps a single node sending it when several nodes watch the same certificate.
func (s *ClusterService) ClaimCertificateEvent(ctx context.Context, monitorID uint, event models.NotificationEvent, day int64) (bool, error) {
	return s.claimEvent(ctx, monitorID, event, day)
}

// claimEvent implements the sender election for an explicit bucket.
func (s *ClusterService) claimEvent(ctx context.Context, monitorID uint, event models.NotificationEvent, bucket int64) (bool, error) {
	settings, err := s.Settings(ctx)
	if err != nil {
		return false, err
	}
	if settings.NotificationSender == models.NotificationSenderPrimaryOnly {
		primary := s.IsPrimary(ctx)
		if !primary {
			s.log.Debug("notification skipped: this node is not the primary", "monitor_id", monitorID)
		}
		return primary, nil
	}

	lock := models.NotificationLock{
		MonitorID: monitorID,
		Event:     event,
		Bucket:    bucket,
		NodeID:    s.cfg.NodeID,
		CreatedAt: time.Now().UTC(),
	}
	result := s.db.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&lock)
	if result.Error != nil {
		return false, result.Error
	}
	if result.RowsAffected == 1 {
		return true, nil
	}
	s.log.Debug("notification already dispatched by another node",
		"monitor_id", monitorID, "event", event, "bucket", bucket)
	return false, nil
}

// PurgeExpiredLocks deletes the notification de-duplication rows that no node can
// claim any more.
//
// The unique index keeps the table correct without this; the prune keeps it
// small. It used to grow for the whole life of the deployment: the only delete
// was the one running when a monitor is deleted, so every transition (and every
// re-notification) left a row behind forever.
//
// The cut-off is created_at, never bucket - see
// models.NotificationLockRetentionHours for why.
func (s *ClusterService) PurgeExpiredLocks(ctx context.Context, before time.Time) (int64, error) {
	result := s.db.WithContext(ctx).Where("created_at < ?", before).Delete(&models.NotificationLock{})
	if result.Error != nil {
		return 0, ErrInternal(result.Error)
	}
	return result.RowsAffected, nil
}

// stateUpdateColumns is the pure core of saveState: it lists the columns the
// upsert refreshes.
//
// The notification bookkeeping (notified, notified_at) is advanced only by the
// node that actually dispatched the notification. Without that distinction the
// loser of a claim writes back the values it read *before* the winner committed:
// with resend_interval_seconds > 0 the resend clock is then rewound and the same
// incident is alerted again earlier than configured.
//
// The rest of the columns stay last-writer-wins: two nodes evaluating the same
// shared votes compute the same aggregated status, so whichever write lands last
// the value is equivalent.
func stateUpdateColumns(dispatched bool) []string {
	columns := []string{
		"status", "changed_at", "last_message", "last_latency", "last_check_at", "updated_at",
	}
	if dispatched {
		columns = append(columns, "notified", "notified_at")
	}
	return columns
}

// saveState upserts the aggregated monitor state.
func (s *ClusterService) saveState(ctx context.Context, state *models.MonitorState, dispatched bool) error {
	return s.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "monitor_id"}},
		DoUpdates: clause.AssignmentColumns(stateUpdateColumns(dispatched)),
	}).Create(state).Error
}

// latestDetail returns the newest heartbeat details of a monitor.
func (s *ClusterService) latestDetail(ctx context.Context, monitorID uint) (string, int64, string) {
	var hb models.Heartbeat
	if err := s.db.WithContext(ctx).
		Where("monitor_id = ?", monitorID).
		Order("id DESC").
		First(&hb).Error; err != nil {
		return "", 0, s.cfg.NodeID
	}
	return hb.Message, hb.LatencyMS, hb.NodeID
}

// States returns the aggregated state of every monitor (used by the public API
// and the status pages summary).
func (s *ClusterService) States(ctx context.Context) (map[uint]models.MonitorState, error) {
	var states []models.MonitorState
	if err := s.db.WithContext(ctx).Find(&states).Error; err != nil {
		return nil, ErrInternal(err)
	}
	out := make(map[uint]models.MonitorState, len(states))
	for _, state := range states {
		out[state.MonitorID] = state
	}
	return out, nil
}
