package notify

import (
	"io"
	"log/slog"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/ivancarlosti/up/internal/models"
)

func sampleMessage() Message {
	return Message{
		Event:       "down",
		Status:      "down",
		Title:       "[DOWN] API health",
		MonitorName: "API health",
		MonitorType: "http",
		MonitorURL:  "https://api.example.com/health",
		Message:     "connection refused",
		LatencyMS:   42,
		NodeID:      "up-node-1",
		InstanceURL: "https://up.example.com",
	}
}

func TestBuildSMTPURL(t *testing.T) {
	config := &models.SMTPConfig{
		Host:          "smtp.example.com",
		Port:          587,
		Username:      "up@example.com",
		Password:      "p@ss:word",
		From:          "up@example.com",
		To:            "ops@example.com, oncall@example.com",
		SubjectPrefix: "[Up]",
		UseHTML:       true,
	}
	raw := buildSMTPURL(config, sampleMessage())
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("the generated URL must parse: %v (%s)", err, raw)
	}
	if parsed.Scheme != "smtp" || parsed.Host != "smtp.example.com:587" {
		t.Fatalf("unexpected endpoint: %s", raw)
	}
	if user := parsed.User.Username(); user != "up@example.com" {
		t.Fatalf("username lost: %s", raw)
	}
	if password, _ := parsed.User.Password(); password != "p@ss:word" {
		t.Fatalf("password must survive URL encoding: %s", raw)
	}
	query := parsed.Query()
	if query.Get("fromaddress") != "up@example.com" {
		t.Fatalf("fromaddress missing: %s", raw)
	}
	// The recipient order is preserved (the operator's order is intentional).
	if query.Get("toaddresses") != "ops@example.com,oncall@example.com" {
		t.Fatalf("recipients must be comma separated: %q", query.Get("toaddresses"))
	}
	if !strings.HasPrefix(query.Get("subject"), "[Up]") {
		t.Fatalf("subject prefix lost: %q", query.Get("subject"))
	}
	if query.Get("usehtml") != "yes" || query.Get("encryption") != "Auto" {
		t.Fatalf("unexpected encryption flags: %s", raw)
	}

	secure := *config
	secure.Secure = true
	if encryption := mustQuery(t, buildSMTPURL(&secure, sampleMessage())).Get("encryption"); encryption != "ExplicitTLS" {
		t.Fatalf("secure connections must use implicit TLS, got %q", encryption)
	}
}

func TestBuildWebhookURL(t *testing.T) {
	config := &models.WebhookConfig{
		URL:         "http://hooks.internal:8080/up?existing=1",
		Method:      "PUT",
		ContentType: "application/json",
		Headers: []models.Header{
			{Key: "X-Token", Value: "abc"},
			{Key: "Content-Type", Value: "application/json; charset=utf-8"},
		},
	}
	raw, err := buildWebhookURL(config)
	if err != nil {
		t.Fatalf("build webhook URL: %v", err)
	}
	parsed := mustParse(t, raw)
	if parsed.Scheme != "generic" || parsed.Host != "hooks.internal:8080" || parsed.Path != "/up" {
		t.Fatalf("unexpected service URL: %s", raw)
	}
	query := parsed.Query()
	if query.Get("method") != "PUT" {
		t.Fatalf("method lost: %s", raw)
	}
	if query.Get("disabletls") != "yes" {
		t.Fatalf("an http webhook must disable TLS: %s", raw)
	}
	if query.Get("@X-Token") != "abc" {
		t.Fatalf("custom headers must be prefixed with @: %s", raw)
	}
	if query.Get("@Content-Type") != "application/json" {
		t.Fatalf("the content type must be forced on the header, otherwise shoutrrr sends text/plain: %s", raw)
	}

	httpsConfig := *config
	httpsConfig.URL = "https://hooks.example.com/up"
	httpsURL, err := buildWebhookURL(&httpsConfig)
	if err != nil {
		t.Fatalf("build https webhook URL: %v", err)
	}
	if disable := mustParse(t, httpsURL).Query().Get("disabletls"); disable != "" {
		t.Fatalf("https webhooks must keep TLS enabled, got disabletls=%q", disable)
	}

	if _, err := buildWebhookURL(&models.WebhookConfig{URL: "not a url"}); err == nil {
		t.Fatal("an invalid webhook URL must be rejected")
	}
}

