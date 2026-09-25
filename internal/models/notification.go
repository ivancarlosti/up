package models

import (
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// NotificationType is the delivery channel kind. SMTP and Webhook are the
// generic transports; Slack, Discord and Telegram are the chat services. Every
// one of them is executed by the shoutrrr engine.
type NotificationType string

// Supported notification types.
const (
	NotificationSMTP     NotificationType = "smtp"
	NotificationWebhook  NotificationType = "webhook"
	NotificationSlack    NotificationType = "slack"
	NotificationDiscord  NotificationType = "discord"
	NotificationTelegram NotificationType = "telegram"
)

// AllNotificationTypes lists the channels offered to the operator.
func AllNotificationTypes() []NotificationType {
	return []NotificationType{
		NotificationSMTP, NotificationWebhook, NotificationSlack, NotificationDiscord, NotificationTelegram,
	}
}

// Notification is a delivery channel that can be linked to any number of
// monitors.
type Notification struct {
	ID uint `gorm:"primaryKey" json:"id"`
	// --- Sync identity (federated clustering) -----------------------------
	//
	// Channels are synchronised by default in federated mode because the node
	// that owns the notification election (the leader) is the one that sends: a
	// leader without the channel would drop the alert silently. The uuid is
	// nullable for the same reason as on monitors (see models.Monitor): the
	// column is added to a table that already has rows and MySQL refuses a
	// unique index over several empty strings.
	UUID         string `gorm:"size:36;uniqueIndex" json:"uuid"`
	OriginNodeID string `gorm:"size:64" json:"origin_node_id"`
	Revision     int64  `gorm:"not null;default:1" json:"revision"`

	Name   string           `gorm:"size:150;not null" json:"name"`
	Type   NotificationType `gorm:"size:20;not null;index" json:"type"`
	Active bool             `gorm:"not null;default:true" json:"active"`
	// IsDefault marks a channel that is pre-selected when creating monitors.
	IsDefault bool `gorm:"not null;default:false" json:"is_default"`
	// ResendIntervalSeconds re-notifies while the monitor keeps the same
	// status (0 = notify only on transitions).
	ResendIntervalSeconds int `gorm:"not null;default:0" json:"resend_interval_seconds"`

	Config NotificationConfig `gorm:"serializer:json;type:json" json:"config"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`

	// MonitorIDs is filled by the service layer for the API responses.
	MonitorIDs []uint `gorm:"-" json:"monitor_ids"`
}

// BeforeCreate fills the sync identity of a new channel (see models.Monitor).
func (n *Notification) BeforeCreate(tx *gorm.DB) error {
	if n.UUID == "" {
		n.UUID = uuid.NewString()
	}
	if n.Revision == 0 {
		n.Revision = 1
	}
	return nil
}

// NotificationConfig groups the per-type settings. Only the block matching the
// notification type is used.
type NotificationConfig struct {
	SMTP     *SMTPConfig     `json:"smtp,omitempty"`
	Webhook  *WebhookConfig  `json:"webhook,omitempty"`
	Slack    *SlackConfig    `json:"slack,omitempty"`
	Discord  *DiscordConfig  `json:"discord,omitempty"`
	Telegram *TelegramConfig `json:"telegram,omitempty"`
}

// SMTPConfig describes the SMTP (e-mail) delivery settings.
type SMTPConfig struct {
	Host          string `json:"host"`
	Port          int    `json:"port"`
	Username      string `json:"username"`
	Password      string `json:"password"`
	From          string `json:"from"`
	To            string `json:"to"` // comma separated recipient list
	Secure        bool   `json:"secure"`
	UseHTML       bool   `json:"use_html"`
	SkipTLSVerify bool   `json:"skip_tls_verify"`
	SubjectPrefix string `json:"subject_prefix"`
}

// WebhookConfig describes the HTTP webhook delivery settings. BodyTemplate is
// a Go text/template rendered by Up before handing the payload to shoutrrr.
type WebhookConfig struct {
	URL          string   `json:"url"`
	Method       string   `json:"method"`
	ContentType  string   `json:"content_type"`
	Headers      []Header `json:"headers"`
	BodyTemplate string   `json:"body_template"`
}

// DefaultWebhookBodyTemplate is pre-filled in the UI when a webhook channel is
// created. Every placeholder is documented in docs/notifications.md.
const DefaultWebhookBodyTemplate = `{
  "event": "{{.Event}}",
  "monitor": "{{.MonitorName}}",
  "type": "{{.MonitorType}}",
  "target": "{{.MonitorURL}}",
  "status": "{{.Status}}",
  "message": "{{.Message}}",
  "latency_ms": {{.LatencyMS}},
  "node": "{{.NodeID}}",
  "timestamp": "{{.Timestamp}}"
}`

// SlackConfig describes the Slack delivery settings. The token is either a bot
// token (xoxb-…) or an incoming webhook token (hook:T…-B…-X…).
type SlackConfig struct {
	Token string `json:"token"`
	// Channel is the id (Cxxxxxxxxxx) or the name (#general) to post to.
	Channel string `json:"channel"`
	// BotName and Icon override the app identity of the message.
	BotName string `json:"bot_name"`
	Icon    string `json:"icon"`
	// ThreadTS posts the message as a reply in the thread of that timestamp.
	ThreadTS string `json:"thread_ts"`
}

// DiscordConfig describes the Discord delivery settings. The webhook id and the
// token are the two halves of the webhook URL
// (discord.com/api/webhooks/<webhook_id>/<token>).
type DiscordConfig struct {
	WebhookID string `json:"webhook_id"`
	Token     string `json:"token"`
	Username  string `json:"username"`
	AvatarURL string `json:"avatar_url"`
	ThreadID  string `json:"thread_id"`
}

// TelegramConfig describes the Telegram delivery settings.
type TelegramConfig struct {
	// Token is the bot token from @BotFather (<bot id>:<secret>).
	Token string `json:"token"`
	// Chats is a comma separated list of chat ids, @channel names or
	// "chat id:thread id" pairs.
	Chats string `json:"chats"`
	// ParseMode is how Telegram parses the message body. Every value but None
	// requires the operator to escape the text themselves.
	ParseMode string `json:"parse_mode"`
	// DisableNotification sends the message silently.
	DisableNotification bool `json:"disable_notification"`
	// DisablePreview hides the link previews of the URLs in the message.
	DisablePreview bool `json:"disable_preview"`
}

// TelegramParseModes are the parse modes accepted by the telegram service of
// shoutrrr.
var TelegramParseModes = []string{"None", "Markdown", "HTML", "MarkdownV2"}

// DefaultTelegramParseMode keeps the body plain and lets shoutrrr render the
// message title with its own HTML escaping.
const DefaultTelegramParseMode = "None"

// TelegramParseMode normalises an operator provided parse mode and reports
// whether it is one of TelegramParseModes (an empty string means the default).
func TelegramParseMode(value string) (string, bool) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return DefaultTelegramParseMode, true
	}
	for _, mode := range TelegramParseModes {
		if strings.EqualFold(trimmed, mode) {
			return mode, true
		}
	}
	return "", false
}

