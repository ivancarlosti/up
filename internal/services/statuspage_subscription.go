package services

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/mail"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/ivancarlosti/up/internal/config"
	"github.com/ivancarlosti/up/internal/i18n"
	"github.com/ivancarlosti/up/internal/models"
	"github.com/ivancarlosti/up/internal/utils"
)

// Subscription handshake timings and delivery limits.
const (
	// SubscriptionConfirmTokenTTL is how long a confirmation link stays valid.
	SubscriptionConfirmTokenTTL = 7 * 24 * time.Hour
	// SubscriptionConfirmationCooldown is the shortest interval between two
	// confirmation mails to the same address.
	SubscriptionConfirmationCooldown = 10 * time.Minute
	// SubscriptionNoticeMaxAttempts bounds the retries of one incident mail.
	SubscriptionNoticeMaxAttempts = 5
	// SubscriptionNoticeClaimTimeout lets a scheduler pass recover a notice left
	// in "sending" by a process that died mid-delivery.
	SubscriptionNoticeClaimTimeout = 10 * time.Minute
)

// StatusPageSubscriptionService owns the double opt-in subscriptions of the
// public status pages and the incident mails delivered to them.
type StatusPageSubscriptionService struct {
	db            *gorm.DB
	cfg           *config.Config
	log           *slog.Logger
	settings      *SettingService
	notifications *NotificationService
	cluster       *ClusterService
	// emit publishes local subscriber writes to the peers (nil outside federated
	// mode).
	emit *SyncEmitter
}

// NewStatusPageSubscriptionService builds the subscription service.
func NewStatusPageSubscriptionService(db *gorm.DB, cfg *config.Config, log *slog.Logger) *StatusPageSubscriptionService {
	return &StatusPageSubscriptionService{db: db, cfg: cfg, log: log}
}

// SetSettingService injects the setting store that owns the shared cluster key
// used to derive unsubscribe tokens.
func (s *StatusPageSubscriptionService) SetSettingService(settings *SettingService) {
	s.settings = settings
}

// SetNotificationService injects the channel delivery service.
func (s *StatusPageSubscriptionService) SetNotificationService(notifications *NotificationService) {
	s.notifications = notifications
}

// SetClusterService injects the sender election, so two nodes never mail the
// same incident twice.
func (s *StatusPageSubscriptionService) SetClusterService(cluster *ClusterService) {
	s.cluster = cluster
}

// SetSyncEmitter injects the publisher of the synchronisation outbox.
func (s *StatusPageSubscriptionService) SetSyncEmitter(emitter *SyncEmitter) { s.emit = emitter }

// PublicAvailability reports whether the page can accept a subscription right
// now: the switch is on and the configured SMTP channel exists and is active.
//
// It is what the public form is gated on. A page whose channel disappeared (or
// was disabled) keeps its stored subscribers and its incidents, but offers no
// form until the channel is back.
func (s *StatusPageSubscriptionService) PublicAvailability(ctx context.Context, page *models.StatusPage) bool {
	if page == nil || !page.SubscriptionEnabled || strings.TrimSpace(page.SubscriptionSMTPUUID) == "" {
		return false
	}
	var notification models.Notification
	err := s.db.WithContext(ctx).
		Where("uuid = ? AND type = ? AND active = ?", page.SubscriptionSMTPUUID, models.NotificationSMTP, true).
		First(&notification).Error
	if err != nil {
		return false
	}
	return notification.Config.SMTP != nil
}

// normalizeSubscriptionEmail lower-cases and validates an address.
func normalizeSubscriptionEmail(raw string) (string, error) {
	value := strings.ToLower(strings.TrimSpace(raw))
	if value == "" || len(value) > 320 {
		return "", ErrBadRequest(i18n.CodeSubscriptionEmail, "a valid e-mail address is required")
	}
	parsed, err := mail.ParseAddress(value)
	if err != nil || parsed.Address != value || !strings.Contains(value, "@") ||
		strings.ContainsAny(value, " \t,;<>\"") {
		return "", ErrBadRequest(i18n.CodeSubscriptionEmail, "a valid e-mail address is required")
	}
	return value, nil
}