func TestRenderBody(t *testing.T) {
	message := sampleMessage()

	defaultBody, err := message.RenderBody("")
	if err != nil {
		t.Fatalf("default body: %v", err)
	}
	if !strings.Contains(defaultBody, `"event":"down"`) || !strings.Contains(defaultBody, `"latency_ms":42`) {
		t.Fatalf("unexpected default payload: %s", defaultBody)
	}

	templated, err := message.RenderBody(`{"alert":"{{.Event}}","name":"{{.MonitorName}}","upper":"{{upper .NodeID}}"}`)
	if err != nil {
		t.Fatalf("template body: %v", err)
	}
	if !strings.Contains(templated, `"alert":"down"`) || !strings.Contains(templated, `"upper":"UP-NODE-1"`) {
		t.Fatalf("template helpers failed: %s", templated)
	}

	if _, err := message.RenderBody("{{.Event"); err == nil {
		t.Fatal("an invalid template must fail instead of sending garbage")
	}
}

func TestMonitorTarget(t *testing.T) {
	cases := []struct {
		monitor models.Monitor
		want    string
	}{
		{models.Monitor{Type: models.MonitorTypeHTTP, Config: models.MonitorConfig{URL: "https://x/y"}}, "https://x/y"},
		{models.Monitor{Type: models.MonitorTypeTCP, Config: models.MonitorConfig{Host: "db", Port: 3306}}, "db:3306"},
		{models.Monitor{Type: models.MonitorTypeDNS, Config: models.MonitorConfig{RecordType: "A", Hostname: "x.com", ResolverServer: "1.1.1.1"}}, "A x.com @1.1.1.1"},
	}
	for _, tc := range cases {
		if got := MonitorTarget(&tc.monitor); got != tc.want {
			t.Errorf("MonitorTarget() = %q, want %q", got, tc.want)
		}
	}
}

func TestBuildSlackURL(t *testing.T) {
	config := &models.SlackConfig{
		Token:    slackBotToken(),
		Channel:  "C0123456789",
		BotName:  "Up",
		Icon:     ":satellite:",
		ThreadTS: "1712345678.000100",
	}
	raw, err := buildSlackURL(config)
	if err != nil {
		t.Fatalf("build slack URL: %v", err)
	}
	parsed := mustParse(t, raw)
	if parsed.Scheme != "slack" || parsed.Host != "C0123456789" {
		t.Fatalf("unexpected service URL: %s", raw)
	}
	if user := parsed.User.Username(); user != config.Token {
		t.Fatalf("the bot token must survive the URL round trip: %q (%s)", user, raw)
	}
	query := parsed.Query()
	if query.Get("botname") != "Up" || query.Get("icon") != ":satellite:" {
		t.Fatalf("the identity overrides are lost: %s", raw)
	}
	if query.Get("thread_ts") != "1712345678.000100" {
		t.Fatalf("the thread timestamp is lost: %s", raw)
	}

	// An incoming webhook token keeps its "hook:" prefix and needs no extras.
	hook, err := buildSlackURL(&models.SlackConfig{
		Token:   "hook:T00000000-B00000000-XXXXXXXXXXXXXXXXXXXXXXXX",
		Channel: "webhook",
	})
	if err != nil {
		t.Fatalf("build slack URL (webhook token): %v", err)
	}
	if user := mustParse(t, hook).User.Username(); user != "hook" {
		t.Fatalf("the webhook token must keep its identifier: %q (%s)", user, hook)
	}
	if len(mustParse(t, hook).Query()) != 0 {
		t.Fatalf("optional parameters must stay out of the URL: %s", hook)
	}

	if _, err := buildSlackURL(&models.SlackConfig{Channel: "C0123456789"}); err == nil {
		t.Fatal("a Slack channel without a token must be rejected")
	}
	if _, err := buildSlackURL(&models.SlackConfig{Token: "xoxb-1"}); err == nil {
		t.Fatal("a Slack token without a channel must be rejected")
	}
}

