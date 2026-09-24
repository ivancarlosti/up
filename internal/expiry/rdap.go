package expiry

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/ivancarlosti/up/internal/models"
)

// RDAPBootstrapURL is the IANA registry that maps a TLD to its RDAP base URL.
//
// Reading it (and caching it for the life of the process) is what lets Up know
// whether a TLD has RDAP at all: a redirector such as rdap.org answers 404 both
// for "unregistered domain" and for "this TLD has no RDAP", and the two must not
// be confused — the second one has to fall through to WHOIS.
const RDAPBootstrapURL = "https://data.iana.org/rdap/dns.json"

// RDAPFallbackBase is used when the bootstrap file cannot be fetched. It is a
// BASE url, exactly like the ones in the IANA bootstrap: the resource path is
// appended by rdapEndpoint.
const RDAPFallbackBase = "https://rdap.org/"

// rdapDomainPath is the RDAP resource path of a domain lookup.
//
// The IANA bootstrap publishes base urls ("https://rdap.registro.br/",
// "https://rdap.verisign.com/com/v1/"), so the lookup url is
// "<base>/domain/<name>". Registries answer 400/501 without the segment, which
// used to be stored as an error instead of the expiration date.
const rdapDomainPath = "domain/"

// rdapMaxBody caps the RDAP response read.
const rdapMaxBody = 1 << 20

// rdapEndpoint builds the lookup URL of a domain from a base URL. A base that
// already carries the resource path is not doubled.
func rdapEndpoint(base, domain string) string {
	trimmed := strings.TrimSuffix(strings.TrimSpace(base), "/")
	if strings.HasSuffix(strings.ToLower(trimmed), "/domain") {
		return trimmed + "/" + url.PathEscape(domain)
	}
	return trimmed + "/" + rdapDomainPath + url.PathEscape(domain)
}

// rdapClient performs RDAP lookups.
type rdapClient struct {
	client       *http.Client
	bootstrapURL string
	fallbackBase string

	mu       sync.Mutex
	services map[string]string // TLD (lower case) -> base URL
	loaded   bool
}

func newRDAPClient(timeout time.Duration) *rdapClient {
	return &rdapClient{
		client:       &http.Client{Timeout: timeout},
		bootstrapURL: RDAPBootstrapURL,
		fallbackBase: RDAPFallbackBase,
	}
}

// bootstrapPayload is the shape of the IANA dns.json file.
type bootstrapPayload struct {
	// Services is a list of [tlds, urls] pairs.
	Services [][][]string `json:"services"`
}

// baseFor returns the RDAP base URL to use for a TLD. It reports false when the
// TLD has no RDAP service (so the caller goes to WHOIS).
func (c *rdapClient) baseFor(ctx context.Context, tld string) (string, bool) {
	c.mu.Lock()
	loaded, services := c.loaded, c.services
	c.mu.Unlock()
	if !loaded {
		loadedServices := map[string]string{}
		if payload, err := c.fetchBootstrap(ctx); err == nil {
			for _, service := range payload.Services {
				if len(service) < 2 || len(service[1]) == 0 {
					continue
				}
				base := service[1][0]
				if !strings.HasSuffix(base, "/") {
					base += "/"
				}
				for _, name := range service[0] {
					loadedServices[strings.ToLower(strings.TrimSpace(name))] = base
				}
			}
		} else {
			// The bootstrap is unreachable: fall back to the public redirector,
			// which is better than skipping RDAP entirely.
			loadedServices = nil
		}
		c.mu.Lock()
		c.services = loadedServices
		c.loaded = true
		c.mu.Unlock()
		services = loadedServices
	}
	if services == nil {
		// Bootstrap unavailable: use the redirector for every TLD.
		return c.fallbackBase, true
	}
	base, ok := services[strings.ToLower(strings.TrimSpace(tld))]
	if !ok {
		return "", false
	}
	return base, true
}

// fetchBootstrap downloads and decodes the IANA registry.
func (c *rdapClient) fetchBootstrap(ctx context.Context) (*bootstrapPayload, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.bootstrapURL, nil)
	if err != nil {
		return nil, err
	}
	response, err := c.client.Do(request)
	if err != nil {
		return nil, err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("the rdap bootstrap answered %s", response.Status)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, rdapMaxBody))
	if err != nil {
		return nil, err
	}
	var payload bootstrapPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, err
	}
	return &payload, nil
}

