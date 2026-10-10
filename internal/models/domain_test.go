package models

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// TestParseCheckTime covers the "HH:MM" parser of the expiry settings.
func TestParseCheckTime(t *testing.T) {
	hour, minute, err := ParseCheckTime("03:30")
	if err != nil || hour != 3 || minute != 30 {
		t.Fatalf("03:30 -> %d:%02d err=%v", hour, minute, err)
	}
	for _, invalid := range []string{"", "3", "24:00", "12:60", "abc"} {
		if _, _, err := ParseCheckTime(invalid); err == nil {
			t.Fatalf("%q must be rejected", invalid)
		}
	}
}

// TestNextDailyRun fixes the daily gate of the expiry job.
func TestNextDailyRun(t *testing.T) {
	// 01:00 UTC, the run is at 03:00 the same day.
	now := time.Date(2026, 5, 10, 1, 0, 0, 0, time.UTC)
	due := RunInstant(now, "03:00", "UTC")
	if due.Before(now) {
		t.Fatalf("03:00 is still in the future for %s", now)
	}
	if next := NextDailyRun(now, "03:00", "UTC"); !next.Equal(due) {
		t.Fatalf("next = %s, want %s", next, due)
	}

	// 04:00 UTC, today's instant has passed: the next run is tomorrow.
	after := time.Date(2026, 5, 10, 4, 0, 0, 0, time.UTC)
	if got := RunInstant(after, "03:00", "UTC"); !got.Before(after) {
		t.Fatalf("03:00 must be in the past for %s", after)
	}
	next := NextDailyRun(after, "03:00", "UTC")
	if want := time.Date(2026, 5, 11, 3, 0, 0, 0, time.UTC); !next.Equal(want) {
		t.Fatalf("next = %s, want %s", next, want)
	}
}

// TestRunInstantTimezone checks that the configured timezone is honoured.
func TestRunInstantTimezone(t *testing.T) {
	// 12:00 UTC is 09:00 in Sao Paulo (UTC-3); the 08:00 local run has passed.
	moment := time.Date(2026, 5, 10, 12, 0, 0, 0, time.UTC)
	local := RunInstant(moment, "08:00", "America/Sao_Paulo")
	if !local.Equal(time.Date(2026, 5, 10, 8, 0, 0, 0, time.FixedZone("-03", -3*3600))) {
		t.Fatalf("run instant = %s (%s)", local, local.Location())
	}
	if local.Before(moment) != true {
		t.Fatalf("the 08:00 local run must be in the past at %s", moment)
	}
	// An unknown timezone falls back to UTC instead of failing.
	fallback := RunInstant(moment, "15:00", "Not/AZone")
	if fallback.Location() != time.UTC {
		t.Fatalf("fallback location = %s", fallback.Location())
	}
}

// TestExpirySettingsValidation covers the bounds of the admin managed settings.
func TestExpirySettingsValidation(t *testing.T) {
	settings := DefaultExpirySettings()
	if problem := settings.Validate(); problem != "" {
		t.Fatalf("the defaults must be valid: %s", problem)
	}
	bad := settings
	bad.CheckTime = "25:00"
	if problem := bad.Validate(); problem == "" {
		t.Fatal("25:00 must be rejected")
	}
	bad = settings
	bad.CheckTimezone = "Not/AZone"
	if problem := bad.Validate(); problem == "" {
		t.Fatal("an unknown timezone must be rejected")
	}
	bad = settings
	bad.RateLimitMS = -1
	if problem := bad.Validate(); problem == "" {
		t.Fatal("a negative rate limit must be rejected")
	}
	bad = settings
	bad.TimeoutSeconds = 0
	if problem := bad.Validate(); problem == "" {
		t.Fatal("a zero timeout must be rejected")
	}
}

// TestExpirySettingsNormalize clamps the stored values.
func TestExpirySettingsNormalize(t *testing.T) {
	settings := ExpirySettings{CheckTime: "nope", CheckTimezone: "nope", RateLimitMS: 999999, TimeoutSeconds: 999}
	settings.Normalize()
	if settings.CheckTime != DefaultExpiryCheckTime || settings.CheckTimezone != DefaultExpiryTimezone {
		t.Fatalf("normalize = %+v", settings)
	}
	if settings.RateLimitMS != 60000 || settings.TimeoutSeconds != DefaultExpiryTimeoutSeconds {
		t.Fatalf("clamp = %+v", settings)
	}
}

