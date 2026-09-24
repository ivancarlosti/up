package models

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

// ---------------------------------------------------------------------------
// Domain expiration (the registry sibling of the TLS certificate watcher)
// ---------------------------------------------------------------------------

// DomainSource reports where an expiry date came from.
//
// It is stored so the UI can be honest about the provenance of a date: a
// manual entry is what the operator typed, an RDAP date comes from the registry
// API and a WHOIS date was extracted by a per-TLD parser.
type DomainSource string

// Supported domain sources.
const (
	// DomainSourceManual is the date typed in the monitor form (the calendar).
	DomainSourceManual DomainSource = "manual"
	// DomainSourceRDAP comes from the registry RDAP endpoint.
	DomainSourceRDAP DomainSource = "rdap"
	// DomainSourceWHOIS comes from a port 43 WHOIS response parsed by a
	// per-TLD parser (models.WhoisParser).
	DomainSourceWHOIS DomainSource = "whois"
)

// DomainStatus is the outcome of one resolution attempt.
type DomainStatus string

// Supported domain statuses.
const (
	// DomainStatusOK: an expiry date was obtained.
	DomainStatusOK DomainStatus = "ok"
	// DomainStatusNotFound: the registry says the domain is not registered.
	DomainStatusNotFound DomainStatus = "not_found"
	// DomainStatusUnsupported: neither RDAP nor a WHOIS parser knows the TLD.
	DomainStatusUnsupported DomainStatus = "unsupported"
	// DomainStatusError: the lookup failed (network, parse, rate limit).
	DomainStatusError DomainStatus = "error"
)

// DomainInfo is the registrable domain expiration observed for a monitor.
//
// It mirrors CertificateInfo: the same shape is what the resolvers produce, what
// the API exposes and what the alert planner consumes, so the certificate and
// domain watchers share one mental model.
type DomainInfo struct {
	// Domain is the registrable domain (eTLD+1) the dates belong to.
	Domain string `json:"domain"`
	// Registrar is the sponsoring registrar when the registry publishes it.
	Registrar string `json:"registrar,omitempty"`
	// ExpiresAt is the registry expiry date (zero when the status is not ok).
	ExpiresAt time.Time `json:"expires_at"`
	// Source is where ExpiresAt came from.
	Source DomainSource `json:"source,omitempty"`
	// Status is the outcome of the last lookup.
	Status DomainStatus `json:"status"`
	// Error carries the reason of a failed lookup (status=error).
	Error string `json:"error,omitempty"`
	// DaysLeft is the remaining validity, floored (see DaysLeft).
	DaysLeft int `json:"days_left"`
	// CheckedAt is when the lookup ran.
	CheckedAt time.Time `json:"checked_at"`
	// CheckedByNode is the cluster node that performed the lookup.
	CheckedByNode string `json:"checked_by_node,omitempty"`
}

// HasExpiry reports whether a usable expiry date is present.
func (d *DomainInfo) HasExpiry() bool {
	return d != nil && !d.ExpiresAt.IsZero()
}

// Expired reports whether the domain is already past its ExpiresAt.
func (d *DomainInfo) Expired() bool {
	if !d.HasExpiry() {
		return false
	}
	return d.ExpiresAt.Before(d.CheckedAt)
}

// MonitorDomain is the stored domain expiration state of a monitor.
//
// It is the domain counterpart of MonitorCertificate and lives in its own table
// for the same reason: it is runtime state rewritten by every lookup, while the
// notification bookkeeping (which thresholds were alerted, on which day) has to
// survive those rewrites.
type MonitorDomain struct {
	MonitorID uint `gorm:"primaryKey" json:"monitor_id"`
	// Domain is the registrable domain the monitor resolves to (eTLD+1).
	Domain    string    `gorm:"size:190" json:"domain"`
	Registrar string    `gorm:"size:200" json:"registrar"`
	ExpiresAt time.Time `json:"expires_at"`
	// Source and Status are stored as the readable strings (manual/rdap/whois,
	// ok/not_found/unsupported/error) so a SQL inspection is self explaining.
	Source        string    `gorm:"size:10" json:"source"`
	Status        string    `gorm:"size:16" json:"status"`
	Error         string    `gorm:"size:500" json:"error"`
	DaysLeft      int       `json:"days_left"`
	CheckedAt     time.Time `json:"checked_at"`
	CheckedByNode string    `gorm:"size:64" json:"checked_by_node"`
	// NotifiedDays is the CSV list of warn thresholds already alerted ("30,7").
	NotifiedDays string `gorm:"size:120" json:"notified_days"`
	// LastNotifiedDay is the day bucket (unix/86400) of the last notification,
	// which is what makes the reminder daily.
	LastNotifiedDay int64 `json:"last_notified_day"`
}

