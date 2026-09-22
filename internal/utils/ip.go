package utils

import (
	"fmt"
	"net"
	"net/http"
	"strings"
)

// ParseRule normalizes an IP rule entry ("203.0.113.7", "203.0.113.0/24",
// "2001:db8::/32") into a *net.IPNet. Single addresses are converted to a
// /32 (IPv4) or /128 (IPv6) network.
func ParseRule(raw string) (*net.IPNet, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return nil, fmt.Errorf("empty IP rule")
	}
	if strings.Contains(value, "/") {
		_, network, err := net.ParseCIDR(value)
		if err != nil {
			return nil, fmt.Errorf("invalid CIDR %q: %w", value, err)
		}
		return network, nil
	}
	ip := net.ParseIP(value)
	if ip == nil {
		return nil, fmt.Errorf("invalid IP address %q", value)
	}
	if v4 := ip.To4(); v4 != nil {
		return &net.IPNet{IP: v4, Mask: net.CIDRMask(32, 32)}, nil
	}
	return &net.IPNet{IP: ip, Mask: net.CIDRMask(128, 128)}, nil
}

// MatchIP reports whether the address belongs to the rule.
func MatchIP(rule string, ip net.IP) bool {
	network, err := ParseRule(rule)
	if err != nil || ip == nil {
		return false
	}
	if v4 := ip.To4(); v4 != nil {
		if network.IP.To4() == nil {
			return false
		}
		return network.Contains(v4)
	}
	if network.IP.To4() != nil {
		return false
	}
	return network.Contains(ip)
}

// ClientIP extracts the client address from a request, honouring the
// X-Forwarded-For / X-Real-IP headers ONLY when trustedProxy is true (which is
// driven by APP_TRUST_PROXY). When the proxy is not trusted the headers are
// ignored on purpose, otherwise any client could spoof its address and bypass
// the IP rules.
func ClientIP(r *http.Request, trustedProxy bool) string {
	if trustedProxy {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			parts := strings.Split(xff, ",")
			if ip := net.ParseIP(strings.TrimSpace(parts[0])); ip != nil {
				return ip.String()
			}
		}
		if xrip := strings.TrimSpace(r.Header.Get("X-Real-IP")); xrip != "" {
			if ip := net.ParseIP(xrip); ip != nil {
				return ip.String()
			}
		}
	}

	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if zone := strings.Index(host, "%"); zone > 0 {
		host = host[:zone]
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.String()
	}
	return host
}

// RequestScheme returns the scheme the client used, honouring
// X-Forwarded-Proto when the proxy is trusted.
func RequestScheme(r *http.Request, trustedProxy bool) string {
	if trustedProxy {
		if proto := strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")); proto != "" {
			if idx := strings.Index(proto, ","); idx > 0 {
				proto = proto[:idx]
			}
			return strings.ToLower(strings.TrimSpace(proto))
		}
	}
	if r.TLS != nil {
		return "https"
	}
	return "http"
}
