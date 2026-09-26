package expiry

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ivancarlosti/up/internal/models"
)

// whoisRegistryExpiryFixture is the expression of the IANA-style registries,
// spelled out here so the assertions do not depend on the model file.
const whoisRegistryExpiryFixture = `(?im)^\s*Registry Expiry Date:\s*(\S+)`

// TestReferral covers the two keywords registries use to point at another
// server (IANA answers with "refer:", some registries with "whois:").
func TestReferral(t *testing.T) {
	ianaAnswer := "domain:        io\nnserver:       a0.nic.io\nwhois:         whois.nic.io\n"
	if got := Referral(ianaAnswer); got != "whois.nic.io" {
		t.Fatalf("Referral(...) = %q, want whois.nic.io", got)
	}
	if got := Referral("refer:        whois.denic.de\n"); got != "whois.denic.de" {
		t.Fatalf("Referral(refer:) = %q", got)
	}
	if got := Referral("Refer:  <whois.example.test>\n"); got != "whois.example.test" {
		t.Fatalf("the angle brackets of a referral must be stripped, got %q", got)
	}
	if got := Referral("domain: example.com\n"); got != "" {
		t.Fatalf("an answer without a referral must yield \"\", got %q", got)
	}
}

// TestServerPrefersTheRegistryOfTheTLD is what the built-in table buys: a known
// TLD never asks whois.iana.org.
func TestServerPrefersTheRegistryOfTheTLD(t *testing.T) {
	client := &WhoisClient{Timeout: time.Second}
	for _, domain := range []string{"example.io", "EXAMPLE.IO", "example.se"} {
		server, err := client.Server(context.Background(), domain)
		if err != nil {
			t.Fatalf("Server(%q): %v", domain, err)
		}
		if server == "" || strings.Contains(server, "iana.org") {
			t.Fatalf("Server(%q) = %q, want the registry of the TLD", domain, server)
		}
	}
	// An unknown TLD falls back to IANA; a cancelled context keeps the test
	// offline and deterministic.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if server, err := client.Server(ctx, "example.unknowntld"); err == nil {
		t.Fatalf("a failed IANA referral must be an error, got server %q", server)
	}
}

// TestBuiltinLayoutsStartWithISO8601 pins the documented order: the operator
// layouts first, then ISO 8601, then the rest.
func TestBuiltinLayoutsStartWithISO8601(t *testing.T) {
	if len(builtinDateLayouts) == 0 || builtinDateLayouts[0] != "2006-01-02T15:04:05Z07:00" {
		t.Fatalf("the first built-in layout is %q", builtinDateLayouts[0])
	}
	layouts := dateLayouts("02/01/2006; 2006-01-02")
	if layouts[0] != "02/01/2006" || layouts[1] != "2006-01-02" {
		t.Fatalf("the operator layouts must come first, got %v", layouts[:2])
	}
	if layouts[2] != builtinDateLayouts[0] {
		t.Fatalf("the built-in layouts must follow the operator ones, got %v", layouts[:3])
	}
	if len(dateLayouts("")) != len(builtinDateLayouts) {
		t.Fatalf("dateLayouts(\"\") = %v", dateLayouts(""))
	}
}

// TestBuiltinDateLayouts parses the shapes the built-in rules rely on. The rule
// carries no DateLayouts, so only builtinDateLayouts is in play.
func TestBuiltinDateLayouts(t *testing.T) {
	cases := []struct {
		name  string
		regex string
		raw   string
		want  string
	}{
		{"ISO 8601", whoisRegistryExpiryFixture, "Registry Expiry Date: 2026-11-22T01:38:41Z", "2026-11-22T01:38:41Z"},
		{"ISO 8601 with milliseconds", whoisRegistryExpiryFixture, "Registry Expiry Date: 2033-01-08T16:00:00.000Z", "2033-01-08T16:00:00Z"},
		{"ISO 8601 with a tenth of a second", whoisRegistryExpiryFixture, "Registry Expiry Date: 2022-12-31T00:00:00.0Z", "2022-12-31T00:00:00Z"},
		{"ISO 8601 with an offset", `(?im)^paid-till:\s*(\S+)`, "paid-till: 2027-07-06T21:00:00+03:00", "2027-07-06T18:00:00Z"},
		{"date and time", `(?im)^Expiration Time:\s*(\S+ \S+)`, "Expiration Time: 2028-03-10 19:06:34", "2028-03-10T19:06:34Z"},
		{"date, time and zone", `(?im)^Expiration date:\s*(\S+ \S+ \S+)`, "Expiration date: 2030-02-08 21:00:00 CLST", "2030-02-08T21:00:00Z"},
		{"slashes", `(?im)^Expiration Date:\s*(\S+ \S+)`, "Expiration Date: 05/03/2027 16:23:00", "2027-03-05T16:23:00Z"},
		{"dots", `(?im)^Expiration date:\s*(\S+ \S+)`, "Expiration date: 10.03.2108 12:00:00", "2108-03-10T12:00:00Z"},
		{"plain dots", `(?im)^expire:\s*(\S+)`, "expire:       07.05.2027", "2027-05-07T00:00:00Z"},
		{"hyphens", `(?im)^Expiry Date:\s*(\S+)`, "Expiry Date: 16-11-2035", "2035-11-16T00:00:00Z"},
		{"year, short month, day", `(?im)^Expires on\.*:\s*([^.]+)`, "Expires on..............: 2026-Aug-25.", "2026-08-25T00:00:00Z"},
		{"day, short month, year and time", `(?im)^Expiration Date:\s*(\S+ \S+)`, "Expiration Date:\t\t20-Mar-2027 13:16:16", "2027-03-20T13:16:16Z"},
		{"plain date", `(?im)^Expire Date:\s*(\S+)`, "Expire Date: 2026-10-10", "2026-10-10T00:00:00Z"},
		{"date under a dotted label", `(?im)^Domain expires:\s*(\S+)`, "Domain expires:             31-Jul-2027", "2027-07-31T00:00:00Z"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			parser := &models.WhoisParser{TLD: "xx", ExpiryRegex: testCase.regex}
			expires, notFound, err := ParseWhois(parser, testCase.raw)
			if err != nil || notFound {
				t.Fatalf("ParseWhois(%q): err=%v notFound=%t", testCase.raw, err, notFound)
			}
			if got := expires.UTC().Format(time.RFC3339); got != testCase.want {
				t.Fatalf("expires = %s, want %s", got, testCase.want)
			}
		})
	}
}

// TestOperatorLayoutWinsOverBuiltin documents the order inside ParseWhois: the
// day-first rule of the operator beats the month-first built-in for the same
// text.
func TestOperatorLayoutWinsOverBuiltin(t *testing.T) {
	parser := &models.WhoisParser{
		TLD:         "xx",
		ExpiryRegex: `(?im)^Expiry:\s*(\S+)`,
		DateLayouts: "02/01/2006",
	}
	expires, _, err := ParseWhois(parser, "Expiry: 01/02/2027")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if want := time.Date(2027, 2, 1, 0, 0, 0, 0, time.UTC); !expires.Equal(want) {
		t.Fatalf("expires = %s, want %s (the operator layout is day first)", expires, want)
	}
}