// lookup resolves the expiration of a registered domain through RDAP.
//
// It returns nil when the TLD has no RDAP service at all (the caller falls back
// to WHOIS). A registered domain without an expiration event, an HTTP failure
// and a rate limit come back as a DomainInfo with status error so the operator
// can see why the date is missing.
func (c *rdapClient) lookup(ctx context.Context, domain string) *models.DomainInfo {
	now := time.Now().UTC()
	base, ok := c.baseFor(ctx, tldOf(domain))
	if !ok {
		return nil
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, rdapEndpoint(base, domain), nil)
	if err != nil {
		return rdapError(domain, now, err)
	}
	request.Header.Set("Accept", "application/rdap+json")
	response, err := c.client.Do(request)
	if err != nil {
		return rdapError(domain, now, err)
	}
	defer func() { _ = response.Body.Close() }()

	switch {
	case response.StatusCode == http.StatusNotFound:
		return &models.DomainInfo{
			Domain:    domain,
			Status:    models.DomainStatusNotFound,
			CheckedAt: now,
		}
	case response.StatusCode == http.StatusTooManyRequests:
		return rdapError(domain, now, fmt.Errorf("the registry rate limited the lookup (%s)", response.Status))
	case response.StatusCode != http.StatusOK:
		return rdapError(domain, now, fmt.Errorf("rdap answered %s", response.Status))
	}

	body, err := io.ReadAll(io.LimitReader(response.Body, rdapMaxBody))
	if err != nil {
		return rdapError(domain, now, err)
	}
	info, err := ParseRDAP(domain, body, now)
	if err != nil {
		return rdapError(domain, now, err)
	}
	return info
}

// rdapError builds the error observation of a failed lookup.
func rdapError(domain string, now time.Time, err error) *models.DomainInfo {
	return &models.DomainInfo{
		Domain:    domain,
		Status:    models.DomainStatusError,
		Error:     err.Error(),
		CheckedAt: now,
	}
}

// ParseRDAP extracts the expiration event (and the registrar, when present) of
// an RDAP response. It is exported so the parsing is unit tested with fixtures.
func ParseRDAP(domain string, body []byte, now time.Time) (*models.DomainInfo, error) {
	var payload rdapResponse
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("decoding the rdap payload: %w", err)
	}
	var expiresAt time.Time
	for _, event := range payload.Events {
		if !strings.EqualFold(strings.TrimSpace(event.Action), "expiration") {
			continue
		}
		parsed, err := parseRDAPTime(event.Date)
		if err != nil {
			continue
		}
		expiresAt = parsed
		break
	}
	if expiresAt.IsZero() {
		return nil, fmt.Errorf("the rdap response carries no usable expiration event")
	}
	return &models.DomainInfo{
		Domain:    domain,
		Registrar: registrarFromRDAP(payload),
		ExpiresAt: expiresAt,
		Source:    models.DomainSourceRDAP,
		Status:    models.DomainStatusOK,
		DaysLeft:  models.DaysLeft(expiresAt, now),
		CheckedAt: now,
	}, nil
}

// rdapResponse is the subset of the RDAP payload Up needs.
type rdapResponse struct {
	Events []struct {
		Action string `json:"eventAction"`
		Date   string `json:"eventDate"`
	} `json:"events"`
	Entities []struct {
		Roles []string        `json:"roles"`
		VCard json.RawMessage `json:"vcardArray"`
	} `json:"entities"`
}

// parseRDAPTime accepts the timestamp shapes registries actually send.
func parseRDAPTime(raw string) (time.Time, error) {
	value := strings.TrimSpace(raw)
	layouts := []string{
		time.RFC3339,
		"2006-01-02T15:04:05Z",
		"2006-01-02T15:04:05",
		"2006-01-02",
	}
	var lastErr error
	for _, layout := range layouts {
		parsed, err := time.Parse(layout, value)
		if err == nil {
			return parsed.UTC(), nil
		}
		lastErr = err
	}
	return time.Time{}, lastErr
}

// registrarFromRDAP reads the "fn" of the registrar entity, best effort.
func registrarFromRDAP(payload rdapResponse) string {
	for _, entity := range payload.Entities {
		if !hasRole(entity.Roles, "registrar") {
			continue
		}
		if name := vcardName(entity.VCard); name != "" {
			return name
		}
	}
	return ""
}

func hasRole(roles []string, want string) bool {
	for _, role := range roles {
		if strings.EqualFold(strings.TrimSpace(role), want) {
			return true
		}
	}
	return false
}

// vcardName extracts the "fn" (formatted name) of a jCard.
func vcardName(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var array []json.RawMessage
	if err := json.Unmarshal(raw, &array); err != nil || len(array) < 2 {
		return ""
	}
	var properties []json.RawMessage
	if err := json.Unmarshal(array[1], &properties); err != nil {
		return ""
	}
	for _, property := range properties {
		var parts []json.RawMessage
		if err := json.Unmarshal(property, &parts); err != nil || len(parts) < 4 {
			continue
		}
		var key string
		if err := json.Unmarshal(parts[0], &key); err != nil || !strings.EqualFold(key, "fn") {
			continue
		}
		var value string
		if err := json.Unmarshal(parts[3], &value); err != nil {
			continue
		}
		return strings.TrimSpace(value)
	}
	return ""
}

// tldOf returns the last label of a domain.
func tldOf(domain string) string {
	trimmed := strings.TrimSuffix(strings.TrimSpace(domain), ".")
	if index := strings.LastIndex(trimmed, "."); index >= 0 {
		return trimmed[index+1:]
	}
	return trimmed
}
