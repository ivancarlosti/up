// Package expiry resolves the expiration of the two things Up watches beyond the
// probe itself: the TLS certificate of an endpoint and the registry registration
// of a domain.
//
// It is deliberately free of database and HTTP-handler concerns: the services
// package owns the storage and the alerts, this package owns the network and the
// parsing. The normalization helpers here are also what makes the daily job
// deduplicate "the same thing" watched by several monitors.
package expiry

import (
	"net"
	"net/url"
	"strconv"
	"strings"

	"golang.org/x/net/publicsuffix"

	"github.com/ivancarlosti/up/internal/models"
)

// ---------------------------------------------------------------------------
// Certificate targets
// ---------------------------------------------------------------------------

// CertificateTarget is the normalized TLS endpoint a certificate belongs to.
//
// Two monitors that point at the same host, port and SNI produce the same Key,
// which is what lets the daily job perform a single handshake for all of them.
type CertificateTarget struct {
	Host       string
	Port       int
	ServerName string
	Insecure   bool
}

// Key is the deduplication identity of the target.
func (t CertificateTarget) Key() string {
	host := strings.ToLower(strings.TrimSpace(t.Host))
	sni := strings.ToLower(strings.TrimSpace(t.ServerName))
	if sni == "" {
		sni = host
	}
	return net.JoinHostPort(host, strconv.Itoa(t.Port)) + "|" + sni
}

// Address is the dialable "host:port" of the target.
func (t CertificateTarget) Address() string {
	return net.JoinHostPort(t.Host, strconv.Itoa(t.Port))
}

// CertificateTargetFor derives the TLS endpoint of a monitor that watches its
// certificate. It reports false when the monitor cannot have one (an http URL,
// a missing host).
func CertificateTargetFor(monitor *models.Monitor) (CertificateTarget, bool) {
	if monitor == nil || !monitor.CertWatch || !monitor.Type.SupportsCertificate() {
		return CertificateTarget{}, false
	}
	cfg := monitor.Config
	switch monitor.Type {
	case models.MonitorTypeHTTP, models.MonitorTypeKeyword:
		parsed, err := url.Parse(strings.TrimSpace(cfg.URL))
		if err != nil || !strings.EqualFold(parsed.Scheme, "https") {
			return CertificateTarget{}, false
		}
		host := parsed.Hostname()
		if host == "" {
			return CertificateTarget{}, false
		}
		port := 443
		if raw := parsed.Port(); raw != "" {
			if value, convErr := strconv.Atoi(raw); convErr == nil && value > 0 {
				port = value
			}
		}
		return CertificateTarget{Host: host, Port: port, ServerName: host, Insecure: cfg.IgnoreTLS}, true
	case models.MonitorTypeSSL:
		host := strings.TrimSpace(cfg.Host)
		if host == "" {
			return CertificateTarget{}, false
		}
		port := cfg.Port
		if port <= 0 {
			port = models.DefaultSSLPort
		}
		return CertificateTarget{Host: host, Port: port, ServerName: strings.TrimSpace(cfg.ServerName), Insecure: cfg.IgnoreTLS}, true
	}
	return CertificateTarget{}, false
}

// ---------------------------------------------------------------------------
// Domain targets
// ---------------------------------------------------------------------------

// NormalizeDomain returns the registrable domain (eTLD+1) of a host: the unit a
// registry actually bills. "app.example.com" and "www.example.com" both become
// "example.com", which is what removes the duplicate lookups.
//
// An IP address has no registry date, so it normalizes to the empty string.
func NormalizeDomain(host string) string {
	name := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))
	name = strings.Trim(name, "[]")
	if name == "" {
		return ""
	}
	if net.ParseIP(name) != nil {
		return ""
	}
	registrable, err := publicsuffix.EffectiveTLDPlusOne(name)
	if err != nil || registrable == "" {
		// A single label ("localhost") or a private suffix: keep the name as it
		// is so the operator still sees what was watched.
		return name
	}
	return registrable
}

// DomainName returns the registrable domain of a monitor target, whether or not
// the monitor watches its domain.
//
// The manual expiration date is a property of the DOMAIN, not of the watch: it
// is remembered even when the monitor that holds it has domain_watch off, so the
// write path (which mirrors the date and applies it to the stored observation)
// needs the name in that case too. DomainFor stays the watch gated answer the
// daily job iterates.
func DomainName(monitor *models.Monitor) string {
	if monitor == nil || !monitor.Type.SupportsDomainWatch() {
		return ""
	}
	return NormalizeDomain(hostFromMonitor(monitor))
}

// DomainFor derives the registrable domain of a monitor (empty when the target
// yields none, or when the monitor does not watch its domain).
func DomainFor(monitor *models.Monitor) string {
	if monitor == nil || !monitor.DomainWatch {
		return ""
	}
	return DomainName(monitor)
}

// hostFromMonitor extracts the hostname of the monitor target, whatever the type.
func hostFromMonitor(monitor *models.Monitor) string {
	cfg := monitor.Config
	switch monitor.Type {
	case models.MonitorTypeHTTP, models.MonitorTypeKeyword:
		return hostOnly(cfg.URL)
	case models.MonitorTypeDNS:
		return hostOnly(cfg.Hostname)
	default:
		return hostOnly(cfg.Host)
	}
}

// hostOnly strips a scheme, user info and a port from a target.
func hostOnly(raw string) string {
	value := strings.TrimSpace(raw)
	if value == "" {
		return ""
	}
	if strings.Contains(value, "://") {
		if parsed, err := url.Parse(value); err == nil {
			value = parsed.Host
		}
	}
	if host, _, err := net.SplitHostPort(value); err == nil {
		value = host
	}
	return strings.Trim(value, "[]")
}
