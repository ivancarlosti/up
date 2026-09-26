package models

import (
	"strings"
	"testing"
)

// allowedWhoisLayouts is every reference layout the built-in table may use.
var allowedWhoisLayouts = map[string]bool{
	layoutISO8601:               true,
	layoutISO8601Millis:         true,
	layoutDate:                  true,
	layoutDateTime:              true,
	layoutDateTimeZone:          true,
	layoutDotDateTime:           true,
	layoutSlashDateTime:         true,
	layoutDayMonthYear:          true,
	layoutDotDate:               true,
	layoutDayMonthShort:         true,
	layoutYearMonthShort:        true,
	layoutDayMonthShortDateTime: true,
}

// TestDefaultWhoisParsersAreUsable is the guard rail of the built-in table: a
// row the admin form would reject, or one pointing at whois.iana.org (which is
// not a registry and publishes no expiration), must never reach the seed.
func TestDefaultWhoisParsersAreUsable(t *testing.T) {
	parsers := DefaultWhoisParsers()
	if len(parsers) == 0 {
		t.Fatal("the built-in table is empty")
	}
	seen := map[string]bool{}
	for _, parser := range parsers {
		if problem := parser.Validate(); problem != "" {
			t.Fatalf("%s: %s", parser.TLD, problem)
		}
		if parser.Server == "" {
			t.Fatalf("%s: the rule carries no server, so every lookup would ask whois.iana.org", parser.TLD)
		}
		if strings.Contains(parser.Server, "iana.org") {
			t.Fatalf("%s: %q is not a registry server", parser.TLD, parser.Server)
		}
		if !parser.Enabled {
			t.Fatalf("%s: a built-in rule must start enabled", parser.TLD)
		}
		if parser.Note == "" {
			t.Fatalf("%s: the note explains the row in the admin table", parser.TLD)
		}
		if parser.NotFoundPattern == "" {
			t.Fatalf("%s: a free domain must be reported as not registered", parser.TLD)
		}
		if parser.DateLayouts == "" {
			t.Fatalf("%s: the rule lists no layout", parser.TLD)
		}
		// The table may only use the named layout constants, so a typo in a
		// registry format shows up here instead of in production.
		for _, layout := range strings.Split(parser.DateLayouts, ";") {
			if !allowedWhoisLayouts[strings.TrimSpace(layout)] {
				t.Fatalf("%s: %q is not one of the documented layouts", parser.TLD, layout)
			}
		}
		if seen[parser.TLD] {
			t.Fatalf("%s appears twice in the table", parser.TLD)
		}
		seen[parser.TLD] = true

		// Normalizing must be a no-op, otherwise the seeded row differs from
		// what the table documents.
		normalized := parser
		normalized.Normalize()
		if normalized.TLD != parser.TLD || normalized.Server != parser.Server {
			t.Fatalf("%s: the table is not normalized (%q / %q)", parser.TLD, normalized.TLD, normalized.Server)
		}
	}
}

// TestDefaultWhoisParsersMatchOwnTLD makes sure every row really applies to the
// suffix it claims (MatchWhoisParser is what a lookup uses).
func TestDefaultWhoisParsersMatchOwnTLD(t *testing.T) {
	parsers := DefaultWhoisParsers()
	for _, parser := range parsers {
		matched := MatchWhoisParser(parsers, "example."+parser.TLD)
		if matched == nil || matched.TLD != parser.TLD {
			t.Fatalf("example.%s did not match its own rule", parser.TLD)
		}
		// The same domain in upper case must match too (domains are folded).
		if upper := MatchWhoisParser(parsers, "EXAMPLE."+strings.ToUpper(parser.TLD)); upper == nil || upper.TLD != parser.TLD {
			t.Fatalf("EXAMPLE.%s did not match its own rule", strings.ToUpper(parser.TLD))
		}
	}
}

// TestDefaultWhoisParsersIsACopy protects the table from a caller that mutates
// what it got back (the seed normalizes and stores it).
func TestDefaultWhoisParsersIsACopy(t *testing.T) {
	first := DefaultWhoisParsers()
	first[0].TLD = "example"
	first[0].Server = "whois.example.test"
	second := DefaultWhoisParsers()
	if second[0].TLD == "example" || second[0].Server == "whois.example.test" {
		t.Fatal("DefaultWhoisParsers leaks the table: a caller can corrupt it")
	}
}

// TestWhoisServerFor covers the lookup the expiry client uses before falling
// back to the IANA referral.
func TestWhoisServerFor(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"io", "whois.nic.io"},
		{".io", "whois.nic.io"},
		{"IO", "whois.nic.io"},
		{"example.io", "whois.nic.io"},
		{"www.example.se", "whois.iis.se"},
		{" ru ", "whois.tcinet.ru"},
		{"tr", "whois.trabis.gov.tr"},
	}
	for _, testCase := range cases {
		server, found := WhoisServerFor(testCase.input)
		if !found || server != testCase.want {
			t.Fatalf("WhoisServerFor(%q) = %q/%t, want %q", testCase.input, server, found, testCase.want)
		}
	}
	for _, unknown := range []string{"", "  ", ".", "com", "co.uk", "example.com"} {
		if server, found := WhoisServerFor(unknown); found {
			t.Fatalf("WhoisServerFor(%q) must not answer, got %q", unknown, server)
		}
	}
}

// TestDefaultWhoisDateLayoutsIsISO8601 pins the recommended layout: it is what
// the admin form prefills and what every built-in rule starts with.
func TestDefaultWhoisDateLayoutsIsISO8601(t *testing.T) {
	if !strings.HasPrefix(DefaultWhoisDateLayouts, "2006-01-02T15:04:05") {
		t.Fatalf("DefaultWhoisDateLayouts = %q", DefaultWhoisDateLayouts)
	}
	for _, layout := range strings.Split(DefaultWhoisDateLayouts, ";") {
		if !strings.HasPrefix(layout, "2006-01-02") {
			t.Fatalf("layout %q is not an ISO 8601 shape", layout)
		}
	}
}
