package utils

import (
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestParseRuleAndMatchIP(t *testing.T) {
	cases := []struct {
		rule string
		ip   string
		want bool
	}{
		{"203.0.113.7", "203.0.113.7", true},
		{"203.0.113.7", "203.0.113.8", false},
		{"203.0.113.0/24", "203.0.113.200", true},
		{"203.0.113.0/24", "203.0.114.1", false},
		{"2001:db8::/32", "2001:db8::1", true},
		{"2001:db8::/32", "2001:db9::1", false},
		{"10.0.0.0/8", "2001:db8::1", false},
	}
	for _, tc := range cases {
		ip := net.ParseIP(tc.ip)
		if got := MatchIP(tc.rule, ip); got != tc.want {
			t.Errorf("MatchIP(%q, %s) = %v, want %v", tc.rule, tc.ip, got, tc.want)
		}
	}

	for _, invalid := range []string{"", "not-an-ip", "10.0.0.0/99"} {
		if _, err := ParseRule(invalid); err == nil {
			t.Errorf("ParseRule(%q) should fail", invalid)
		}
	}
}

func TestClientIPHonoursTheProxyFlag(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/api/health", nil)
	request.RemoteAddr = "192.0.2.10:54321"
	request.Header.Set("X-Forwarded-For", "198.51.100.7, 192.0.2.1")
	request.Header.Set("X-Real-IP", "198.51.100.9")

	if got := ClientIP(request, false); got != "192.0.2.10" {
		t.Errorf("with an untrusted proxy the header must be ignored, got %s", got)
	}
	if got := ClientIP(request, true); got != "198.51.100.7" {
		t.Errorf("with a trusted proxy the first forwarded address wins, got %s", got)
	}

	ipv6 := httptest.NewRequest(http.MethodGet, "/api/health", nil)
	ipv6.RemoteAddr = "[2001:db8::2]:4444"
	if got := ClientIP(ipv6, false); got != "2001:db8::2" {
		t.Errorf("IPv6 address parsing failed, got %s", got)
	}
}

func TestSignedValue(t *testing.T) {
	signed := SignedValue("secret", "payload")
	if payload, ok := VerifySignedValue("secret", signed); !ok || payload != "payload" {
		t.Fatalf("round trip failed: %q %v", payload, ok)
	}
	if _, ok := VerifySignedValue("other-secret", signed); ok {
		t.Fatal("a different secret must not validate the signature")
	}
	if _, ok := VerifySignedValue("secret", "payload.tampered"); ok {
		t.Fatal("a tampered signature must be rejected")
	}
}

func TestRequestScheme(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.Header.Set("X-Forwarded-Proto", "https")
	if got := RequestScheme(request, false); got != "http" {
		t.Errorf("untrusted proxy: got %s, want http", got)
	}
	if got := RequestScheme(request, true); got != "https" {
		t.Errorf("trusted proxy: got %s, want https", got)
	}
}