// TestManualDomainInfo documents the observation built from a date the operator
// typed: it is ok/manual immediately, so a stale "unsupported" (the "no parser"
// badge) cannot hide a date that is already known.
func TestManualDomainInfo(t *testing.T) {
	now := time.Date(2026, 5, 10, 12, 0, 0, 0, time.UTC)
	date := time.Date(2028, 1, 9, 0, 0, 0, 0, time.UTC)

	info := ManualDomainInfo("example.com", &date, now)
	if info == nil {
		t.Fatal("a date must produce an observation")
	}
	if info.Status != DomainStatusOK || info.Source != DomainSourceManual {
		t.Fatalf("status/source = %s/%s", info.Status, info.Source)
	}
	if !info.ExpiresAt.Equal(date) || info.Domain != "example.com" {
		t.Fatalf("info = %+v", info)
	}
	if want := DaysLeft(date, now); info.DaysLeft != want {
		t.Fatalf("days_left = %d, want %d", info.DaysLeft, want)
	}
	if !info.HasExpiry() || info.Expired() {
		t.Fatalf("a future manual date is a valid, unexpired expiry: %+v", info)
	}

	if ManualDomainInfo("example.com", nil, now) != nil {
		t.Fatal("no date means no observation")
	}
	zero := time.Time{}
	if ManualDomainInfo("example.com", &zero, now) != nil {
		t.Fatal("a zero date means no date")
	}
	expired := now.AddDate(0, 0, -1)
	if info := ManualDomainInfo("example.com", &expired, now); info == nil || !info.Expired() {
		t.Fatalf("an expired manual date must report itself: %+v", info)
	}
}

// TestWhoisParserValidate covers the rule validation.
func TestWhoisParserValidate(t *testing.T) {
	valid := &WhoisParser{TLD: ".com.BR ", ExpiryRegex: `Expiry:\s*(.+)`}
	valid.Normalize()
	if valid.TLD != "com.br" {
		t.Fatalf("tld = %q", valid.TLD)
	}
	if problem := valid.Validate(); problem != "" {
		t.Fatalf("the rule must be valid: %s", problem)
	}
	if problem := (&WhoisParser{TLD: "com"}).Validate(); problem == "" {
		t.Fatal("a missing regex must be rejected")
	}
	if problem := (&WhoisParser{TLD: "com", ExpiryRegex: `no group`}).Validate(); problem == "" {
		t.Fatal("a regex without a capture group must be rejected")
	}
	if problem := (&WhoisParser{TLD: "com", ExpiryRegex: `(`}).Validate(); problem == "" {
		t.Fatal("an invalid regex must be rejected")
	}
	if problem := (&WhoisParser{TLD: "com", ExpiryRegex: `(.+)`, MinIntervalMS: 99000}).Validate(); problem == "" {
		t.Fatal("an out of range min_interval_ms must be rejected")
	}
}

// TestMatchWhoisParser picks the most specific suffix.
func TestMatchWhoisParser(t *testing.T) {
	parsers := []WhoisParser{
		{TLD: "br", ExpiryRegex: `(.+)`, Enabled: true},
		{TLD: "com.br", ExpiryRegex: `(.+)`, Enabled: true},
		{TLD: "net.br", ExpiryRegex: `(.+)`, Enabled: false},
	}
	match := MatchWhoisParser(parsers, "example.com.br")
	if match == nil || match.TLD != "com.br" {
		t.Fatalf("match = %+v", match)
	}
	match = MatchWhoisParser(parsers, "example.org.br")
	if match == nil || match.TLD != "br" {
		t.Fatalf("fallback match = %+v", match)
	}
	// A disabled rule is skipped, but a broader enabled one still applies.
	if got := MatchWhoisParser(parsers, "example.net.br"); got == nil || got.TLD != "br" {
		t.Fatalf("a disabled rule must fall back to the enabled suffix, got %+v", got)
	}
	if MatchWhoisParser(parsers, "example.com") != nil {
		t.Fatal("a domain outside the configured suffixes must not match")
	}
}

