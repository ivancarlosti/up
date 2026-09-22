package notify

import (
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

func yesNo(value bool) string {
	if value {
		return "yes"
	}
	return "no"
}
