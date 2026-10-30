package services

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/ivancarlosti/up/internal/i18n"
	"github.com/ivancarlosti/up/internal/models"
	"github.com/ivancarlosti/up/internal/notify"
)

// subscriptionBaseURL is APP_URL without its trailing slash.
func (s *StatusPageSubscriptionService) subscriptionBaseURL() string {
	if s.cfg == nil {
		return ""
	}
	return strings.TrimRight(strings.TrimSpace(s.cfg.AppURL), "/")
}

// publicStatusURL is the page a subscriber can open from a mail.
func (s *StatusPageSubscriptionService) publicStatusURL(page *models.StatusPage) string {
	return s.subscriptionBaseURL() + "/status/" + url.PathEscape(page.Slug)
}

// subscriptionLink builds an absolute link to one of the two handshake endpoints.
func (s *StatusPageSubscriptionService) subscriptionLink(page *models.StatusPage, action, token string) string {
	return s.subscriptionBaseURL() + "/api/public/status/" + url.PathEscape(page.Slug) + "/" + action +
		"?token=" + url.QueryEscape(token)
}

// sendSubscriptionConfirmation mails the double opt-in link. The unsubscribe
// link is included too, so a confirmation message is never a dead end.
func (s *StatusPageSubscriptionService) sendSubscriptionConfirmation(ctx context.Context, subscriberID uint, page *models.StatusPage, token string) error {
	if s.notifications == nil {
		return errSubscriptionMail
	}
	var subscriber models.StatusPageSubscriber
	if err := s.db.WithContext(ctx).First(&subscriber, subscriberID).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return errSubscriptionMail
		}
		return err
	}
	unsubscribeToken := buildUnsubscribeToken(s.unsubscribeSecret(ctx), subscriber.UUID)
	mail := i18n.SubscriptionEmailMessage(subscriber.Locale, i18n.SubscriptionMailConfirm, i18n.SubscriptionEmailData{
		PageTitle:      page.Title,
		StatusURL:      s.subscriptionLink(page, "confirm", token),
		UnsubscribeURL: s.subscriptionLink(page, "unsubscribe", unsubscribeToken),
	})
	return s.notifications.SendEmailTo(ctx, page.SubscriptionSMTPUUID, subscriber.Email, mail.Subject, notify.Message{
		Title:    mail.Subject,
		TextBody: mail.Text,
		HTMLBody: mail.HTML,
	})
}

// sendSubscriptionNotice renders and sends one incident mail.
func (s *StatusPageSubscriptionService) sendSubscriptionNotice(ctx context.Context, page *models.StatusPage,
	subscriber *models.StatusPageSubscriber, monitor *models.Monitor, notice *models.SubscriptionNotice,
	unsubscribeToken string, now time.Time) error {

	kind := i18n.SubscriptionMailUp
	if notice.Kind == models.SubscriptionNoticeDown {
		kind = i18n.SubscriptionMailDown
	}
	detail := ""
	var state models.MonitorState
	if err := s.db.WithContext(ctx).Where("monitor_id = ?", monitor.ID).First(&state).Error; err == nil {
		detail = state.LastMessage
	}
	mail := i18n.SubscriptionEmailMessage(subscriber.Locale, kind, i18n.SubscriptionEmailData{
		PageTitle:      page.Title,
		MonitorName:    monitor.Name,
		Detail:         detail,
		Time:           now.UTC().Format(time.RFC1123),
		StatusURL:      s.publicStatusURL(page),
		UnsubscribeURL: s.subscriptionLink(page, "unsubscribe", unsubscribeToken),
	})
	if mail.Subject == "" {
		return fmt.Errorf("the subscription mail could not be rendered")
	}
	return s.notifications.SendEmailTo(ctx, page.SubscriptionSMTPUUID, subscriber.Email, mail.Subject, notify.Message{
		Title:    mail.Subject,
		TextBody: mail.Text,
		HTMLBody: mail.HTML,
	})
}