// TestDomainInfoMarshalJSON pins the wire contract of the decorated domain
// object: a lookup that read no date (not_found, unsupported, error) exposes
// "expires_at": null instead of Go's zero time "0001-01-01T00:00:00Z", which the
// UI painted as a real date. It mirrors TestHeartbeatSummaryMarshalJSON.
func TestDomainInfoMarshalJSON(t *testing.T) {
	checkedAt := time.Date(2026, 5, 10, 12, 0, 0, 0, time.UTC)
	missing := DomainInfo{Domain: "saranszky.com", Status: DomainStatusNotFound, CheckedAt: checkedAt}

	encoded, err := json.Marshal(missing)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(encoded), `"expires_at":null`) {
		t.Fatalf("a missing date must be null, got %s", encoded)
	}
	if strings.Contains(string(encoded), "0001-01-01") {
		t.Fatalf("the zero time must never reach the API: %s", encoded)
	}
	// A manual or a WHOIS observation advertises no registry status, so the
	// field is omitted instead of travelling as null or [].
	if strings.Contains(string(encoded), "rdap_status") {
		t.Fatalf("an observation without a registry status must omit rdap_status: %s", encoded)
	}

	// A known date still round trips (always UTC, the wire format of the API).
	date := time.Date(2028, 1, 9, 19, 2, 41, 0, time.UTC)
	known := DomainInfo{
		Domain:     "example.com",
		Status:     DomainStatusOK,
		ExpiresAt:  date,
		RDAPStatus: []string{"active", "client transfer prohibited"},
		CheckedAt:  date,
	}
	encoded, err = json.Marshal(&known)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(encoded), `"expires_at":"2028-01-09T19:02:41Z"`) {
		t.Fatalf("a known date must round trip, got %s", encoded)
	}
	if !strings.Contains(string(encoded), `"rdap_status":["active","client transfer prohibited"]`) {
		t.Fatalf("the registry status list must round trip, got %s", encoded)
	}

	// The decorated monitor carries the same object: the fix must apply there too.
	// created_at/updated_at are set so the only zero time left in the payload is
	// the one this test is about.
	encoded, err = json.Marshal(Monitor{Domain: &missing, CreatedAt: checkedAt, UpdatedAt: checkedAt})
	if err != nil {
		t.Fatalf("marshal monitor: %v", err)
	}
	if !strings.Contains(string(encoded), `"domain":{"domain":"saranszky.com"`) {
		t.Fatalf("the decorated monitor must carry the domain, got %s", encoded)
	}
	if strings.Contains(string(encoded), "0001-01-01") {
		t.Fatalf("the decorated monitor must not leak the zero time: %s", encoded)
	}
}

// TestSplitJoinDomainStatus pins the CSV column that stores the registry status.
// Every RDAP entry is a phrase with spaces ("client transfer prohibited"), so the
// separator is the comma alone: models.SplitList (which also splits on whitespace)
// would shred each entry into single words.
func TestSplitJoinDomainStatus(t *testing.T) {
	// Split trims and drops the empties but is faithful: it keeps whatever the
	// column holds, spaces and repeats included.
	raw := "active,client transfer prohibited, client hold ,,active"
	want := []string{"active", "client transfer prohibited", "client hold", "active"}
	if got := SplitDomainStatus(raw); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("SplitDomainStatus(%q) = %v, want %v", raw, got, want)
	}
	// Join is the normalising half: it trims, drops the empties and the repeats,
	// so the column it writes is stable whatever the registry sent.
	if got := JoinDomainStatus([]string{"active", " active ", "", "client hold"}); got != "active,client hold" {
		t.Fatalf("JoinDomainStatus = %q", got)
	}
	if got := SplitDomainStatus(""); len(got) != 0 {
		t.Fatalf("a blank CSV must split to nothing, got %v", got)
	}
	if got := JoinDomainStatus(nil); got != "" {
		t.Fatalf("no status must join to the empty string, got %q", got)
	}
}

// TestJoinDomainStatusClipsToTheColumn keeps the insert from failing on the
// varchar(255) column: an entry that would not fit is dropped whole, never cut in
// the middle (a truncated registry phrase would be a lie).
func TestJoinDomainStatusClipsToTheColumn(t *testing.T) {
	long := strings.Repeat("x", MaxDomainStatusLen)
	got := JoinDomainStatus([]string{long, "active"})
	if len(got) > MaxDomainStatusLen {
		t.Fatalf("JoinDomainStatus = %d bytes, want <= %d", len(got), MaxDomainStatusLen)
	}
	if got != long {
		t.Fatalf("JoinDomainStatus dropped an entry that fits: %q", got)
	}
}

// TestMonitorDomainRDAPStatusIsRoundTripped covers the storage conversion the API
// and the notifications share: the column holds a CSV, Info() exposes the array.
func TestMonitorDomainRDAPStatusIsRoundTripped(t *testing.T) {
	row := &MonitorDomain{
		Domain:     "example.com",
		Status:     string(DomainStatusOK),
		RDAPStatus: JoinDomainStatus([]string{"active", "client transfer prohibited"}),
	}
	info := row.Info()
	if got := strings.Join(info.RDAPStatus, "|"); got != "active|client transfer prohibited" {
		t.Fatalf("Info().RDAPStatus = %v", info.RDAPStatus)
	}
}