// subscriptionLocale keeps a locale the mails can render.
func (s *StatusPageSubscriptionService) subscriptionLocale(locale string) string {
	locale = strings.TrimSpace(locale)
	for _, supported := range config.SupportedLocales {
		if locale == supported {
			return locale
		}
	}
	if s.cfg != nil {
		for _, supported := range config.SupportedLocales {
			if s.cfg.DefaultLocale == supported {
				return s.cfg.DefaultLocale
			}
		}
	}
	return "en-US"
}

// pageBySlug loads a page.
func (s *StatusPageSubscriptionService) pageBySlug(ctx context.Context, slug string) (*models.StatusPage, error) {
	var page models.StatusPage
	err := s.db.WithContext(ctx).Where("slug = ?", strings.ToLower(strings.TrimSpace(slug))).First(&page).Error
	if err == gorm.ErrRecordNotFound {
		return nil, ErrNotFound(i18n.CodeStatusPageNotFound, "status page "+slug+" does not exist")
	}
	if err != nil {
		return nil, ErrInternal(err)
	}
	return &page, nil
}

// pageForSubscription loads a public page and rejects the one that cannot accept
// subscriptions.
func (s *StatusPageSubscriptionService) pageForSubscription(ctx context.Context, slug string) (*models.StatusPage, error) {
	page, err := s.pageBySlug(ctx, slug)
	if err != nil {
		return nil, err
	}
	if !page.IsPublic {
		return nil, ErrForbidden(i18n.CodeStatusPageHidden, "this status page is not public")
	}
	if !s.PublicAvailability(ctx, page) {
		return nil, ErrForbidden(i18n.CodeSubscriptionDisabled, "subscriptions are not available on this status page")
	}
	return page, nil
}

// publishSubscriber records a local subscription write in the sync outbox.
func (s *StatusPageSubscriptionService) publishSubscriber(ctx context.Context, tx *gorm.DB, id uint) error {
	if s.emit == nil || id == 0 {
		return nil
	}
	return s.emit.EmitStatusPageSubscriber(ctx, tx, id)
}

// clearConfirmationSent lets the next request try again after a failed
// delivery. Without it a transient SMTP failure would keep the resend cooldown
// running for ten minutes while the visitor has already seen the error.
func (s *StatusPageSubscriptionService) clearConfirmationSent(ctx context.Context, id uint) {
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&models.StatusPageSubscriber{}).Where("id = ?", id).Updates(map[string]any{
			"confirmation_sent_at": nil,
			"revision":             gorm.Expr("revision + 1"),
			"updated_at":           time.Now().UTC(),
		}).Error; err != nil {
			return err
		}
		return s.publishSubscriber(ctx, tx, id)
	})
	if err != nil {
		s.log.Warn("could not reset the confirmation cooldown", "subscriber_id", id, "error", err)
	}
}

// unsubscribeSecret returns the key that makes unsubscribe links unguessable.
//
// The shared cluster key is used on purpose: every node can derive the same link
// for the same subscriber without storing the bearer token, and a cluster that
// could not agree on the key could not authenticate its peers either.
func (s *StatusPageSubscriptionService) unsubscribeSecret(ctx context.Context) string {
	if s.settings != nil {
		if secret, err := s.settings.ClusterPrivateKey(ctx); err == nil && secret != "" {
			return secret
		}
	}
	if s.cfg != nil && s.cfg.ClusterPrivateKey != "" {
		return s.cfg.ClusterPrivateKey
	}
	if s.cfg != nil {
		return s.cfg.NodeID
	}
	return "up-status-page-subscription"
}

// buildUnsubscribeToken returns "<subscriber-uuid>.<hmac>".
func buildUnsubscribeToken(secret, subscriberUUID string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte("statuspage-unsubscribe|" + subscriberUUID))
	return subscriberUUID + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// sha256Hex is the stored form of every subscription token.
func sha256Hex(value string) string { return utils.SHA256Hex(value) }