// TableName keeps the table name stable.
func (MonitorDomain) TableName() string { return "monitor_domains" }

// Info converts the row into the payload exposed to the API and the UI.
func (d *MonitorDomain) Info() *DomainInfo {
	if d == nil {
		return nil
	}
	return &DomainInfo{
		Domain:        d.Domain,
		Registrar:     d.Registrar,
		ExpiresAt:     d.ExpiresAt,
		Source:        DomainSource(d.Source),
		Status:        DomainStatus(d.Status),
		Error:         d.Error,
		DaysLeft:      d.DaysLeft,
		CheckedAt:     d.CheckedAt,
		CheckedByNode: d.CheckedByNode,
	}
}

// NotifiedThresholds returns the thresholds already alerted.
func (d *MonitorDomain) NotifiedThresholds() []int {
	days, _ := ParseCertWarnDays(d.NotifiedDays)
	return days
}

// DomainWarnDaysOrDefault returns the configured thresholds of a monitor or the
// default list. It reuses the certificate semantics on purpose: "the dates when
// the operator is notified" must behave identically for both watchers.
func DomainWarnDaysOrDefault(raw string) []int {
	return WarnDaysOrDefault(raw)
}

// ---------------------------------------------------------------------------
// WHOIS parsers (per TLD, operator managed)
// ---------------------------------------------------------------------------

