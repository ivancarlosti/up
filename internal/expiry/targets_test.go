package expiry

import (
	"context"
	"testing"
	"time"

	"github.com/ivancarlosti/up/internal/models"
)

// TestNormalizeDomain fixes the registrable domain extraction that makes the
// deduplication work: several monitors of the same site must share one lookup.
func TestNormalizeDomain(t *testing.T) {
	cases := []struct {
		host string
		want string
	}{
		{"app.example.com", "example.com"},
		{"www.example.com", "example.com"},
		{"EXAMPLE.com.", "example.com"},
		{"www.example.co.uk", "example.co.uk"},
		{"example.com.br", "example.com.br"},
		{"localhost", "localhost"},
		{"192.0.2.1", ""},
		{"2001:db8::1", ""},
		{"", ""},
	}
	for _, testCase := range cases {
		if got := NormalizeDomain(testCase.host); got != testCase.want {
			t.Fatalf("NormalizeDomain(%q) = %q, want %q", testCase.host, got, testCase.want)
		}
	}
}

// TestCertificateTargetForKey documents the identity used to collapse several
// monitors into one handshake.
func TestCertificateTargetForKey(t *testing.T) {
	ssl := &models.Monitor{
		Type:      models.MonitorTypeSSL,
		CertWatch: true,
		Config:    models.MonitorConfig{Host: "Example.com"},
	}
	target, ok := CertificateTargetFor(ssl)
	if !ok {
		t.Fatal("a ssl monitor with cert_watch must have a certificate target")
	}
	if target.Port != 443 || target.Key() != "example.com:443|example.com" {
		t.Fatalf("target = %+v key = %q", target, target.Key())
	}

	withSNI := &models.Monitor{
		Type:      models.MonitorTypeSSL,
		CertWatch: true,
		Config:    models.MonitorConfig{Host: "10.0.0.1", Port: 8443, ServerName: "Shop.Example.com"},
	}
	sniTarget, _ := CertificateTargetFor(withSNI)
	if sniTarget.Key() != "10.0.0.1:8443|shop.example.com" {
		t.Fatalf("sni key = %q", sniTarget.Key())
	}
	// The same endpoint with and without an explicit SNI is the same target.
	if target.Key() == sniTarget.Key() {
		t.Fatal("different endpoints must not share a key")
	}

	https := &models.Monitor{
		Type:      models.MonitorTypeHTTP,
		CertWatch: true,
		Config:    models.MonitorConfig{URL: "https://app.example.com:8443/login", IgnoreTLS: true},
	}
	httpsTarget, ok := CertificateTargetFor(https)
	if !ok || httpsTarget.Port != 8443 || !httpsTarget.Insecure {
		t.Fatalf("https target = %+v ok = %t", httpsTarget, ok)
	}

	plain := &models.Monitor{
		Type:      models.MonitorTypeHTTP,
		CertWatch: true,
		Config:    models.MonitorConfig{URL: "http://app.example.com"},
	}
	if _, ok := CertificateTargetFor(plain); ok {
		t.Fatal("an http URL has no certificate to watch")
	}
}

// TestDomainFor covers the target derivation per monitor type.
func TestDomainFor(t *testing.T) {
	cases := []struct {
		monitor *models.Monitor
		want    string
	}{
		{&models.Monitor{Type: models.MonitorTypeHTTP, DomainWatch: true, Config: models.MonitorConfig{URL: "https://www.example.com/x"}}, "example.com"},
		{&models.Monitor{Type: models.MonitorTypeSSL, DomainWatch: true, Config: models.MonitorConfig{Host: "mail.example.org"}}, "example.org"},
		{&models.Monitor{Type: models.MonitorTypeTCP, DomainWatch: true, Config: models.MonitorConfig{Host: "example.net", Port: 25}}, "example.net"},
		{&models.Monitor{Type: models.MonitorTypeDNS, DomainWatch: true, Config: models.MonitorConfig{Hostname: "example.io"}}, "example.io"},
		{&models.Monitor{Type: models.MonitorTypeSSL, DomainWatch: false, Config: models.MonitorConfig{Host: "mail.example.org"}}, ""},
	}
	for _, testCase := range cases {
		if got := DomainFor(testCase.monitor); got != testCase.want {
			t.Fatalf("DomainFor(%s) = %q, want %q", testCase.monitor.Type, got, testCase.want)
		}
	}
}

// TestLimiterSpacesLookups documents the per-registry pace.
func TestLimiterSpacesLookups(t *testing.T) {
	limiter := NewLimiter()
	ctx := context.Background()
	const interval = 40 * time.Millisecond

	if err := limiter.Wait(ctx, "registry", interval); err != nil {
		t.Fatalf("first wait: %v", err)
	}
	started := time.Now()
	if err := limiter.Wait(ctx, "registry", interval); err != nil {
		t.Fatalf("second wait: %v", err)
	}
	if elapsed := time.Since(started); elapsed < interval/2 {
		t.Fatalf("two waits on the same key took %s, expected at least %s", elapsed, interval/2)
	}

	// A different key is not slowed down by the first one.
	started = time.Now()
	if err := limiter.Wait(ctx, "other", interval); err != nil {
		t.Fatalf("other key: %v", err)
	}
	if elapsed := time.Since(started); elapsed > interval/2 {
		t.Fatalf("a different key waited %s", elapsed)
	}

	// 0 means no delay at all.
	started = time.Now()
	_ = limiter.Wait(ctx, "registry", 0)
	if elapsed := time.Since(started); elapsed > 10*time.Millisecond {
		t.Fatalf("a zero interval waited %s", elapsed)
	}
}

// TestLimiterCancelledContext makes sure a shutdown is not delayed.
func TestLimiterCancelledContext(t *testing.T) {
	limiter := NewLimiter()
	ctx, cancel := context.WithCancel(context.Background())
	_ = limiter.Wait(ctx, "registry", time.Millisecond)
	cancel()
	started := time.Now()
	if err := limiter.Wait(ctx, "registry", time.Hour); err == nil {
		t.Fatal("a cancelled context must abort the wait")
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("the wait lasted %s after the cancellation", elapsed)
	}
}