// verifyUnsubscribeToken checks the signature AND the stored hash.
func verifyUnsubscribeToken(secret, token, storedHash string) (string, bool) {
	token = strings.TrimSpace(token)
	uuid, signature, found := strings.Cut(token, ".")
	if !found || uuid == "" || signature == "" {
		return "", false
	}
	expected := buildUnsubscribeToken(secret, uuid)
	if !utils.SecureCompare(expected, token) {
		return "", false
	}
	if storedHash != "" && !utils.SecureCompare(utils.SHA256Hex(token), storedHash) {
		return "", false
	}
	return uuid, true
}

// isDownStatus reports whether an aggregated status is the outage side of an
// incident.
func isDownStatus(status models.AggregateStatus) bool {
	return status == models.AggregateDown || status == models.AggregateDegraded
}

// errSubscriptionToken is the stable error of a stale or forged link.
func errSubscriptionToken(detail string) *APIError {
	return ErrBadRequest(i18n.CodeSubscriptionToken, detail)
}

var errSubscriptionMail = errors.New("subscription mail could not be delivered")

// Subscribe starts (or restarts) the double opt-in of an address.
//
// The answer is deliberately the same whether the address is new, pending or
// already subscribed: an endpoint that says "already subscribed" is an
// enumeration oracle for a public page. A confirmation mail is only sent when
// one is actually needed, and never twice inside the cooldown.
func (s *StatusPageSubscriptionService) Subscribe(ctx context.Context, slug, rawEmail, locale string) error {
	page, err := s.pageForSubscription(ctx, slug)
	if err != nil {
		return err
	}
	email, err := normalizeSubscriptionEmail(rawEmail)
	if err != nil {
		return err
	}
	locale = s.subscriptionLocale(locale)
	now := time.Now().UTC()

	token, err := utils.RandomHex(32)
	if err != nil {
		return ErrInternal(err)
	}
	expires := now.Add(SubscriptionConfirmTokenTTL)
	secret := s.unsubscribeSecret(ctx)

	var subscriberID uint
	var sendToken string
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var existing models.StatusPageSubscriber
		findErr := tx.Where("status_page_id = ? AND email = ?", page.ID, email).First(&existing).Error
		switch findErr {
		case nil:
			if existing.ConfirmedAt != nil && existing.UnsubscribedAt == nil {
				return nil
			}
			if existing.ConfirmationSentAt != nil && existing.UnsubscribedAt == nil &&
				now.Sub(*existing.ConfirmationSentAt) < SubscriptionConfirmationCooldown {
				return nil
			}
			updates := map[string]any{
				"locale":                   locale,
				"confirm_token_hash":       sha256Hex(token),
				"confirm_token_expires_at": expires,
				"confirmation_sent_at":     now,
				"unsubscribe_token_hash":   sha256Hex(buildUnsubscribeToken(secret, existing.UUID)),
				"revision":                 gorm.Expr("revision + 1"),
				"updated_at":               now,
			}
			if existing.UnsubscribedAt != nil {
				// A resubscribe is a NEW double opt-in: the old confirmation must
				// not reactivate the address, and the fresh token is the only one
				// that can. The unsubscribed flag is cleared now because the row is
				// pending again: the new link is what makes it active.
				updates["confirmed_at"] = nil
				updates["unsubscribed_at"] = nil
			}
			if err := tx.Model(&models.StatusPageSubscriber{}).Where("id = ?", existing.ID).
				Updates(updates).Error; err != nil {
				return err
			}
			subscriberID = existing.ID
		case gorm.ErrRecordNotFound:
			unsubscribeToken := buildUnsubscribeToken(secret, models.StatusPageSubscriberUUID(page.UUID, email))
			row := models.StatusPageSubscriber{
				UUID:                  models.StatusPageSubscriberUUID(page.UUID, email),
				OriginNodeID:          s.cfg.NodeID,
				Revision:              1,
				StatusPageID:          page.ID,
				Email:                 email,
				Locale:                locale,
				ConfirmationSentAt:    &now,
				ConfirmTokenHash:      sha256Hex(token),
				ConfirmTokenExpiresAt: &expires,
				UnsubscribeTokenHash:  sha256Hex(unsubscribeToken),
			}
			if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error; err != nil {
				return err
			}
			if row.ID == 0 {
				// A concurrent request created the same page+address first. The
				// answer stays the neutral success: the other request owns the
				// confirmation mail.
				return nil
			}
			subscriberID = row.ID
		default:
			return findErr
		}
		if subscriberID == 0 {
			return nil
		}
		if err := s.publishSubscriber(ctx, tx, subscriberID); err != nil {
			return err
		}
		sendToken = token
		return nil
	})
	if err != nil {
		return ErrInternal(err)
	}
	if sendToken == "" {
		return nil
	}
	if err := s.sendSubscriptionConfirmation(ctx, subscriberID, page, sendToken); err != nil {
		s.clearConfirmationSent(ctx, subscriberID)
		s.log.Warn("could not send the subscription confirmation",
			"status_page_id", page.ID, "subscriber_id", subscriberID, "error", err)
		return NewError(http.StatusBadGateway, i18n.CodeSubscriptionSendFailed,
			"the confirmation e-mail could not be delivered")
	}
	return nil
}

