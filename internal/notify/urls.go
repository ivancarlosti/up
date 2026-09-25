package notify

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"

	"github.com/ivancarlosti/up/internal/models"
)

// buildSMTPURL translates the SMTP settings into the shoutrrr service URL:
//
//	smtp://user:pass@host:port/?fromaddress=...&toaddresses=a,b&subject=...
//
// The URL is documented at https://github.com/nicholas-fedor/shoutrrr
func buildSMTPURL(cfg *models.SMTPConfig, msg Message) string {
	query := url.Values{}
	query.Set("fromaddress", cfg.From)
	query.Set("toaddresses", strings.Join(models.SplitList(cfg.To), ","))
	query.Set("subject", subjectFor(cfg, msg))
	query.Set("usehtml", yesNo(cfg.UseHTML))

	if cfg.Secure {
		// Port 465 style: implicit TLS from the first byte.
		query.Set("encryption", "ExplicitTLS")
		query.Set("usestarttls", "no")
	} else {
		query.Set("encryption", "Auto")
		query.Set("usestarttls", "yes")
	}
	if cfg.SkipTLSVerify {
		query.Set("skiptlsverify", "yes")
	}
	query.Set("timeout", "15s")

	serviceURL := url.URL{
		Scheme:   "smtp",
		Host:     net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port)),
		RawQuery: query.Encode(),
	}
	if cfg.Username != "" {
		serviceURL.User = url.UserPassword(cfg.Username, cfg.Password)
	}
	return serviceURL.String()
}

// buildWebhookURL translates the webhook settings into the shoutrrr "generic"
// service URL:
//
//	generic://host[:port]/path?method=POST&contenttype=application/json&@Header=value
//
// Notes:
//   - "disabletls=yes" switches the request to plain HTTP.
//   - custom headers are passed with the "@" prefix.
//   - "@Content-Type" is set explicitly because shoutrrr would otherwise use
//     "text/plain" for a raw (template-less) body.
func buildWebhookURL(cfg *models.WebhookConfig) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(cfg.URL))
	if err != nil {
		return "", fmt.Errorf("invalid webhook url: %w", err)
	}
	if parsed.Host == "" {
		return "", fmt.Errorf("invalid webhook url %q: host is missing", cfg.URL)
	}

	contentType := strings.TrimSpace(cfg.ContentType)
	if contentType == "" {
		contentType = "application/json"
	}
	method := strings.ToUpper(strings.TrimSpace(cfg.Method))
	if method == "" {
		method = "POST"
	}

	query := url.Values{}
	query.Set("method", method)
	query.Set("contenttype", contentType)
	query.Set("@Content-Type", contentType)
	if parsed.Scheme == "http" {
		query.Set("disabletls", "yes")
		query.Set("titlekey", "title")
		query.Set("messagekey", "message")
	}

	for _, header := range cfg.Headers {
		key := strings.TrimSpace(header.Key)
		if key == "" || strings.EqualFold(key, "content-type") {
			continue
		}
		query.Set("@"+key, header.Value)
	}

	serviceURL := url.URL{
		Scheme:   "generic",
		Host:     parsed.Host,
		Path:     parsed.Path,
		RawQuery: query.Encode(),
	}
	return serviceURL.String(), nil
}

// buildSlackURL translates the Slack settings into the shoutrrr service URL:
//
//	slack://token@channel?botname=...&icon=...&thread_ts=...
//
// The token travels in the userinfo part: a bot token (xoxb-…) or an incoming
// webhook token (hook:T…-B…-X…) are both understood. The channel is the host
// part, so it must stay free of separators (`C0123456789` is what the UI
// suggests).
func buildSlackURL(cfg *models.SlackConfig) (string, error) {
	token := strings.TrimSpace(cfg.Token)
	if token == "" {
		return "", errors.New("the Slack token is missing")
	}
	channel := strings.TrimSpace(cfg.Channel)
	if channel == "" {
		return "", errors.New("the Slack channel is missing")
	}

	query := url.Values{}
	if cfg.BotName != "" {
		query.Set("botname", cfg.BotName)
	}
	if cfg.Icon != "" {
		query.Set("icon", cfg.Icon)
	}
	if cfg.ThreadTS != "" {
		query.Set("thread_ts", cfg.ThreadTS)
	}

	serviceURL := url.URL{
		Scheme:   "slack",
		User:     slackUserinfo(token),
		Host:     channel,
		RawQuery: query.Encode(),
	}
	return serviceURL.String(), nil
}