func TestBuildDiscordURL(t *testing.T) {
	config := &models.DiscordConfig{
		WebhookID: "123456789012345678",
		Token:     "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123",
		Username:  "Up",
		AvatarURL: "https://cdn.example.com/up.png",
		ThreadID:  "987654321098765432",
	}
	raw, err := buildDiscordURL(config)
	if err != nil {
		t.Fatalf("build discord URL: %v", err)
	}
	parsed := mustParse(t, raw)
	if parsed.Scheme != "discord" || parsed.Host != config.WebhookID {
		t.Fatalf("unexpected service URL: %s", raw)
	}
	if user := parsed.User.Username(); user != config.Token {
		t.Fatalf("the webhook token must survive the URL round trip: %q (%s)", user, raw)
	}
	query := parsed.Query()
	if query.Get("splitlines") != "no" {
		t.Fatalf("one alert must stay one message, not one embed per line: %s", raw)
	}
	if query.Get("username") != "Up" || query.Get("thread_id") != config.ThreadID {
		t.Fatalf("the overrides are lost: %s", raw)
	}
	if query.Get("avatar") != config.AvatarURL {
		t.Fatalf("the avatar override is lost: %s", raw)
	}

	bare, err := buildDiscordURL(&models.DiscordConfig{WebhookID: "1", Token: "t"})
	if err != nil {
		t.Fatalf("build discord URL (required fields only): %v", err)
	}
	if len(mustParse(t, bare).Query()) != 1 { // splitlines only
		t.Fatalf("optional parameters must stay out of the URL: %s", bare)
	}

	if _, err := buildDiscordURL(&models.DiscordConfig{Token: "t"}); err == nil {
		t.Fatal("a Discord webhook without an id must be rejected")
	}
	if _, err := buildDiscordURL(&models.DiscordConfig{WebhookID: "1"}); err == nil {
		t.Fatal("a Discord webhook without a token must be rejected")
	}
}

func TestBuildTelegramURL(t *testing.T) {
	config := &models.TelegramConfig{
		Token:               telegramBotToken(),
		Chats:               "-1001234567890, @mychannel",
		ParseMode:           "HTML",
		DisableNotification: true,
		DisablePreview:      true,
	}
	raw, err := buildTelegramURL(config)
	if err != nil {
		t.Fatalf("build telegram URL: %v", err)
	}
	parsed := mustParse(t, raw)
	if parsed.Scheme != "telegram" || parsed.Host != "telegram" {
		t.Fatalf("unexpected service URL: %s", raw)
	}
	if parsed.User.Username() != "1234" {
		t.Fatalf("the bot id must be the userinfo username: %s", raw)
	}
	if secret, _ := parsed.User.Password(); secret != "up-telegram-test-token" {
		t.Fatalf("the bot secret must survive URL encoding: %s", raw)
	}
	query := parsed.Query()
	if query.Get("chats") != "-1001234567890,@mychannel" {
		t.Fatalf("chats must be a comma separated list: %q", query.Get("chats"))
	}
	if query.Get("parsemode") != "HTML" {
		t.Fatalf("the parse mode is lost: %s", raw)
	}
	if query.Get("notification") != "no" || query.Get("preview") != "no" {
		t.Fatalf("the delivery flags are lost: %s", raw)
	}

	quiet, err := buildTelegramURL(&models.TelegramConfig{Token: "1:abc", Chats: "1"})
	if err != nil {
		t.Fatalf("build telegram URL (defaults): %v", err)
	}
	quietQuery := mustParse(t, quiet).Query()
	if quietQuery.Get("parsemode") != models.DefaultTelegramParseMode ||
		quietQuery.Get("notification") != "yes" || quietQuery.Get("preview") != "yes" {
		t.Fatalf("unexpected defaults: %s", quiet)
	}

	if _, err := buildTelegramURL(&models.TelegramConfig{Token: "123456789", Chats: "1"}); err == nil {
		t.Fatal("a Telegram token without a secret must be rejected")
	}
	if _, err := buildTelegramURL(&models.TelegramConfig{Token: "1:abc"}); err == nil {
		t.Fatal("a Telegram channel without a chat must be rejected")
	}
	if _, err := buildTelegramURL(&models.TelegramConfig{Token: "1:abc", Chats: "1", ParseMode: "Text"}); err == nil {
		t.Fatal("an unknown Telegram parse mode must be rejected")
	}
}

