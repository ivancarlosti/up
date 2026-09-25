package models

import "testing"

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
