package expiry

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"regexp"
	"strings"
	"time"

	"github.com/ivancarlosti/up/internal/models"
)

// IANAServer is the root whois server used to discover the registry server of a
// TLD.
const IANAServer = "whois.iana.org:43"

// whoisMaxBytes caps a single response (a registry answer is a few KB; the cap
// only protects against a misbehaving server).
const whoisMaxBytes = 1 << 20

// WhoisClient queries port 43 servers.
type WhoisClient struct {
	Timeout time.Duration
}

// Query sends a single line to a whois server and returns the raw answer.
func (c *WhoisClient) Query(ctx context.Context, server, query string) (string, error) {
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = models.DefaultExpiryTimeoutSeconds * time.Second
	}
	address := withPort(server, "43")
	if address == "" {
		return "", fmt.Errorf("the whois server is empty")
	}
	dialer := &net.Dialer{Timeout: timeout}
	conn, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return "", err
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(timeout))
	if _, err := fmt.Fprintf(conn, "%s\r\n", strings.TrimSpace(query)); err != nil {
		return "", err
	}
	body, err := io.ReadAll(io.LimitReader(conn, whoisMaxBytes))
	if err != nil {
		return "", err
	}
	return string(body), nil
}

// Server returns the registry whois server of a domain.
//
// The built-in table of the TLD wins (models.WhoisServerFor): asking
// whois.iana.org for a server we already know costs a round trip per TLD and
// leaves the lookup without an answer every time IANA is slow or unreachable.
// The referral is the fallback for every TLD the table does not cover.
func (c *WhoisClient) Server(ctx context.Context, domain string) (string, error) {
	suffix := tldOf(domain)
	if server, known := models.WhoisServerFor(suffix); known {
		return server, nil
	}
	answer, err := c.Query(ctx, IANAServer, suffix)
	if err != nil {
		return "", err
	}
	if server := Referral(answer); server != "" {
		return server, nil
	}
	return "", fmt.Errorf("whois.iana.org did not refer a server for %q", suffix)
}

// Referral returns the server a whois answer points at ("" when there is none).
//
// IANA answers use "refer:"; some registries use "whois:".
func Referral(answer string) string {
	scanner := bufio.NewScanner(strings.NewReader(answer))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		lower := strings.ToLower(line)
		if !strings.HasPrefix(lower, "refer:") && !strings.HasPrefix(lower, "whois:") {
			continue
		}
		index := strings.Index(line, ":")
		if index < 0 {
			continue
		}
		value := strings.Trim(strings.TrimSpace(line[index+1:]), "<> \t")
		if value != "" {
			return value
		}
	}
	return ""
}

// withPort appends the default port when the server does not carry one.
func withPort(server, port string) string {
	value := strings.TrimSpace(server)
	if value == "" {
		return ""
	}
	if _, _, err := net.SplitHostPort(value); err == nil {
		return value
	}
	return net.JoinHostPort(value, port)
}

// builtinDateLayouts are the shapes tried after the operator provided ones. They
// cover the common registry formats so an operator only has to write a layout
// when their registry is unusual.
//
// ISO 8601 comes first because it is what models.DefaultWhoisDateLayouts
// recommends (and what most registries emit); the fractional-second entry uses
// 999 so both "2026-11-22T01:38:41Z" and "2026-11-22T01:38:41.000Z" parse.
var builtinDateLayouts = []string{
	"2006-01-02T15:04:05Z07:00",
	"2006-01-02T15:04:05.999Z07:00",
	"2006-01-02T15:04:05Z",
	"2006-01-02 15:04:05 MST",
	"2006-01-02 15:04:05",
	"2006-01-02",
	"02-Jan-2006",
	"2006-Jan-02",
	"02-Jan-2006 15:04:05",
	"02.01.2006",
	"02.01.2006 15:04:05",
	"02/01/2006",
	"02/01/2006 15:04:05",
	"02-01-2006",
	"2006.01.02",
	"2006/01/02",
	"01/02/2006",
	"20060102",
}

// ParseWhois extracts the expiration date of a raw whois response using the rule
// of the TLD.
//
// It returns notFound=true when the response matches the "not registered"
// pattern. An error means the response was received but could not be read, which
// is exactly the case the operator fixes with the parser (or with a manual date).
func ParseWhois(parser *models.WhoisParser, raw string) (expiresAt time.Time, notFound bool, err error) {
	if parser == nil {
		return time.Time{}, false, fmt.Errorf("no whois parser is configured for this tld")
	}
	if parser.NotFoundPattern != "" {
		if pattern, compileErr := regexp.Compile(parser.NotFoundPattern); compileErr == nil && pattern.MatchString(raw) {
			return time.Time{}, true, nil
		}
	}
	expression, compileErr := regexp.Compile(parser.ExpiryRegex)
	if compileErr != nil {
		return time.Time{}, false, fmt.Errorf("the expiry regular expression does not compile: %w", compileErr)
	}
	match := expression.FindStringSubmatch(raw)
	if match == nil {
		return time.Time{}, false, fmt.Errorf("the expiry regular expression did not match the whois response")
	}
	text := match[0]
	if len(match) > 1 && strings.TrimSpace(match[1]) != "" {
		text = match[1]
	}
	text = strings.TrimSpace(text)
	for _, layout := range dateLayouts(parser.DateLayouts) {
		if parsed, parseErr := time.Parse(layout, text); parseErr == nil {
			return parsed.UTC(), false, nil
		}
	}
	return time.Time{}, false, fmt.Errorf("could not parse the date %q with the configured layouts", text)
}

// dateLayouts returns the operator layouts first, then the built-in fallbacks.
func dateLayouts(raw string) []string {
	out := []string{}
	for _, part := range strings.Split(raw, ";") {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return append(out, builtinDateLayouts...)
}