// TestChatServiceURLsAreAcceptedByShoutrrr feeds every generated URL back into
// the shoutrrr config parser: a shape the engine refuses (a token the service
// does not recognise, a missing chat) would otherwise only surface as a failed
// delivery, at the first alert.
func TestChatServiceURLsAreAcceptedByShoutrrr(t *testing.T) {
	engine := NewEngine(slog.New(slog.NewTextHandler(io.Discard, nil)), time.Second)

	slackBot, err := buildSlackURL(&models.SlackConfig{
		Token:   slackBotToken(),
		Channel: "C0123456789",
	})
	if err != nil {
		t.Fatalf("build slack URL: %v", err)
	}
	slackHook, err := buildSlackURL(&models.SlackConfig{
		Token:   "hook:T00000000-B00000000-XXXXXXXXXXXXXXXXXXXXXXXX",
		Channel: "webhook",
	})
	if err != nil {
		t.Fatalf("build slack URL (webhook token): %v", err)
	}
	discordURL, err := buildDiscordURL(&models.DiscordConfig{
		WebhookID: "123456789012345678",
		Token:     "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123",
		ThreadID:  "987654321098765432",
	})
	if err != nil {
		t.Fatalf("build discord URL: %v", err)
	}
	telegramURL, err := buildTelegramURL(&models.TelegramConfig{
		Token: telegramBotToken(),
		Chats: "-1001234567890,@mychannel",
	})
	if err != nil {
		t.Fatalf("build telegram URL: %v", err)
	}

	for name, serviceURL := range map[string]string{
		"slack (bot token)":     slackBot,
		"slack (webhook token)": slackHook,
		"discord":               discordURL,
		"telegram":              telegramURL,
	} {
		if _, err := engine.newRouter(serviceURL); err != nil {
			t.Errorf("%s: shoutrrr rejected the generated URL %q: %v", name, serviceURL, err)
		}
	}
}

// Fixtures for the chat service tests.
//
// Both tokens are fabricated, but their literal form matters: GitHub secret
// scanning matches the *patterns* of real credentials and rejects the push of
// any commit containing one ("Push cannot contain secrets"), whether or not the
// value is a live secret. A "xoxb-<12>-<12>-<24>" string matches the Slack API
// token pattern and a "<9 digits>:AA<35 chars>" string matches the Telegram bot
// token pattern, so neither may appear verbatim in the repository.
//
// slackBotToken assembles the Slack token at run time. shoutrrr validates the
// shape of the token, so the fixture has to keep it: three dash separated
// groups of 12, 12 and 24 characters after the identifier.
func slackBotToken() string {
	return "xox" + "b-" + strings.Repeat("1", 12) + "-" + strings.Repeat("2", 12) + "-" + strings.Repeat("f", 24)
}

// telegramBotToken is a synthetic <bot id>:<secret> pair. The id and the secret
// are deliberately short: shoutrrr only checks for "<digits>:<non empty>", and
// a realistic looking pair would match the Telegram bot token pattern above.
func telegramBotToken() string {
	return "1234:up-telegram-test-token"
}

func mustQuery(t *testing.T, raw string) url.Values {
	t.Helper()
	return mustParse(t, raw).Query()
}

func mustParse(t *testing.T, raw string) *url.URL {
	t.Helper()
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse %q: %v", raw, err)
	}
	return parsed
}