// Confirm activates a subscription from the link of the confirmation mail. It
// is idempotent for the link that confirmed it, and it never reactivates an
// address that unsubscribed afterwards.
func (s *StatusPageSubscriptionService) Confirm(ctx context.Context, slug, token string) error {
	page, err := s.pageBySlug(ctx, slug)
	if err != nil {
		return err
	}
	token = strings.TrimSpace(token)
	if token == "" {
		return errSubscriptionToken("the confirmation token is missing")
	}
	var subscriber models.StatusPageSubscriber
	err = s.db.WithContext(ctx).
		Where("status_page_id = ? AND confirm_token_hash = ?", page.ID, sha256Hex(token)).
		First(&subscriber).Error
	if err == gorm.ErrRecordNotFound {
		return errSubscriptionToken("this confirmation link is not valid")
	}
	if err != nil {
		return ErrInternal(err)
	}
	if subscriber.UnsubscribedAt != nil {
		return errSubscriptionToken("this address unsubscribed after this link was issued")
	}
	if subscriber.ConfirmedAt != nil {
		return nil
	}
	if subscriber.ConfirmTokenExpiresAt == nil || time.Now().UTC().After(*subscriber.ConfirmTokenExpiresAt) {
		return errSubscriptionToken("this confirmation link has expired")
	}

	now := time.Now().UTC()
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&models.StatusPageSubscriber{}).Where("id = ?", subscriber.ID).Updates(map[string]any{
			"confirmed_at":    now,
			"unsubscribed_at": nil,
			"revision":        gorm.Expr("revision + 1"),
			"updated_at":      now,
		}).Error; err != nil {
			return err
		}
		return s.publishSubscriber(ctx, tx, subscriber.ID)
	})
	if err != nil {
		return ErrInternal(err)
	}
	s.log.Info("status page subscription confirmed",
		"status_page_id", page.ID, "subscriber_id", subscriber.ID)
	return nil
}

