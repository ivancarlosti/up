package expiry

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ivancarlosti/up/internal/models"
)

// brFixture is a trimmed copy of a real rdap.registro.br answer.
const brFixture = `{
  "objectClassName": "domain",
  "handle": "example.com.br",
  "ldhName": "EXAMPLE.COM.BR",
  "events": [
    {"eventAction": "registration", "eventDate": "2019-01-09T19:02:41Z"},
    {"eventAction": "last changed", "eventDate": "2026-01-21T08:22:04Z"},
    {"eventAction": "expiration", "eventDate": "2028-01-09T19:02:41Z"}
  ],
  "entities": [{"roles": ["registrant"], "vcardArray": ["vcard", [["fn", {}, "text", "Ink Comunicacao"]]]}]
}`

// TestRDAPEndpointIsTheRegistryURL is the regression guard of the missing
// resource path: the IANA bootstrap publishes BASE urls, and a registry answers
// 400/501 when "domain/" is missing, which was stored as an error instead of the
// expiration date.
func TestRDAPEndpointIsTheRegistryURL(t *testing.T) {
	cases := []struct {
		base   string
		domain string
		want   string
	}{
		{"https://rdap.registro.br/", "example.com.br", "https://rdap.registro.br/domain/example.com.br"},
		{"https://rdap.verisign.com/com/v1/", "example.com", "https://rdap.verisign.com/com/v1/domain/example.com"},
		{"https://rdap.org/", "example.com", "https://rdap.org/domain/example.com"},
		{"https://rdap.example/domain/", "example.com", "https://rdap.example/domain/example.com"},
	}
	for _, testCase := range cases {
		if got := rdapEndpoint(testCase.base, testCase.domain); got != testCase.want {
			t.Fatalf("rdapEndpoint(%q, %q) = %q, want %q", testCase.base, testCase.domain, got, testCase.want)
		}
	}
}

// TestRDAPLookupRequestsTheResourcePath drives a whole lookup against a stub
// registry: the requested path and the parsed expiration must both be right.
func TestRDAPLookupRequestsTheResourcePath(t *testing.T) {
	var requested string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requested = r.URL.Path
		if accept := r.Header.Get("Accept"); accept != "application/rdap+json" {
			t.Errorf("Accept = %q, want application/rdap+json", accept)
		}
		w.Header().Set("Content-Type", "application/rdap+json")
		_, _ = w.Write([]byte(brFixture))
	}))
	defer server.Close()

	client := newRDAPClient(5 * time.Second)
	client.loaded = true
	client.services = map[string]string{"br": server.URL + "/"}

	info := client.lookup(context.Background(), "example.com.br")
	if requested != "/domain/example.com.br" {
		t.Fatalf("requested %q, want /domain/example.com.br", requested)
	}
	if info == nil || info.Status != models.DomainStatusOK {
		t.Fatalf("info = %+v", info)
	}
	if want := time.Date(2028, 1, 9, 19, 2, 41, 0, time.UTC); !info.ExpiresAt.Equal(want) {
		t.Fatalf("expires = %s, want %s", info.ExpiresAt, want)
	}
}

// TestRDAPLookupStatusMapping covers not found (404) and failure (5xx).
func TestRDAPLookupStatusMapping(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "free") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer server.Close()

	client := newRDAPClient(5 * time.Second)
	client.loaded = true
	client.services = map[string]string{"test": server.URL + "/"}

	notFound := client.lookup(context.Background(), "free.test")
	if notFound == nil || notFound.Status != models.DomainStatusNotFound {
		t.Fatalf("not found = %+v", notFound)
	}
	failed := client.lookup(context.Background(), "broken.test")
	if failed == nil || failed.Status != models.DomainStatusError || failed.Error == "" {
		t.Fatalf("failure = %+v", failed)
	}
}

// TestRDAPLookupWithoutService falls through to WHOIS: a TLD with no RDAP entry
// must answer nil so the resolver can try the per-TLD parser.
func TestRDAPLookupWithoutService(t *testing.T) {
	client := newRDAPClient(time.Second)
	client.loaded = true
	client.services = map[string]string{"com": "https://rdap.example/"}
	if info := client.lookup(context.Background(), "example.io"); info != nil {
		t.Fatalf("a TLD without RDAP must return nil, got %+v", info)
	}
}
