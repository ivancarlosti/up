package models

import (
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// Subscription notice kinds. They are the two mails a subscriber receives per
// monitor incident (the confirmation is part of the handshake, not a notice).
const (
	SubscriptionNoticeDown = "down"
	SubscriptionNoticeUp   = "up"
)

// Subscription notice states. "pending" covers a notice waiting for its
// scheduled moment and one waiting for a retry; next_attempt_at tells them apart.
const (
	SubscriptionNoticePending = "pending"
	SubscriptionNoticeSending = "sending"
	SubscriptionNoticeSent    = "sent"
	SubscriptionNoticeSkipped = "skipped"
	SubscriptionNoticeFailed  = "failed"
)

// Subscription delay limits (minutes). The minimum is deliberately above zero:
// a page that mails within seconds of a blip would publish every retry the
// monitor makes, and the delay is the whole point of the opt-in.
const (
	DefaultSubscriptionDelayMinutes = 10
	MinSubscriptionDelayMinutes     = 5
	MaxSubscriptionDelayMinutes     = 1440
	SubscriptionDelayStepMinutes    = 5
)

// NormalizeSubscriptionDelayMinutes clamps a configured delay to the range the
// UI offers. Zero means "use the default": an omitted field in an older API call
// must not turn the delay off.
func NormalizeSubscriptionDelayMinutes(value int) int {
	if value <= 0 {
		return DefaultSubscriptionDelayMinutes
	}
	if value < MinSubscriptionDelayMinutes {
		value = MinSubscriptionDelayMinutes
	}
	// Round to the nearest step (7 -> 5, 8 -> 10). The form offers only the
	// steps, but a hand written API call can send anything.
	value = ((value + SubscriptionDelayStepMinutes/2) / SubscriptionDelayStepMinutes) * SubscriptionDelayStepMinutes
	if value < MinSubscriptionDelayMinutes {
		value = MinSubscriptionDelayMinutes
	}
	if value > MaxSubscriptionDelayMinutes {
		value = MaxSubscriptionDelayMinutes
	}
	return value
}

// StatusPageSubscriber is one e-mail address subscribed to the incident mails of
// a status page.
//
// The identity is derived from the page uuid and the lower-cased address
// (StatusPageSubscriberUUID), so every node of a federated cluster names the same
// subscription without coordinating. The row is RETAINED after an unsubscribe:
// deleting it would let a later subscribe request mail the address again as if
// nothing had happened, and the operator would lose who opted out.
type StatusPageSubscriber struct {
	ID uint `gorm:"primaryKey" json:"id"`
	// --- Sync identity (federated clustering) -----------------------------
	//
	// Subscribers replicate like any other federated entity. The payload carries
	// PII (the address) and is protected by the peer transport; docs/clustering-
	// modes.md spells that out.
	UUID         string `gorm:"size:36;uniqueIndex" json:"uuid"`
	OriginNodeID string `gorm:"size:64" json:"origin_node_id"`
	Revision     int64  `gorm:"not null;default:1" json:"revision"`

	StatusPageID uint   `gorm:"not null;uniqueIndex:idx_status_page_subscriber,priority:1" json:"status_page_id"`
	Email        string `gorm:"size:320;not null;uniqueIndex:idx_status_page_subscriber,priority:2" json:"email"`
	// Locale is the language of the mails, chosen by the subscriber (one of
	// config.SupportedLocales).
	Locale string `gorm:"size:12;not null;default:en-US" json:"locale"`

	// ConfirmedAt is nil until the double opt-in link is visited. Only a
	// confirmed subscriber receives incident mails.
	ConfirmedAt *time.Time `json:"confirmed_at"`
	// UnsubscribedAt is set by the unsubscribe link and cleared by a new
	// confirmation. A row with both timestamps set opted out after confirming.
	UnsubscribedAt *time.Time `json:"unsubscribed_at"`

	// ConfirmationSentAt is the last confirmation mail, used for the resend
	// cooldown (SubscriptionConfirmationCooldown).
	ConfirmationSentAt *time.Time `json:"confirmation_sent_at"`
	// ConfirmTokenHash is the SHA-256 of the single-use confirmation token. The
	// plain token exists only in the mail.
	ConfirmTokenHash      string     `gorm:"size:64;index" json:"-"`
	ConfirmTokenExpiresAt *time.Time `json:"-"`

	// UnsubscribeTokenHash is the SHA-256 of the unsubscribe token. The token is
	// deterministic (page uuid + address + the shared cluster key), so every node
	// can put the same link in a mail without storing the secret in clear text.
	UnsubscribeTokenHash string `gorm:"size:64" json:"-"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// TableName keeps the table name stable.
func (StatusPageSubscriber) TableName() string { return "status_page_subscribers" }

// BeforeCreate fills the sync identity of a new subscriber.
func (s *StatusPageSubscriber) BeforeCreate(tx *gorm.DB) error {
	if s.UUID == "" {
		s.UUID = uuid.NewString()
	}
	if s.Revision == 0 {
		s.Revision = 1
	}
	if s.Locale == "" {
		s.Locale = "en-US"
	}
	return nil
}

// StatusPageSubscriberUUID returns the deterministic identity of a subscription.
//
// The address is lower-cased and trimmed first: two spellings of the same
// address must not become two subscribers (which would send two copies of every
// incident).
func StatusPageSubscriberUUID(pageUUID, email string) string {
	return LinkEntityUUID(EntityStatusPageSubscriber, pageUUID, strings.ToLower(strings.TrimSpace(email)))
}

// SubscriptionIncident is one open (or closed) outage of one monitor on one
// status page. Its unique open key enforces a single open incident per
// (page, monitor) even when two nodes observe the same transition.
type SubscriptionIncident struct {
	ID uint `gorm:"primaryKey" json:"id"`

	StatusPageID uint `gorm:"not null;index:idx_subscription_incident_monitor,priority:1" json:"status_page_id"`
	MonitorID    uint `gorm:"not null;index:idx_subscription_incident_monitor,priority:2" json:"monitor_id"`

	// OpenKey is "pageID:monitorID" while the incident is open and NULL once it
	// closes. A unique index over a nullable column lets MySQL hold many closed
	// rows and exactly one open one.
	OpenKey *string `gorm:"size:80;uniqueIndex" json:"-"`

	OpenedAt time.Time  `json:"opened_at"`
	ClosedAt *time.Time `json:"closed_at"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// TableName keeps the table name stable.
func (SubscriptionIncident) TableName() string { return "status_page_subscription_incidents" }

// SubscriptionNotice is one queued incident mail. The unique key over
// (incident, subscriber, kind) is what makes "exactly one down and one up per
// incident" true under retries, concurrent scheduler passes and a cluster.
type SubscriptionNotice struct {
	ID uint `gorm:"primaryKey" json:"id"`

	IncidentID   uint   `gorm:"not null;uniqueIndex:idx_subscription_notice,priority:1" json:"incident_id"`
	SubscriberID uint   `gorm:"not null;uniqueIndex:idx_subscription_notice,priority:2" json:"subscriber_id"`
	Kind         string `gorm:"size:8;not null;uniqueIndex:idx_subscription_notice,priority:3" json:"kind"`

	// StatusPageID and MonitorID are denormalised so the due query does not have
	// to join the incident for every row.
	StatusPageID uint `gorm:"not null;index" json:"status_page_id"`
	MonitorID    uint `gorm:"not null;index" json:"monitor_id"`

	Status string `gorm:"size:12;not null;default:pending;index" json:"status"`
	// ScheduledAt is when the mail may leave: opened_at + delay for a down mail,
	// immediately for the recovery.
	ScheduledAt   time.Time  `json:"scheduled_at"`
	NextAttemptAt *time.Time `gorm:"index" json:"next_attempt_at"`
	ClaimedAt     *time.Time `json:"claimed_at"`
	SentAt        *time.Time `json:"sent_at"`
	Attempts      int        `gorm:"not null;default:0" json:"attempts"`
	LastError     string     `gorm:"size:1000" json:"last_error"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// TableName keeps the table name stable.
func (SubscriptionNotice) TableName() string { return "status_page_subscription_notices" }
