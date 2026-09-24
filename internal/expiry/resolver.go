package expiry

import (
	"context"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/ivancarlosti/up/internal/models"
)

// Resolver resolves the expiration of a registrable domain.
//
// It is the orchestrator of the expiry feature: manual date, then RDAP, then
// WHOIS with the operator parser. It never touches the database — the services
// package feeds it the settings and the parsers of the run.
type Resolver struct {
	mu       sync.Mutex
	settings models.ExpirySettings
	parsers  []models.WhoisParser
	limiter  *Limiter
	rdap     *rdapClient
	whois    *WhoisClient
	// servers caches the registry whois server discovered through IANA, keyed by
	// TLD (referrals do not change inside a run).
	servers map[string]string
}

// NewResolver builds a resolver with the default settings.
func NewResolver() *Resolver {
	resolver := &Resolver{
		settings: models.DefaultExpirySettings(),
		limiter:  NewLimiter(),
		servers:  map[string]string{},
	}
	resolver.rebuildLocked()
	return resolver
}

// Configure applies the settings and the parsers used by the next run.
func (r *Resolver) Configure(settings models.ExpirySettings, parsers []models.WhoisParser) {
	settings.Normalize()
	r.mu.Lock()
	defer r.mu.Unlock()
	r.settings = settings
	r.parsers = append([]models.WhoisParser{}, parsers...)
	r.rebuildLocked()
}

// Settings returns a copy of the current settings.
func (r *Resolver) Settings() models.ExpirySettings {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.settings
}

// rebuildLocked (re)creates the clients from the current timeout.
func (r *Resolver) rebuildLocked() {
	timeout := time.Duration(r.settings.TimeoutSeconds) * time.Second
	r.rdap = newRDAPClient(timeout)
	r.whois = &WhoisClient{Timeout: timeout}
}

// Resolve returns the expiration of a domain, never nil.
func (r *Resolver) Resolve(ctx context.Context, domain string, manual *time.Time) *models.DomainInfo {
	now := time.Now().UTC()
	normalized := NormalizeDomain(domain)
	if normalized == "" {
		normalized = strings.ToLower(strings.TrimSpace(domain))
	}
	if manual != nil && !manual.IsZero() {
		// A manual date is authoritative on purpose: some TLDs simply do not
		// publish the expiry, and the operator typed the truth.
		expires := manual.UTC()
		return &models.DomainInfo{
			Domain:    normalized,
			ExpiresAt: expires,
			Source:    models.DomainSourceManual,
			Status:    models.DomainStatusOK,
			DaysLeft:  models.DaysLeft(expires, now),
			CheckedAt: now,
		}
	}
	if normalized == "" {
		return &models.DomainInfo{
			Status:    models.DomainStatusError,
			Error:     "the monitor target yields no domain",
			CheckedAt: now,
		}
	}

	r.mu.Lock()
	settings := r.settings
	parsers := append([]models.WhoisParser{}, r.parsers...)
	r.mu.Unlock()

	var rdapInfo *models.DomainInfo
	if settings.RDAPEnabled && r.rdap != nil {
		if base, ok := r.rdap.baseFor(ctx, tldOf(normalized)); ok {
			_ = r.limiter.Wait(ctx, "rdap:"+hostOfURL(base), interval(settings, 0))
			rdapInfo = r.rdap.lookup(ctx, normalized)
			if settled(rdapInfo) {
				return rdapInfo
			}
		}
	}

	var whoisInfo *models.DomainInfo
	if settings.WHOISEnabled && r.whois != nil {
		if parser := models.MatchWhoisParser(parsers, normalized); parser == nil {
			whoisInfo = &models.DomainInfo{
				Domain:    normalized,
				Status:    models.DomainStatusUnsupported,
				Error:     "no whois parser is configured for the tld of " + normalized,
				CheckedAt: now,
			}
		} else {
			whoisInfo = r.lookupWHOIS(ctx, normalized, parser, settings, now)
			if settled(whoisInfo) {
				return whoisInfo
			}
		}
	}

	// Nothing settled: report the most useful reason. The RDAP failure wins (it
	// is the authoritative source for the TLD), then the WHOIS one.
	if rdapInfo != nil && rdapInfo.Status == models.DomainStatusError {
		return rdapInfo
	}
	if whoisInfo != nil {
		return whoisInfo
	}
	return &models.DomainInfo{
		Domain:    normalized,
		Status:    models.DomainStatusUnsupported,
		Error:     "rdap and whois are both disabled",
		CheckedAt: now,
	}
}