// Unsubscribe stops the incident mails of a subscriber. It is idempotent: a
// second click on the same link succeeds without changing anything.
func (s *StatusPageSubscriptionService) Unsubscribe(ctx context.Context, slug, token string) error {
	page, err := s.pageBySlug(ctx, slug)
	if err != nil {
		return err
	}
	token = strings.TrimSpace(token)
	if token == "" {
		return errSubscriptionToken("the unsubscribe token is missing")
	}
	subscriberUUID, ok := verifyUnsubscribeToken(s.unsubscribeSecret(ctx), token, "")
	if !ok {
		return errSubscriptionToken("this unsubscribe link is not valid")
	}
	var subscriber models.StatusPageSubscriber
	err = s.db.WithContext(ctx).
		Where("status_page_id = ? AND uuid = ?", page.ID, subscriberUUID).
		First(&subscriber).Error
	if err == gorm.ErrRecordNotFound {
		return errSubscriptionToken("this unsubscribe link is not valid")
	}
	if err != nil {
		return ErrInternal(err)
	}
	if subscriber.UnsubscribeTokenHash != "" &&
		!utils.SecureCompare(sha256Hex(token), subscriber.UnsubscribeTokenHash) {
		return errSubscriptionToken("this unsubscribe link is no longer valid")
	}
	if subscriber.UnsubscribedAt != nil {
		return nil
	}

	now := time.Now().UTC()
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&models.StatusPageSubscriber{}).Where("id = ?", subscriber.ID).Updates(map[string]any{
			"unsubscribed_at": now,
			"revision":        gorm.Expr("revision + 1"),
			"updated_at":      now,
		}).Error; err != nil {
			return err
		}
		if err := tx.Model(&models.SubscriptionNotice{}).
			Where("subscriber_id = ? AND status = ?", subscriber.ID, models.SubscriptionNoticePending).
			Updates(map[string]any{
				"status":     models.SubscriptionNoticeSkipped,
				"last_error": "the subscriber unsubscribed before delivery",
				"updated_at": now,
			}).Error; err != nil {
			return err
		}
		return s.publishSubscriber(ctx, tx, subscriber.ID)
	})
	if err != nil {
		return ErrInternal(err)
	}
	s.log.Info("status page subscription removed",
		"status_page_id", page.ID, "subscriber_id", subscriber.ID)
	return nil
}

// SubscriberList is one page of the admin subscriber listing.
type SubscriberList struct {
	Subscribers []*models.StatusPageSubscriber `json:"subscribers"`
	Total       int64                          `json:"total"`
	Page        int                            `json:"page"`
	Size        int                            `json:"size"`
}

// List returns the subscribers of a page, newest first, with a text filter over
// the address.
func (s *StatusPageSubscriptionService) List(ctx context.Context, pageID uint, search string, page, size int) (*SubscriberList, error) {
	if page < 1 {
		page = 1
	}
	if size <= 0 || size > 100 {
		size = 25
	}
	query := s.db.WithContext(ctx).Model(&models.StatusPageSubscriber{}).Where("status_page_id = ?", pageID)
	if term := strings.TrimSpace(strings.ToLower(search)); term != "" {
		query = query.Where("LOWER(email) LIKE ?", "%"+term+"%")
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, ErrInternal(err)
	}
	var subscribers []*models.StatusPageSubscriber
	if err := query.Order("created_at DESC, id DESC").
		Offset((page - 1) * size).Limit(size).
		Find(&subscribers).Error; err != nil {
		return nil, ErrInternal(err)
	}
	if subscribers == nil {
		subscribers = []*models.StatusPageSubscriber{}
	}
	return &SubscriberList{Subscribers: subscribers, Total: total, Page: page, Size: size}, nil
}

// Delete removes one subscriber (the explicit admin action). The row is
// tombstoned in the outbox before it disappears, so a peer removes it too.
func (s *StatusPageSubscriptionService) Delete(ctx context.Context, pageID, subscriberID uint) error {
	var subscriber models.StatusPageSubscriber
	err := s.db.WithContext(ctx).
		Where("id = ? AND status_page_id = ?", subscriberID, pageID).
		First(&subscriber).Error
	if err == gorm.ErrRecordNotFound {
		return ErrNotFound(i18n.CodeNotFound, fmt.Sprintf("subscriber %d does not exist", subscriberID))
	}
	if err != nil {
		return ErrInternal(err)
	}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if s.emit != nil && subscriber.UUID != "" {
			if err := s.emit.EmitDelete(ctx, tx, models.EntityStatusPageSubscriber, subscriber.UUID); err != nil {
				return err
			}
		}
		if err := tx.Where("subscriber_id = ?", subscriber.ID).Delete(&models.SubscriptionNotice{}).Error; err != nil {
			return err
		}
		return tx.Delete(&models.StatusPageSubscriber{}, subscriber.ID).Error
	})
	if err != nil {
		return ErrInternal(err)
	}
	s.log.Info("status page subscriber deleted",
		"status_page_id", pageID, "subscriber_id", subscriberID)
	return nil
}