// WhoisParser is an operator provided rule that extracts the expiry date from a
// raw WHOIS response.
//
// Several TLDs have no RDAP at all and many of the remaining ones do not
// publish the expiry in a machine readable way, so the only portable escape
// hatch is "tell Up how to read the text of this registry". The rule is small on
// purpose: a regular expression (with a capture group holding the date) plus the
// layouts the captured text may use.
type WhoisParser struct {
	ID uint `gorm:"primaryKey" json:"id"`
	// TLD is the suffix the rule applies to, without a leading dot. It may be
	// multi label ("com.br"); the most specific match wins (see MatchWhoisParser).
	TLD string `gorm:"column:tld;size:80;not null;uniqueIndex" json:"tld"`
	// Server overrides the registry whois server (normally discovered through
	// whois.iana.org). Useful for registries that ignore the referral.
	Server string `gorm:"size:190" json:"server"`
	// ExpiryRegex is a RE2 expression; the first capture group (or the whole
	// match when there is none) is the date text.
	ExpiryRegex string `gorm:"size:500" json:"expiry_regex"`
	// DateLayouts is the ";" separated list of Go reference layouts the captured
	// text may use (a single one is the common case).
	DateLayouts string `gorm:"size:255" json:"date_layouts"`
	// NotFoundPattern, when it matches, marks the domain as not registered.
	NotFoundPattern string `gorm:"size:255" json:"not_found_pattern"`
	// MinIntervalMS overrides the global lookup rate limit for this registry
	// server (0 = use the global setting). Strict registries get their own pace.
	MinIntervalMS int    `gorm:"not null;default:0" json:"min_interval_ms"`
	Enabled       bool   `gorm:"not null;default:true" json:"enabled"`
	Note          string `gorm:"size:255" json:"note"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// TableName keeps the table name stable.
func (WhoisParser) TableName() string { return "whois_parsers" }

// Normalize cleans the fields of a parser.
func (p *WhoisParser) Normalize() {
	p.TLD = strings.ToLower(strings.Trim(strings.TrimSpace(p.TLD), "."))
	p.Server = strings.ToLower(strings.TrimSpace(p.Server))
	p.ExpiryRegex = strings.TrimSpace(p.ExpiryRegex)
	p.DateLayouts = strings.TrimSpace(p.DateLayouts)
	p.NotFoundPattern = strings.TrimSpace(p.NotFoundPattern)
	p.Note = strings.TrimSpace(p.Note)
}

// Validate checks the rule (empty string means "valid").
func (p *WhoisParser) Validate() string {
	if p.TLD == "" {
		return "tld is required"
	}
	for _, label := range strings.Split(p.TLD, ".") {
		if label == "" {
			return "tld must not contain empty labels"
		}
	}
	if p.ExpiryRegex == "" {
		return "expiry_regex is required"
	}
	expression, err := regexp.Compile(p.ExpiryRegex)
	if err != nil {
		return "expiry_regex is not a valid regular expression: " + err.Error()
	}
	if expression.NumSubexp() == 0 {
		return "expiry_regex must contain a capture group around the date"
	}
	if p.NotFoundPattern != "" {
		if _, err := regexp.Compile(p.NotFoundPattern); err != nil {
			return "not_found_pattern is not a valid regular expression: " + err.Error()
		}
	}
	if p.MinIntervalMS < 0 || p.MinIntervalMS > 60000 {
		return "min_interval_ms must be between 0 and 60000"
	}
	return ""
}

// MatchWhoisParser returns the enabled parser that applies to a domain, the most
// specific suffix winning ("com.br" beats "br").
func MatchWhoisParser(parsers []WhoisParser, domain string) *WhoisParser {
	name := strings.ToLower(strings.Trim(strings.TrimSpace(domain), "."))
	if name == "" {
		return nil
	}
	var best *WhoisParser
	bestLabels := 0
	for i := range parsers {
		candidate := &parsers[i]
		if !candidate.Enabled {
			continue
		}
		tld := strings.ToLower(strings.Trim(strings.TrimSpace(candidate.TLD), "."))
		if tld == "" {
			continue
		}
		if name != tld && !strings.HasSuffix(name, "."+tld) {
			continue
		}
		labels := strings.Count(tld, ".") + 1
		if labels > bestLabels {
			best = candidate
			bestLabels = labels
		}
	}
	return best
}

// ---------------------------------------------------------------------------
// Expiry settings (Admin > TLD/SSL expiration)
// ---------------------------------------------------------------------------

// DefaultExpiryCheckTime is when the daily expiry job runs (local to
// DefaultExpiryTimezone) when the operator did not choose a time.
const DefaultExpiryCheckTime = "03:00"

// DefaultExpiryTimezone is the timezone of the daily run. UTC keeps the
// behaviour identical on every node; the container ships tzdata, so an IANA
// name (e.g. "America/Sao_Paulo") also works.
const DefaultExpiryTimezone = "UTC"

// DefaultExpiryRateLimitMS is the minimum delay between two registry lookups.
// rdap.org and most WHOIS servers are happy with 10 requests/s; 100 ms leaves a
// wide margin and is what the operator lowers when there is no throttling.
const DefaultExpiryRateLimitMS = 100

// DefaultExpiryTimeoutSeconds bounds a single RDAP/WHOIS lookup.
const DefaultExpiryTimeoutSeconds = 10

// ExpirySettings is the admin managed configuration of the daily expiry job.
type ExpirySettings struct {
	// CheckTime is "HH:MM" (24h) in CheckTimezone.
	CheckTime string `json:"check_time"`
	// CheckTimezone is an IANA name ("UTC", "America/Sao_Paulo").
	CheckTimezone string `json:"check_timezone"`
	// RDAPEnabled and WHOISEnabled select which registries are tried.
	RDAPEnabled  bool `json:"rdap_enabled"`
	WHOISEnabled bool `json:"whois_enabled"`
	// RateLimitMS is the minimum delay between two lookups (0 = no delay).
	RateLimitMS int `json:"rate_limit_ms"`
	// TimeoutSeconds bounds a single lookup.
	TimeoutSeconds int `json:"timeout_seconds"`
}

// DefaultExpirySettings returns the configuration used when nothing is stored.
func DefaultExpirySettings() ExpirySettings {
	return ExpirySettings{
		CheckTime:      DefaultExpiryCheckTime,
		CheckTimezone:  DefaultExpiryTimezone,
		RDAPEnabled:    true,
		WHOISEnabled:   true,
		RateLimitMS:    DefaultExpiryRateLimitMS,
		TimeoutSeconds: DefaultExpiryTimeoutSeconds,
	}
}

// Normalize applies the defaults and clamps the bounds of the settings.
func (e *ExpirySettings) Normalize() {
	if _, _, err := ParseCheckTime(e.CheckTime); err != nil {
		e.CheckTime = DefaultExpiryCheckTime
	}
	e.CheckTimezone = strings.TrimSpace(e.CheckTimezone)
	if _, err := time.LoadLocation(e.CheckTimezone); err != nil {
		e.CheckTimezone = DefaultExpiryTimezone
	}
	if e.RateLimitMS < 0 {
		e.RateLimitMS = 0
	}
	if e.RateLimitMS > 60000 {
		e.RateLimitMS = 60000
	}
	if e.TimeoutSeconds < 1 || e.TimeoutSeconds > 60 {
		e.TimeoutSeconds = DefaultExpiryTimeoutSeconds
	}
}

// Validate checks the operator input (empty string means "valid").
func (e *ExpirySettings) Validate() string {
	if _, _, err := ParseCheckTime(e.CheckTime); err != nil {
		return "check_time must be a time of day like 03:30"
	}
	if _, err := time.LoadLocation(strings.TrimSpace(e.CheckTimezone)); err != nil {
		return fmt.Sprintf("check_timezone %q is not a known IANA timezone", e.CheckTimezone)
	}
	if e.RateLimitMS < 0 || e.RateLimitMS > 60000 {
		return "rate_limit_ms must be between 0 and 60000"
	}
	if e.TimeoutSeconds < 1 || e.TimeoutSeconds > 60 {
		return "timeout_seconds must be between 1 and 60"
	}
	return ""
}

// ParseCheckTime parses an "HH:MM" time of day.
func ParseCheckTime(raw string) (hour, minute int, err error) {
	trimmed := strings.TrimSpace(raw)
	parts := strings.Split(trimmed, ":")
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("%q is not HH:MM", raw)
	}
	if _, err := fmt.Sscanf(trimmed, "%d:%d", &hour, &minute); err != nil {
		return 0, 0, fmt.Errorf("%q is not HH:MM", raw)
	}
	if hour < 0 || hour > 23 || minute < 0 || minute > 59 {
		return 0, 0, fmt.Errorf("%q is out of range", raw)
	}
	return hour, minute, nil
}

// RunInstant returns the instant of the run scheduled for the calendar day of
// `moment`, expressed in the configured timezone. It is already in the past when
// that day's run is due, which is exactly what the daily job checks.
func RunInstant(moment time.Time, checkTime, timezone string) time.Time {
	location := time.UTC
	if loaded, err := time.LoadLocation(strings.TrimSpace(timezone)); err == nil {
		location = loaded
	}
	hour, minute, err := ParseCheckTime(checkTime)
	if err != nil {
		hour, minute = 3, 0
	}
	local := moment.In(location)
	return time.Date(local.Year(), local.Month(), local.Day(), hour, minute, 0, 0, location)
}

// NextDailyRun returns the first scheduled instant at or after `now`, expressed
// in the configured timezone. It is the pure core of the daily expiry job.
func NextDailyRun(now time.Time, checkTime, timezone string) time.Time {
	scheduled := RunInstant(now, checkTime, timezone)
	if scheduled.Before(now) {
		scheduled = RunInstant(now.AddDate(0, 0, 1), checkTime, timezone)
	}
	return scheduled
}
