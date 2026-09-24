package checkers

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/ivancarlosti/up/internal/models"
)

// checkSSL performs a TLS handshake against the target and reports the state of
// its certificate.
//
// The handshake is the probe: a target whose certificate cannot be verified is
// down (which is what a browser would see). The certificate is captured even
// when the verification fails, so the operator gets "expires in -3 days
// (issuer: ...)" instead of a bare handshake error.
func checkSSL(ctx context.Context, monitor *models.Monitor) Result {
	cfg := monitor.Config
	host := strings.TrimSpace(cfg.Host)
	if host == "" {
		return Result{Status: models.StatusDown, Message: "the host is required"}
	}
	port := cfg.Port
	if port <= 0 {
		port = models.DefaultSSLPort
	}

	started := time.Now()
	info, err := probeCertificate(ctx, net.JoinHostPort(host, strconv.Itoa(port)), cfg.ServerName, cfg.IgnoreTLS)
	latency := time.Since(started).Milliseconds()
	if err != nil {
		return Result{
			Status:      models.StatusDown,
			LatencyMS:   latency,
			Message:     describeRequestError(err),
			Certificate: info,
		}
	}
	message := "TLS handshake ok"
	if info != nil {
		message = fmt.Sprintf("certificate expires in %d days (issuer: %s)", info.DaysLeft, info.Issuer)
	}
	return Result{
		Status:      models.StatusUp,
		LatencyMS:   latency,
		Message:     message,
		Certificate: info,
	}
}

// ProbeCertificate dials the address and returns the leaf certificate.
//
// It is the exported entry point of the daily expiry job, which re-checks the
// deduplicated certificate endpoints once a day (instead of writing the row on
// every probe), and of the "run now" action of the admin page.
func ProbeCertificate(ctx context.Context, address, serverName string, insecure bool, timeout time.Duration) (*models.CertificateInfo, error) {
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return probeCertificate(ctx, address, serverName, insecure)
}

// probeCertificate dials the address and returns the leaf certificate.
//
// When the verification fails the certificates carried by the error are used:
// an expired or self signed chain is exactly the situation the certificate
// watcher has to report.
func probeCertificate(ctx context.Context, address, serverName string, insecure bool) (*models.CertificateInfo, error) {
	config := &tls.Config{InsecureSkipVerify: insecure} //nolint:gosec // explicit operator opt-in
	if serverName != "" {
		config.ServerName = serverName
	} else if host, _, err := net.SplitHostPort(address); err == nil {
		config.ServerName = host
	}

	dialer := &net.Dialer{Timeout: timeoutFromContext(ctx)}
	conn, err := tls.DialWithDialer(dialer, "tcp", address, config)
	now := time.Now().UTC()
	if err != nil {
		return certificateFromError(err, now), err
	}
	defer func() { _ = conn.Close() }()

	state := conn.ConnectionState()
	return captureCertificate(&state, now), nil
}

// captureCertificate reads the leaf certificate of a completed handshake.
func captureCertificate(state *tls.ConnectionState, now time.Time) *models.CertificateInfo {
	if state == nil || len(state.PeerCertificates) == 0 {
		return nil
	}
	return certificateInfo(state.PeerCertificates[0], now)
}

// certificateFromError extracts the certificate a failed verification carried.
func certificateFromError(err error, now time.Time) *models.CertificateInfo {
	var verifyErr *tls.CertificateVerificationError
	if errors.As(err, &verifyErr) && len(verifyErr.UnverifiedCertificates) > 0 {
		return certificateInfo(verifyErr.UnverifiedCertificates[0], now)
	}
	return nil
}

// certificateInfo converts an x509 certificate into the stored shape. DaysLeft
// floors the remaining time, so a certificate that expires in 12 hours has 0
// days left and one already past has a negative value.
func certificateInfo(leaf *x509.Certificate, now time.Time) *models.CertificateInfo {
	if leaf == nil {
		return nil
	}
	return &models.CertificateInfo{
		Subject:    leaf.Subject.String(),
		Issuer:     leaf.Issuer.String(),
		Serial:     leaf.SerialNumber.String(),
		NotBefore:  leaf.NotBefore.UTC(),
		NotAfter:   leaf.NotAfter.UTC(),
		DNSNames:   append([]string{}, leaf.DNSNames...),
		DaysLeft:   models.DaysLeft(leaf.NotAfter, now),
		CapturedAt: now.UTC(),
	}
}
