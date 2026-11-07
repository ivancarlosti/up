package models

import (
	"strings"
	"testing"
)

func TestTelegramParseMode(t *testing.T) {
	cases := []struct {
		in   string
		want string
		ok   bool
	}{
		{"", DefaultTelegramParseMode, true},
		{"   ", DefaultTelegramParseMode, true},
		{"html", "HTML", true},
		{"MarkdownV2", "MarkdownV2", true},
		{"none", "None", true},
		{"Text", "", false},
	}
	for _, tc := range cases {
		got, ok := TelegramParseMode(tc.in)
		if got != tc.want || ok != tc.ok {
			t.Errorf("TelegramParseMode(%q) = (%q, %v), want (%q, %v)", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}

func TestNotificationValidateChatChannels(t *testing.T) {
	cases := []struct {
		name         string
		notification Notification
		want         string
	}{
		{
			name: "slack",
			notification: Notification{Name: "ops", Type: NotificationSlack, Config: NotificationConfig{
				Slack: &SlackConfig{Token: "xoxb-1", Channel: "C1"},
			}},
		},
		{
			name: "discord",
			notification: Notification{Name: "ops", Type: NotificationDiscord, Config: NotificationConfig{
				Discord: &DiscordConfig{WebhookID: "1", Token: "t"},
			}},
		},
		{
			name: "telegram",
			notification: Notification{Name: "ops", Type: NotificationTelegram, Config: NotificationConfig{
				Telegram: &TelegramConfig{Token: "123456789:secret", Chats: "@ops"},
			}},
		},
		{
			name:         "slack without config",
			notification: Notification{Name: "ops", Type: NotificationSlack},
			want:         "config.slack is required for Slack notifications",
		},
		{
			name: "slack without channel",
			notification: Notification{Name: "ops", Type: NotificationSlack, Config: NotificationConfig{
				Slack: &SlackConfig{Token: "xoxb-1"},
			}},
			want: "config.slack.channel is required",
		},
		{
			name:         "discord without config",
			notification: Notification{Name: "ops", Type: NotificationDiscord},
			want:         "config.discord is required for Discord notifications",
		},
		{
			name: "discord without token",
			notification: Notification{Name: "ops", Type: NotificationDiscord, Config: NotificationConfig{
				Discord: &DiscordConfig{WebhookID: "1"},
			}},
			want: "config.discord.token is required",
		},
		{
			name:         "telegram without config",
			notification: Notification{Name: "ops", Type: NotificationTelegram},
			want:         "config.telegram is required for Telegram notifications",
		},
		{
			name: "telegram token without secret",
			notification: Notification{Name: "ops", Type: NotificationTelegram, Config: NotificationConfig{
				Telegram: &TelegramConfig{Token: "123456789", Chats: "@ops"},
			}},
			want: "config.telegram.token must look like <bot id>:<secret>",
		},
		{
			name: "telegram without chats",
			notification: Notification{Name: "ops", Type: NotificationTelegram, Config: NotificationConfig{
				Telegram: &TelegramConfig{Token: "123456789:secret"},
			}},
			want: "config.telegram.chats requires at least one chat",
		},
		{
			name: "telegram with an unknown parse mode",
			notification: Notification{Name: "ops", Type: NotificationTelegram, Config: NotificationConfig{
				Telegram: &TelegramConfig{Token: "123456789:secret", Chats: "@ops", ParseMode: "Text"},
			}},
			want: "config.telegram.parse_mode must be None, Markdown, HTML or MarkdownV2",
		},
		{
			name:         "unknown type",
			notification: Notification{Name: "ops", Type: "carrier-pigeon"},
			want:         "type must be smtp, webhook, slack, discord or telegram",
		},
	}
	for _, tc := range cases {
		if got := tc.notification.Validate(); got != tc.want {
			t.Errorf("%s: Validate() = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// TestEffectiveResendInterval pins the rule behind the re-notification clock: a
// channel may ask to repeat more often than its monitor does, never less often.
// The value is what shouldResend (services/cluster_notify.go) compares against the
// time that passed since the incident was reported.
func TestEffectiveResendInterval(t *testing.T) {
	cases := []struct {
		name           string
		monitorSeconds int
		channelSeconds int
		want           int
	}{
		{name: "transitions only when neither asks to repeat", monitorSeconds: 0, channelSeconds: 0, want: 0},
		{name: "the monitor interval applies when the channel is silent", monitorSeconds: 300, channelSeconds: 0, want: 300},
		{name: "the channel interval applies when the monitor is silent", monitorSeconds: 0, channelSeconds: 300, want: 300},
		{name: "the larger value wins", monitorSeconds: 600, channelSeconds: 300, want: 600},
		{name: "an equal interval is kept", monitorSeconds: 300, channelSeconds: 300, want: 300},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := EffectiveResendInterval(tc.monitorSeconds, tc.channelSeconds); got != tc.want {
				t.Errorf("EffectiveResendInterval(%d, %d) = %d, want %d",
					tc.monitorSeconds, tc.channelSeconds, got, tc.want)
			}
		})
	}
}

func TestNotificationNormalizeChatChannels(t *testing.T) {
	telegram := Notification{Name: "ops", Type: NotificationTelegram}
	telegram.Normalize()
	if telegram.Config.Telegram == nil {
		t.Fatal("the telegram block must be created")
	}
	if telegram.Config.Telegram.ParseMode != DefaultTelegramParseMode {
		t.Fatalf("the parse mode must default to %q, got %q",
			DefaultTelegramParseMode, telegram.Config.Telegram.ParseMode)
	}

	spaced := Notification{Name: "ops", Type: NotificationTelegram, Config: NotificationConfig{
		Telegram: &TelegramConfig{Token: " 123456789:secret ", Chats: " -1001 , @ops ,", ParseMode: "html"},
	}}
	spaced.Normalize()
	if spaced.Config.Telegram.Token != "123456789:secret" {
		t.Fatalf("the token must be trimmed: %q", spaced.Config.Telegram.Token)
	}
	if spaced.Config.Telegram.Chats != "-1001,@ops" {
		t.Fatalf("the chat list must be normalised: %q", spaced.Config.Telegram.Chats)
	}
	if spaced.Config.Telegram.ParseMode != "HTML" {
		t.Fatalf("the parse mode must be canonicalised: %q", spaced.Config.Telegram.ParseMode)
	}

	slack := Notification{Name: "ops", Type: NotificationSlack, Config: NotificationConfig{
		Slack: &SlackConfig{Token: " xoxb-1 ", Channel: " C1 "},
	}}
	slack.Normalize()
	if slack.Config.Slack.Token != "xoxb-1" || slack.Config.Slack.Channel != "C1" {
		t.Fatalf("the Slack fields must be trimmed: %+v", slack.Config.Slack)
	}
	if problem := slack.Validate(); problem != "" {
		t.Fatalf("a normalised Slack channel must validate: %s", problem)
	}

	discord := Notification{Name: "ops", Type: NotificationDiscord}
	discord.Normalize()
	if discord.Config.Discord == nil {
		t.Fatal("the discord block must be created")
	}
}

// TestUpgradeWebhookBodyTemplate pins the repair rule of an existing channel: the
// block a release older than the json helper shipped is replaced with the current
// default, and everything else is returned byte for byte. The second half matters
// as much as the first - a template the operator wrote is never rewritten, because
// Up cannot tell which of its lines were meant as JSON.
func TestUpgradeWebhookBodyTemplate(t *testing.T) {
	if len(legacyWebhookBodyTemplates) == 0 {
		t.Fatal("the legacy snapshots are the whole point of this test")
	}
	for i, legacy := range legacyWebhookBodyTemplates {
		// Every snapshot has to be the broken shape: if one of them ever got the
		// json helper, this list would silently stop matching the rows it exists
		// for.
		if strings.Contains(legacy, "{{json .Message}}") || !strings.Contains(legacy, `"{{.Message}}"`) {
			t.Fatalf("legacy snapshot %d is not the raw interpolation it stands for:\n%s", i, legacy)
		}
		if got := UpgradeWebhookBodyTemplate(legacy); got != DefaultWebhookBodyTemplate {
			t.Errorf("legacy snapshot %d was not upgraded, got:\n%s", i, got)
		}
		// The same block after a round trip through a textarea, a JSON client or
		// a Windows editor is still the same block.
		mangled := "\r\n\t" + strings.ReplaceAll(legacy, "\n  ", "\r\n\t") + "  \r\n\r\n"
		if got := UpgradeWebhookBodyTemplate(mangled); got != DefaultWebhookBodyTemplate {
			t.Errorf("legacy snapshot %d was not upgraded from its CRLF and re-indented form", i)
		}
	}

	untouched := []struct {
		name     string
		template string
	}{
		{"the current default", DefaultWebhookBodyTemplate},
		{"a custom escaped template", `{"message": {{json .Message}}}`},
		{"a custom raw template", `{"message": "{{.Message}}"}`},
		{
			name:     "a copy of the default with an extra field",
			template: strings.Replace(DefaultWebhookBodyTemplate, `"latency_ms"`, `"extra": {{json .NodeID}}, "latency_ms"`, 1),
		},
		{"a blank template", "  \n\t "},
		{"an empty template", ""},
	}
	for _, tc := range untouched {
		if got := UpgradeWebhookBodyTemplate(tc.template); got != tc.template {
			t.Errorf("%s must be returned verbatim, got:\n%s", tc.name, got)
		}
	}
}

// TestNotificationAfterFindUpgradesLegacyWebhookTemplate pins the read path: a row
// written by an older release is healed in memory on every load, so the UI, the
// test button and the dispatch stop using the raw block before the backfill has
// rewritten the row.
func TestNotificationAfterFindUpgradesLegacyWebhookTemplate(t *testing.T) {
	legacy := &Notification{Name: "n8n", Type: NotificationWebhook, Config: NotificationConfig{
		Webhook: &WebhookConfig{URL: "https://n8n.example.com/hook", BodyTemplate: legacyWebhookBodyTemplates[0]},
	}}
	if err := legacy.AfterFind(nil); err != nil {
		t.Fatalf("AfterFind: %v", err)
	}
	if got := legacy.Config.Webhook.BodyTemplate; got != DefaultWebhookBodyTemplate {
		t.Errorf("the stored default must be upgraded on read, got:\n%s", got)
	}

	// A template the operator wrote is exactly what must survive the hook.
	custom := `{"message": "{{.Message}}"}`
	own := &Notification{Type: NotificationWebhook, Config: NotificationConfig{
		Webhook: &WebhookConfig{BodyTemplate: custom},
	}}
	if err := own.AfterFind(nil); err != nil {
		t.Fatalf("AfterFind: %v", err)
	}
	if got := own.Config.Webhook.BodyTemplate; got != custom {
		t.Errorf("a custom template must be kept verbatim, got:\n%s", got)
	}

	// A channel of another type has no webhook block, and the hook must not
	// invent one: that block is what Normalize fills on a save.
	smtp := &Notification{Type: NotificationSMTP, Config: NotificationConfig{SMTP: &SMTPConfig{Host: "smtp.example.com"}}}
	if err := smtp.AfterFind(nil); err != nil {
		t.Fatalf("AfterFind: %v", err)
	}
	if smtp.Config.Webhook != nil {
		t.Error("AfterFind must not add a webhook block to a channel of another type")
	}
}

// TestNotificationNormalizeUpgradesLegacyWebhookTemplate pins the write path: the
// save of a channel that still carries a shipped default stores the escaped block,
// while a template the operator wrote is stored as it was sent.
func TestNotificationNormalizeUpgradesLegacyWebhookTemplate(t *testing.T) {
	notification := Notification{Name: "n8n", Type: NotificationWebhook, Config: NotificationConfig{
		Webhook: &WebhookConfig{URL: "https://n8n.example.com/hook", BodyTemplate: legacyWebhookBodyTemplates[0]},
	}}
	notification.Normalize()
	if got := notification.Config.Webhook.BodyTemplate; got != DefaultWebhookBodyTemplate {
		t.Errorf("a stored default must be upgraded on save, got:\n%s", got)
	}

	custom := `{"text": {{json .Message}}}`
	notification.Config.Webhook.BodyTemplate = custom
	notification.Normalize()
	if got := notification.Config.Webhook.BodyTemplate; got != custom {
		t.Errorf("a custom template must be stored verbatim, got:\n%s", got)
	}

	notification.Config.Webhook.BodyTemplate = "   "
	notification.Normalize()
	if got := notification.Config.Webhook.BodyTemplate; got != DefaultWebhookBodyTemplate {
		t.Errorf("a blank template must fall back to the default, got:\n%s", got)
	}
}
