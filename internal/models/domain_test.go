package models

import (
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