// lookupWHOIS discovers the registry server, applies the rate limit and parses
// the answer with the rule of the TLD.
func (r *Resolver) lookupWHOIS(ctx context.Context, domain string, parser *models.WhoisParser, settings models.ExpirySettings, now time.Time) *models.DomainInfo {
	server := parser.Server
	if server == "" {
		tld := tldOf(domain)
		r.mu.Lock()
		cached := r.servers[tld]
		r.mu.Unlock()
		if cached == "" {
			_ = r.limiter.Wait(ctx, "whois:"+IANAServer, interval(settings, 0))
			discovered, err := r.whois.Server(ctx, domain)
			if err != nil {
				return &models.DomainInfo{Domain: domain, Status: models.DomainStatusError, Error: err.Error(), CheckedAt: now}
			}
			cached = discovered
			r.mu.Lock()
			r.servers[tld] = cached
			r.mu.Unlock()
		}
		server = cached
	}

	if err := r.limiter.Wait(ctx, "whois:"+server, interval(settings, parser.MinIntervalMS)); err != nil {
		return &models.DomainInfo{Domain: domain, Status: models.DomainStatusError, Error: err.Error(), CheckedAt: now}
	}
	raw, err := r.whois.Query(ctx, server, domain)
	if err != nil {
		return &models.DomainInfo{Domain: domain, Status: models.DomainStatusError, Error: err.Error(), CheckedAt: now}
	}
	expiresAt, notFound, err := ParseWhois(parser, raw)
	switch {
	case notFound:
		return &models.DomainInfo{Domain: domain, Status: models.DomainStatusNotFound, CheckedAt: now}
	case err != nil:
		return &models.DomainInfo{Domain: domain, Status: models.DomainStatusError, Error: err.Error(), CheckedAt: now}
	}
	return &models.DomainInfo{
		Domain:    domain,
		ExpiresAt: expiresAt,
		Source:    models.DomainSourceWHOIS,
		Status:    models.DomainStatusOK,
		DaysLeft:  models.DaysLeft(expiresAt, now),
		CheckedAt: now,
	}
}

// WHOISTest queries the registry of a domain and parses it with a candidate rule
// (used by the "test parser" box of the admin page). An empty server is
// discovered through IANA.
func (r *Resolver) WHOISTest(ctx context.Context, domain string, parser *models.WhoisParser, server string) (raw string, expiresAt time.Time, notFound bool, err error) {
	normalized := NormalizeDomain(domain)
	if normalized == "" {
		return "", time.Time{}, false, context.Canceled
	}
	r.mu.Lock()
	settings := r.settings
	timeout := time.Duration(settings.TimeoutSeconds) * time.Second
	client := r.whois
	if client == nil {
		client = &WhoisClient{Timeout: timeout}
	}
	r.mu.Unlock()

	target := strings.TrimSpace(server)
	if target == "" && parser != nil {
		target = strings.TrimSpace(parser.Server)
	}
	if target == "" {
		if target, err = client.Server(ctx, normalized); err != nil {
			return "", time.Time{}, false, err
		}
	}
	raw, err = client.Query(ctx, target, normalized)
	if err != nil {
		return "", time.Time{}, false, err
	}
	expiresAt, notFound, err = ParseWhois(parser, raw)
	return raw, expiresAt, notFound, err
}

// settled reports whether an observation carries a final answer.
func settled(info *models.DomainInfo) bool {
	return info != nil && (info.Status == models.DomainStatusOK || info.Status == models.DomainStatusNotFound)
}

// interval returns the effective delay between two lookups: the global setting,
// raised by a per-parser override.
func interval(settings models.ExpirySettings, overrideMS int) time.Duration {
	ms := settings.RateLimitMS
	if overrideMS > ms {
		ms = overrideMS
	}
	return time.Duration(ms) * time.Millisecond
}

// hostOfURL returns the host of a URL (or the raw value when it is not one).
func hostOfURL(raw string) string {
	if parsed, err := url.Parse(raw); err == nil && parsed.Host != "" {
		return parsed.Host
	}
	return raw
}
