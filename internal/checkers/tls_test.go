package checkers

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"testing"
	"time"

	"github.com/ivancarlosti/up/internal/models"
)

// testCertificate builds a certificate with the given validity, which keeps the
// capture test independent from the network.
func testCertificate(t *testing.T, notAfter time.Time) *x509.Certificate {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generating the key: %v", err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(4242),
		Subject:      pkix.Name{CommonName: "api.example.com", Organization: []string{"Up lab"}},
		Issuer:       pkix.Name{CommonName: "Up lab CA"},
		NotBefore:    notAfter.Add(-90 * 24 * time.Hour),
		NotAfter:     notAfter,
		DNSNames:     []string{"api.example.com", "www.api.example.com"},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("creating the certificate: %v", err)
	}
	parsed, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parsing the certificate: %v", err)
	}
	return parsed
}

// TestCaptureCertificate covers the conversion the probes rely on.
func TestCaptureCertificate(t *testing.T) {
	now := time.Date(2026, 5, 1, 8, 0, 0, 0, time.UTC)
	leaf := testCertificate(t, now.Add(20*24*time.Hour))
	state := &tls.ConnectionState{PeerCertificates: []*x509.Certificate{leaf}}

	info := captureCertificate(state, now)
	if info == nil {
		t.Fatal("the certificate was not captured")
	}
	if info.DaysLeft != 20 {
		t.Fatalf("days_left = %d, want 20", info.DaysLeft)
	}
	if info.Issuer != "CN=api.example.com,O=Up lab" {
		t.Fatalf("issuer = %q", info.Issuer)
	}
	if info.Serial != "4242" {
		t.Fatalf("serial = %q", info.Serial)
	}
	if len(info.DNSNames) != 2 {
		t.Fatalf("dns names = %v", info.DNSNames)
	}
	if info.Expired() {
		t.Fatal("a certificate 20 days from now is not expired")
	}

	// An expired certificate keeps its negative remaining validity, which is what
	// the watcher reports to the operator.
	expired := captureCertificate(&tls.ConnectionState{
		PeerCertificates: []*x509.Certificate{testCertificate(t, now.Add(-72*time.Hour))},
	}, now)
	if expired == nil || expired.DaysLeft != -3 || !expired.Expired() {
		t.Fatalf("expired capture = %+v", expired)
	}

	// No handshake state (a tcp target, a dns target) means no certificate.
	if got := captureCertificate(nil, now); got != nil {
		t.Fatalf("a nil state must not produce a certificate: %+v", got)
	}
	if got := captureCertificate(&tls.ConnectionState{}, now); got != nil {
		t.Fatalf("an empty state must not produce a certificate: %+v", got)
	}
}

// TestCertificateFromError covers the "certificate is expired but the handshake
// failed" path: the operator still gets the issuer and the days left.
func TestCertificateFromError(t *testing.T) {
	now := time.Date(2026, 5, 1, 8, 0, 0, 0, time.UTC)
	leaf := testCertificate(t, now.Add(-24*time.Hour))
	verifyErr := &tls.CertificateVerificationError{
		UnverifiedCertificates: []*x509.Certificate{leaf},
	}

	info := certificateFromError(verifyErr, now)
	if info == nil {
		t.Fatal("the certificate carried by the verification error was not captured")
	}
	if info.DaysLeft != -1 {
		t.Fatalf("days_left = %d, want -1", info.DaysLeft)
	}

	// Any other error has nothing to offer.
	if got := certificateFromError(x509.UnknownAuthorityError{Cert: leaf}, now); got != nil {
		t.Fatalf("an unrelated error must not produce a certificate: %+v", got)
	}
	if got := certificateFromError(nil, now); got != nil {
		t.Fatalf("nil must not produce a certificate: %+v", got)
	}
}

// TestCertificateInfoMarksOurs keeps the captured certificate tagged with the
// moment and the node, which is what the UI shows.
func TestCertificateInfoMarksOurs(t *testing.T) {
	now := time.Date(2026, 5, 1, 8, 0, 0, 0, time.UTC)
	leaf := testCertificate(t, now.Add(5*24*time.Hour))
	info := certificateInfo(leaf, now)
	if !info.CapturedAt.Equal(now.UTC()) {
		t.Fatalf("captured_at = %s", info.CapturedAt)
	}
	if info.NotAfter.Equal(time.Time{}) {
		t.Fatal("not_after must be filled")
	}
	if models.DaysLeft(info.NotAfter, info.CapturedAt) != 5 {
		t.Fatalf("the stored validity disagrees with the payload: %+v", info)
	}
}