// Normalize applies the defaults of the optional fields.
func (n *Notification) Normalize() {
	switch n.Type {
	case NotificationSMTP:
		if n.Config.SMTP == nil {
			n.Config.SMTP = &SMTPConfig{Port: 587}
		}
		if n.Config.SMTP.Port == 0 {
			n.Config.SMTP.Port = 587
		}
	case NotificationWebhook:
		if n.Config.Webhook == nil {
			n.Config.Webhook = &WebhookConfig{Method: "POST", ContentType: "application/json"}
		}
		w := n.Config.Webhook
		w.Method = strings.ToUpper(strings.TrimSpace(w.Method))
		if w.Method == "" {
			w.Method = "POST"
		}
		if strings.TrimSpace(w.ContentType) == "" {
			w.ContentType = "application/json"
		}
		if strings.TrimSpace(w.BodyTemplate) == "" {
			w.BodyTemplate = DefaultWebhookBodyTemplate
		}
		if w.Headers == nil {
			w.Headers = []Header{}
		}
	case NotificationSlack:
		if n.Config.Slack == nil {
			n.Config.Slack = &SlackConfig{}
		}
		s := n.Config.Slack
		s.Token = strings.TrimSpace(s.Token)
		s.Channel = strings.TrimSpace(s.Channel)
		s.BotName = strings.TrimSpace(s.BotName)
		s.Icon = strings.TrimSpace(s.Icon)
		s.ThreadTS = strings.TrimSpace(s.ThreadTS)
	case NotificationDiscord:
		if n.Config.Discord == nil {
			n.Config.Discord = &DiscordConfig{}
		}
		d := n.Config.Discord
		d.WebhookID = strings.TrimSpace(d.WebhookID)
		d.Token = strings.TrimSpace(d.Token)
		d.Username = strings.TrimSpace(d.Username)
		d.AvatarURL = strings.TrimSpace(d.AvatarURL)
		d.ThreadID = strings.TrimSpace(d.ThreadID)
	case NotificationTelegram:
		if n.Config.Telegram == nil {
			n.Config.Telegram = &TelegramConfig{ParseMode: DefaultTelegramParseMode}
		}
		t := n.Config.Telegram
		t.Token = strings.TrimSpace(t.Token)
		t.Chats = strings.Join(SplitList(t.Chats), ",")
		if mode, ok := TelegramParseMode(t.ParseMode); ok {
			t.ParseMode = mode
		}
	}
}

