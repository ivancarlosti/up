package services

import (
	"context"
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/ivancarlosti/up/internal/models"
	"github.com/ivancarlosti/up/internal/utils"
)

// ObserveTransition opens or closes the subscription incident of a monitor on
// every status page that publishes it.
//
// It is called by the notification pipeline after the aggregated status changed.
// The incident rows are local (they are not federated): the subscribers are, so
// whichever node owns the mail duty can find them, and the unique open key makes
// two nodes observing the same transition converge on one incident when they
// share a database.
func (s *StatusPageSubscriptionService) ObserveTransition(ctx context.Context, monitorID uint, previous, status models.AggregateStatus, at time.Time) error {
	if previous == status {
		return nil
	}
	pageIDs, err := s.subscribedStatusPagesOf(ctx, monitorID)
	if err != nil {
		return err
	}
	if len(pageIDs) == 0 {
		return nil
	}
	if at.IsZero() {
		at = time.Now().UTC()
	}
	for _, pageID := range pageIDs {
		if err := s.observePageTransition(ctx, pageID, monitorID, status, at); err != nil {
			return err
		}
	}
	return nil
}

// subscribedStatusPagesOf lists the pages with subscriptions that publish a
// monitor, either explicitly or through one of their groups.
func (s *StatusPageSubscriptionService) subscribedStatusPagesOf(ctx context.Context, monitorID uint) ([]uint, error) {
	var explicit []uint
	if err := s.db.WithContext(ctx).Table("status_page_monitors").
		Where("monitor_id = ?", monitorID).Distinct().Pluck("status_page_id", &explicit).Error; err != nil {
		return nil, ErrInternal(err)
	}
	var throughGroups []uint
	if err := s.db.WithContext(ctx).Table("status_page_groups").
		Joins("JOIN monitor_group_members ON monitor_group_members.group_id = status_page_groups.group_id").
		Where("monitor_group_members.monitor_id = ?", monitorID).
		Distinct().Pluck("status_page_groups.status_page_id", &throughGroups).Error; err != nil {
		return nil, ErrInternal(err)
	}
	seen := map[uint]bool{}
	all := append(explicit, throughGroups...)
	enabled := make([]uint, 0, len(all))
	for _, id := range all {
		if id == 0 || seen[id] {
			continue
		}
		seen[id] = true
		enabled = append(enabled, id)
	}
	if len(enabled) == 0 {
		return nil, nil
	}
	var pages []uint
	if err := s.db.WithContext(ctx).Model(&models.StatusPage{}).
		Where("id IN ? AND subscription_enabled = ?", enabled, true).
		Pluck("id", &pages).Error; err != nil {
		return nil, ErrInternal(err)
	}
	return pages, nil
}

// observePageTransition applies one page's side of a transition.
func (s *StatusPageSubscriptionService) observePageTransition(ctx context.Context, pageID, monitorID uint, status models.AggregateStatus, at time.Time) error {
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var page models.StatusPage
		if err := tx.First(&page, pageID).Error; err != nil {
			return err
		}
		var incident models.SubscriptionIncident
		findErr := tx.Where("status_page_id = ? AND monitor_id = ? AND open_key IS NOT NULL", pageID, monitorID).
			First(&incident).Error
		if findErr != nil && findErr != gorm.ErrRecordNotFound {
			return findErr
		}
		open := findErr == nil

		if isDownStatus(status) {
			if open {
				return nil
			}
			key := fmt.Sprintf("%d:%d", pageID, monitorID)
			incident = models.SubscriptionIncident{
				StatusPageID: pageID,
				MonitorID:    monitorID,
				OpenKey:      &key,
				OpenedAt:     at,
			}
			result := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&incident)
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected == 0 {
				return nil
			}
			delay := models.NormalizeSubscriptionDelayMinutes(page.SubscriptionDelayMinutes)
			return s.createSubscriptionNotices(ctx, tx, &page, &incident, models.SubscriptionNoticeDown, at.Add(time.Duration(delay)*time.Minute))
		}

		if !open {
			return nil
		}
		if err := tx.Model(&models.SubscriptionIncident{}).Where("id = ?", incident.ID).Updates(map[string]any{
			"open_key":   nil,
			"closed_at":  at,
			"updated_at": at,
		}).Error; err != nil {
			return err
		}
		if status == models.AggregateUp {
			return s.createSubscriptionNotices(ctx, tx, &page, &incident, models.SubscriptionNoticeUp, at)
		}
		// A recovery that is not "up" (maintenance, pending, unknown) closes the
		// incident without sending a recovery mail, and cancels the down mail that
		// has not left yet.
		return tx.Model(&models.SubscriptionNotice{}).
			Where("incident_id = ? AND kind = ? AND status = ?", incident.ID,
				models.SubscriptionNoticeDown, models.SubscriptionNoticePending).
			Updates(map[string]any{
				"status":     models.SubscriptionNoticeSkipped,
				"last_error": "the incident closed before the delay elapsed",
				"updated_at": at,
			}).Error
	})
	if err != nil {
		return ErrInternal(err)
	}
	return nil
}