// slackUserinfo splits a "type:rest" token (hook:…, xoxb:…) the way shoutrrr
// does when it renders the URL itself: the identifier in the username and the
// rest in the password. A bare token (xoxb-…) travels whole, so the colons of
// an incoming webhook token are not percent escaped into the userinfo.
func slackUserinfo(token string) *url.Userinfo {
	if identifier, rest, found := strings.Cut(token, ":"); found && identifier != "" && rest != "" {
		return url.UserPassword(identifier, rest)
	}
	return url.User(token)
}

// buildDiscordURL translates the Discord settings into the shoutrrr service
// URL:
//
//	discord://token@webhook_id?username=...&avatar=...&thread_id=...
//
// "splitlines=no" is forced: shoutrrr defaults to one embedded item per line,
// which would turn a multi line alert into a stack of embeds.
func buildDiscordURL(cfg *models.DiscordConfig) (string, error) {
	webhookID := strings.TrimSpace(cfg.WebhookID)
	if webhookID == "" {
		return "", errors.New("the Discord webhook id is missing")
	}
	token := strings.TrimSpace(cfg.Token)
	if token == "" {
		return "", errors.New("the Discord webhook token is missing")
	}

	query := url.Values{}
	query.Set("splitlines", "no")
	if cfg.Username != "" {
		query.Set("username", cfg.Username)
	}
	if cfg.AvatarURL != "" {
		query.Set("avatar", cfg.AvatarURL)
	}
	if cfg.ThreadID != "" {
		query.Set("thread_id", cfg.ThreadID)
	}

	serviceURL := url.URL{
		Scheme:   "discord",
		User:     url.User(token),
		Host:     webhookID,
		RawQuery: query.Encode(),
	}
	return serviceURL.String(), nil
}

// buildTelegramURL translates the Telegram settings into the shoutrrr service
// URL:
//
//	telegram://bot_id:secret@telegram?chats=a,b&parsemode=None&notification=yes&preview=no
//
// The bot token is a "id:secret" pair, so it is split and sent as the userinfo
// password (shoutrrr rebuilds it verbatim).
func buildTelegramURL(cfg *models.TelegramConfig) (string, error) {
	botID, secret, found := strings.Cut(strings.TrimSpace(cfg.Token), ":")
	if !found || strings.TrimSpace(botID) == "" || strings.TrimSpace(secret) == "" {
		return "", errors.New("the Telegram bot token must look like <bot id>:<secret>")
	}
	chats := models.SplitList(cfg.Chats)
	if len(chats) == 0 {
		return "", errors.New("the Telegram chat list is empty")
	}
	parseMode, ok := models.TelegramParseMode(cfg.ParseMode)
	if !ok {
		return "", fmt.Errorf("unknown Telegram parse mode %q", cfg.ParseMode)
	}

	query := url.Values{}
	query.Set("chats", strings.Join(chats, ","))
	query.Set("parsemode", parseMode)
	query.Set("notification", yesNo(!cfg.DisableNotification))
	query.Set("preview", yesNo(!cfg.DisablePreview))

	serviceURL := url.URL{
		Scheme:   "telegram",
		User:     url.UserPassword(strings.TrimSpace(botID), strings.TrimSpace(secret)),
		Host:     "telegram",
		RawQuery: query.Encode(),
	}
	return serviceURL.String(), nil
}

func yesNo(value bool) string {
	if value {
		return "yes"
	}
	return "no"
}
