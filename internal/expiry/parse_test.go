package expiry

import (
	"testing"
	"time"

	"github.com/ivancarlosti/up/internal/models"
)

// rdapFixture is a trimmed but realistic registry answer.
const rdapFixture = `{
  "objectClassName": "domain",
  "ldhName": "EXAMPLE.COM",
  "events": [
    {"eventAction": "registration", "eventDate": "2010-03-01T00:00:00Z"},
    {"eventAction": "expiration", "eventDate": "2027-05-01T04:00:00Z"},
    {"eventAction": "last changed", "eventDate": "2024-04-02T09:00:00Z"}
  ],
  "entities": [
    {
      "roles": ["registrar"],
      "vcardArray": ["vcard", [
        ["version", {}, "text", "4.0"],
        ["fn", {}, "text", "ACME Registrar Inc."]
      ]]
    }
  ]
}`

// TestParseRDAP extracts the expiration event and the registrar.
func TestParseRDAP(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	info, err := ParseRDAP("example.com", []byte(rdapFixture), now)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := time.Date(2027, 5, 1, 4, 0, 0, 0, time.UTC)
	if !info.ExpiresAt.Equal(want) {
		t.Fatalf("expires_at = %s, want %s", info.ExpiresAt, want)
	}
	if info.Source != models.DomainSourceRDAP || info.Status != models.DomainStatusOK {
		t.Fatalf("source/status = %s/%s", info.Source, info.Status)
	}
	if info.Registrar != "ACME Registrar Inc." {
		t.Fatalf("registrar = %q", info.Registrar)
	}
	if info.DaysLeft != models.DaysLeft(want, now) {
		t.Fatalf("days_left = %d", info.DaysLeft)
	}
}

// TestParseRDAPWithoutExpiration is what makes the operator add a manual date.
func TestParseRDAPWithoutExpiration(t *testing.T) {
	body := `{"events":[{"eventAction":"registration","eventDate":"2010-03-01T00:00:00Z"}]}`
	if _, err := ParseRDAP("example.com", []byte(body), time.Now().UTC()); err == nil {
		t.Fatal("a payload without an expiration event must be an error")
	}
}

// TestParseWhois documents the per-TLD rule: a capture group plus the layouts.
func TestParseWhois(t *testing.T) {
	parser := &models.WhoisParser{
		TLD:         "br",
		ExpiryRegex: `(?i)expir[^:]*:\s*(.+)`,
		DateLayouts: "2006-01-02",
	}
	raw := "domain: example.com.br\nstatus: published\nexpires at: 2027-05-01\n"
	expires, notFound, err := ParseWhois(parser, raw)
	if err != nil || notFound {
		t.Fatalf("parse: %v notFound=%t", err, notFound)
	}
	if want := time.Date(2027, 5, 1, 0, 0, 0, 0, time.UTC); !expires.Equal(want) {
		t.Fatalf("expires = %s, want %s", expires, want)
	}
}

// TestParseWhoisCustomLayout covers a registry with an unusual format.
func TestParseWhoisCustomLayout(t *testing.T) {
	parser := &models.WhoisParser{
		TLD:         "xx",
		ExpiryRegex: `Expiry:\s+(\S+)`,
		DateLayouts: "02/01/2006",
	}
	expires, _, err := ParseWhois(parser, "Expiry: 31/12/2027")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if want := time.Date(2027, 12, 31, 0, 0, 0, 0, time.UTC); !expires.Equal(want) {
		t.Fatalf("expires = %s, want %s", expires, want)
	}
}

// TestParseWhoisNotFound reports an unregistered domain.
func TestParseWhoisNotFound(t *testing.T) {
	parser := &models.WhoisParser{
		TLD:             "com",
		ExpiryRegex:     `Expiry Date:\s*(\S+)`,
		NotFoundPattern: `(?i)no match for`,
	}
	_, notFound, err := ParseWhois(parser, "No match for domain \"NOTREGISTERED.COM\"")
	if err != nil || !notFound {
		t.Fatalf("notFound = %t err = %v", notFound, err)
	}
}

// TestParseWhoisUnparsable is the case the operator fixes with a parser or a
// manual date: the answer arrived but the rule does not match it.
func TestParseWhoisUnparsable(t *testing.T) {
	parser := &models.WhoisParser{TLD: "xx", ExpiryRegex: `Expiry:\s*(.+)`}
	if _, _, err := ParseWhois(parser, "nothing useful here"); err == nil {
		t.Fatal("a non matching rule must be an error")
	}
	if _, _, err := ParseWhois(nil, "anything"); err == nil {
		t.Fatal("a missing parser must be an error")
	}
}