// Validate checks the channel configuration (empty string means "valid").
func (n *Notification) Validate() string {
	if strings.TrimSpace(n.Name) == "" {
		return "name is required"
	}
	switch n.Type {
	case NotificationSMTP:
		if n.Config.SMTP == nil {
			return "config.smtp is required for SMTP notifications"
		}
		s := n.Config.SMTP
		if s.Host == "" {
			return "config.smtp.host is required"
		}
		if s.Port < 1 || s.Port > 65535 {
			return "config.smtp.port must be between 1 and 65535"
		}
		if s.From == "" {
			return "config.smtp.from is required"
		}
		if len(SplitList(s.To)) == 0 {
			return "config.smtp.to requires at least one recipient"
		}
	case NotificationWebhook:
		if n.Config.Webhook == nil {
			return "config.webhook is required for webhook notifications"
		}
		w := n.Config.Webhook
		if !strings.HasPrefix(w.URL, "http://") && !strings.HasPrefix(w.URL, "https://") {
			return "config.webhook.url must start with http:// or https://"
		}
		switch w.Method {
		case "GET", "POST", "PUT", "PATCH", "DELETE":
		default:
			return "config.webhook.method must be GET, POST, PUT, PATCH or DELETE"
		}
	case NotificationSlack:
		if n.Config.Slack == nil {
			return "config.slack is required for Slack notifications"
		}
		s := n.Config.Slack
		if s.Token == "" {
			return "config.slack.token is required"
		}
		if s.Channel == "" {
			return "config.slack.channel is required"
		}
	case NotificationDiscord:
		if n.Config.Discord == nil {
			return "config.discord is required for Discord notifications"
		}
		d := n.Config.Discord
		if d.WebhookID == "" {
			return "config.discord.webhook_id is required"
		}
		if d.Token == "" {
			return "config.discord.token is required"
		}
	case NotificationTelegram:
		if n.Config.Telegram == nil {
			return "config.telegram is required for Telegram notifications"
		}
		t := n.Config.Telegram
		botID, secret, found := strings.Cut(t.Token, ":")
		if !found || strings.TrimSpace(botID) == "" || strings.TrimSpace(secret) == "" {
			return "config.telegram.token must look like <bot id>:<secret>"
		}
		if len(SplitList(t.Chats)) == 0 {
			return "config.telegram.chats requires at least one chat"
		}
		if _, ok := TelegramParseMode(t.ParseMode); !ok {
			return "config.telegram.parse_mode must be None, Markdown, HTML or MarkdownV2"
		}
	default:
		return "type must be smtp, webhook, slack, discord or telegram"
	}
	return ""
}

// EffectiveResendInterval returns the re-notification interval of a channel
// taking the monitor level override into account (the larger value wins, 0
// means "transitions only").
func EffectiveResendInterval(monitorSeconds, notificationSeconds int) int {
	if monitorSeconds > notificationSeconds {
		return monitorSeconds
	}
	return notificationSeconds
}