// createSubscriptionNotices queues one kind of mail for every active subscriber
// of a page. The unique key over (incident, subscriber, kind) makes the call
// idempotent.
func (s *StatusPageSubscriptionService) createSubscriptionNotices(ctx context.Context, tx *gorm.DB,
	page *models.StatusPage, incident *models.SubscriptionIncident, kind string, scheduledAt time.Time) error {

	var subscribers []models.StatusPageSubscriber
	if err := tx.WithContext(ctx).
		Where("status_page_id = ? AND confirmed_at IS NOT NULL AND unsubscribed_at IS NULL", page.ID).
		Find(&subscribers).Error; err != nil {
		return err
	}
	now := time.Now().UTC()
	for _, subscriber := range subscribers {
		notice := models.SubscriptionNotice{
			IncidentID:   incident.ID,
			SubscriberID: subscriber.ID,
			Kind:         kind,
			StatusPageID: page.ID,
			MonitorID:    incident.MonitorID,
			Status:       models.SubscriptionNoticePending,
			ScheduledAt:  scheduledAt,
			CreatedAt:    now,
			UpdatedAt:    now,
		}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&notice).Error; err != nil {
			return err
		}
	}
	return nil
}

// RunDue delivers the subscription mails whose scheduled moment has arrived.
// It is called by the scheduler every 30 seconds and is safe to run on every
// node: the sender election picks one, and the notice row is claimed atomically
// before the mail leaves.
func (s *StatusPageSubscriptionService) RunDue(ctx context.Context, now time.Time) error {
	if s.notifications == nil {
		return nil
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	stale := now.Add(-SubscriptionNoticeClaimTimeout)
	if err := s.db.WithContext(ctx).Model(&models.SubscriptionNotice{}).
		Where("status = ? AND claimed_at IS NOT NULL AND claimed_at < ?", models.SubscriptionNoticeSending, stale).
		Updates(map[string]any{"status": models.SubscriptionNoticePending, "claimed_at": nil, "updated_at": now}).Error; err != nil {
		return ErrInternal(err)
	}

	var due []models.SubscriptionNotice
	if err := s.db.WithContext(ctx).
		Where("status = ? AND scheduled_at <= ?", models.SubscriptionNoticePending, now).
		Where("next_attempt_at IS NULL OR next_attempt_at <= ?", now).
		Order("scheduled_at ASC, id ASC").Limit(200).
		Find(&due).Error; err != nil {
		return ErrInternal(err)
	}
	for i := range due {
		if err := s.deliverSubscriptionNotice(ctx, &due[i], now); err != nil {
			s.log.Warn("could not process a status page subscription notice",
				"notice_id", due[i].ID, "error", err)
		}
	}
	return nil
}

// deliverSubscriptionNotice performs one delivery attempt for one notice.
func (s *StatusPageSubscriptionService) deliverSubscriptionNotice(ctx context.Context, notice *models.SubscriptionNotice, now time.Time) error {
	var subscriber models.StatusPageSubscriber
	err := s.db.WithContext(ctx).First(&subscriber, notice.SubscriberID).Error
	if err == gorm.ErrRecordNotFound {
		return s.finishSubscriptionNotice(ctx, notice.ID, models.SubscriptionNoticeSkipped, now, "the subscriber no longer exists", 0)
	}
	if err != nil {
		return err
	}
	if subscriber.ConfirmedAt == nil || subscriber.UnsubscribedAt != nil {
		return s.finishSubscriptionNotice(ctx, notice.ID, models.SubscriptionNoticeSkipped, now, "the subscriber is not active", 0)
	}

	var page models.StatusPage
	err = s.db.WithContext(ctx).First(&page, notice.StatusPageID).Error
	if err == gorm.ErrRecordNotFound {
		return s.finishSubscriptionNotice(ctx, notice.ID, models.SubscriptionNoticeSkipped, now, "the status page no longer exists", 0)
	}
	if err != nil {
		return err
	}
	if !s.PublicAvailability(ctx, &page) {
		return s.finishSubscriptionNotice(ctx, notice.ID, models.SubscriptionNoticeSkipped, now, "subscriptions are disabled on the status page", 0)
	}
	if notice.Kind == models.SubscriptionNoticeDown {
		var incident models.SubscriptionIncident
		if err := s.db.WithContext(ctx).First(&incident, notice.IncidentID).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				return s.finishSubscriptionNotice(ctx, notice.ID, models.SubscriptionNoticeSkipped, now, "the incident no longer exists", 0)
			}
			return err
		}
		if incident.ClosedAt != nil {
			return s.finishSubscriptionNotice(ctx, notice.ID, models.SubscriptionNoticeSkipped, now, "the incident recovered before the delay elapsed", 0)
		}
	}
	if s.cluster != nil {
		owned, err := s.cluster.OwnsMonitorNotification(ctx, notice.MonitorID)
		if err != nil {
			return err
		}
		if !owned {
			// Another node owns this monitor: leaving the notice pending is what
			// lets the owner send it a moment later.
			return nil
		}
	}

	claim := s.db.WithContext(ctx).Model(&models.SubscriptionNotice{}).
		Where("id = ? AND status = ?", notice.ID, models.SubscriptionNoticePending).
		Updates(map[string]any{
			"status":     models.SubscriptionNoticeSending,
			"claimed_at": now,
			"updated_at": now,
		})
	if claim.Error != nil {
		return claim.Error
	}
	if claim.RowsAffected == 0 {
		return nil
	}

	var monitor models.Monitor
	if err := s.db.WithContext(ctx).First(&monitor, notice.MonitorID).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return s.finishSubscriptionNotice(ctx, notice.ID, models.SubscriptionNoticeSkipped, now, "the monitor no longer exists", 0)
		}
		return err
	}
	secret := s.unsubscribeSecret(ctx)
	unsubscribeToken := buildUnsubscribeToken(secret, subscriber.UUID)
	if !utils.SecureCompare(sha256Hex(unsubscribeToken), subscriber.UnsubscribeTokenHash) {
		// The token is derived from the shared cluster key. If the key changed (or
		// the row arrived without the hash), the stored hash is refreshed with the
		// token the mail is about to carry, so the link in THIS mail works.
		err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			if err := tx.Model(&models.StatusPageSubscriber{}).Where("id = ?", subscriber.ID).
				Updates(map[string]any{
					"unsubscribe_token_hash": sha256Hex(unsubscribeToken),
					"revision":               gorm.Expr("revision + 1"),
					"updated_at":             now,
				}).Error; err != nil {
				return err
			}
			return s.publishSubscriber(ctx, tx, subscriber.ID)
		})
		if err != nil {
			return err
		}
	}
	if err := s.sendSubscriptionNotice(ctx, &page, &subscriber, &monitor, notice, unsubscribeToken, now); err != nil {
		attempts := notice.Attempts + 1
		s.log.Warn("status page subscription mail failed",
			"notice_id", notice.ID, "status_page_id", notice.StatusPageID,
			"subscriber_id", subscriber.ID, "attempt", attempts, "error", err)
		if attempts >= SubscriptionNoticeMaxAttempts {
			return s.finishSubscriptionNotice(ctx, notice.ID, models.SubscriptionNoticeFailed, now, err.Error(), attempts)
		}
		delay := time.Duration(1<<uint(attempts-1)) * time.Minute
		if delay > 30*time.Minute {
			delay = 30 * time.Minute
		}
		next := now.Add(delay)
		if dbErr := s.db.WithContext(ctx).Model(&models.SubscriptionNotice{}).
			Where("id = ?", notice.ID).Updates(map[string]any{
			"status":          models.SubscriptionNoticePending,
			"attempts":        attempts,
			"next_attempt_at": next,
			"claimed_at":      nil,
			"last_error":      trimmed(err.Error(), 1000),
			"updated_at":      now,
		}).Error; dbErr != nil {
			return dbErr
		}
		return err
	}
	return s.finishSubscriptionNotice(ctx, notice.ID, models.SubscriptionNoticeSent, now, "", notice.Attempts)
}

// finishSubscriptionNotice records the terminal (or retry) outcome of a notice.
func (s *StatusPageSubscriptionService) finishSubscriptionNotice(ctx context.Context, id uint, status string, now time.Time, lastError string, attempts int) error {
	updates := map[string]any{
		"status":     status,
		"last_error": trimmed(lastError, 1000),
		"attempts":   attempts,
		"updated_at": now,
	}
	if status == models.SubscriptionNoticeSent {
		updates["sent_at"] = now
		updates["next_attempt_at"] = nil
	}
	if err := s.db.WithContext(ctx).Model(&models.SubscriptionNotice{}).Where("id = ?", id).
		Updates(updates).Error; err != nil {
		return ErrInternal(err)
	}
	return nil
}
