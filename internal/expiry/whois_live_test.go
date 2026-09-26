package expiry

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/ivancarlosti/up/internal/models"
)

// probeDomains is the registered domain each built-in rule was verified with:
// normally the registry's own name (nic.<tld>), which is the one domain that
// certainly exists in the TLD. It is what makes the live check below possible.
var probeDomains = map[string]string{
	"io": "nic.io", "us": "b.cctld.us", "me": "nic.me", "ie": "iedr.ie", "my": "nic.my",
	"ru": "nic.ru", "hr": "dns.hr", "ug": "nic.ug", "cn": "cnnic.cn", "cl": "nic.cl",
	"pt": "dns.pt", "rs": "a.nic.rs", "im": "registry.im", "tr": "google.com.tr",
	"edu": "nic.edu", "bn": "nic.bn", "hk": "hkirc.hk", "ax": "nic.ax", "ls": "nic.ls",
	"mk": "nic.mk", "ve": "nic.ve", "it": "nic.it", "se": "iis.se", "dk": "dk-hostmaster.dk",
	"lt": "domreg.lt", "mx": "nic.mx", "by": "nic.by", "am": "google.am", "pk": "root-c1.pknic.pk",
	"ee": "internet.ee", "mc": "nic.mc", "st": "nic.st", "tg": "nic.tg", "ac": "nic.ac",
	"bf": "nic.bf", "bh": "nic.bh", "bi": "nic.bi", "bj": "nic.bj", "co": "nic.co",
	"gi": "nic.gi", "gl": "nic.gl", "la": "registry.la", "sh": "nic.sh", "su": "nic.su",
	"sx": "nic.sx", "vc": "nic.vc", "af": "nic.af", "ag": "nic.ag", "ci": "nic.ci",
	"dm": "nic.dm", "do": "nic.do", "gh": "nic.gh", "gn": "nic.gn", "ki": "nic.ki",
	"kn": "nic.kn", "mn": "nic.mn", "mr": "nic.mr", "mz": "nic.mz", "pr": "nic.pr",
	"sb": "nic.sb", "sc": "nic.sc", "so": "nic.so", "td": "nic.td", "tl": "nic.tl",
}

// TestLiveWhoisDefaults queries every real registry of the built-in table and
// checks that its rule still extracts a date. It is opt-in because it needs the
// public internet and takes a minute:
//
//	UP_LIVE_WHOIS=1 go test ./internal/expiry/ -run TestLiveWhoisDefaults -v
//
// It is the reproducible form of "this rule was verified", which is what
// justifies every row of models.DefaultWhoisParsers.
func TestLiveWhoisDefaults(t *testing.T) {
	if os.Getenv("UP_LIVE_WHOIS") != "1" {
		t.Skip("set UP_LIVE_WHOIS=1 to query the real registries")
	}
	client := &WhoisClient{Timeout: 10 * time.Second}
	parsers := models.DefaultWhoisParsers()
	if len(parsers) != len(probeDomains) {
		t.Errorf("%d built-in rules but %d probe domains: keep them in sync", len(parsers), len(probeDomains))
	}
	for i := range parsers {
		parser := parsers[i]
		domain, known := probeDomains[parser.TLD]
		if !known {
			t.Errorf("%s: no probe domain, the rule cannot be verified", parser.TLD)
			continue
		}
		t.Run(parser.TLD, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			raw, err := client.Query(ctx, parser.Server, domain)
			if err != nil {
				t.Fatalf("%s: querying %s: %v", domain, parser.Server, err)
			}
			expires, notFound, err := ParseWhois(&parser, raw)
			if err != nil {
				t.Fatalf("%s: %v", domain, err)
			}
			if notFound {
				t.Fatalf("%s is reported as not registered: pick another probe domain", domain)
			}
			if expires.IsZero() {
				t.Fatalf("%s: no expiration date", domain)
			}
			t.Logf(".%s %s -> %s", parser.TLD, domain, expires.Format(time.RFC3339))
		})
	}
}
