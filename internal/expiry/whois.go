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

// Server discovers the registry whois server of a domain through IANA.
func (c *WhoisClient) Server(ctx context.Context, domain string) (string, error) {
	answer, err := c.Query(ctx, IANAServer, tldOf(domain))
	if err != nil {
		return "", err
	}
	if server := Referral(answer); server != "" {
		return server, nil
	}
	return "", fmt.Errorf("whois.iana.org did not refer a server for %q", tldOf(domain))
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
var builtinDateLayouts = []string{
	"2006-01-02T15:04:05Z07:00",
	"2006-01-02T15:04:05Z",
	"2006-01-02 15:04:05",
	"2006-01-02",
	"02-Jan-2006",
	"02.01.2006",
	"2006.01.02",
	"2006/01/02",
	"02/01/2006",
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
