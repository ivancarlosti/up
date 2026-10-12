package models

import (
	"fmt"
	"strings"
)

// Validate checks the type specific configuration and returns a human readable
// problem description. An empty string means the configuration is valid.
func (c *MonitorConfig) Validate(monitorType MonitorType) string {
	// The address family is a shared field, so it is checked for every type.
	if !ValidIPFamily(c.IPFamily) {
		return fmt.Sprintf("config.ip_family must be one of %s", strings.Join(IPFamilies, ", "))
	}
	switch monitorType {
	case MonitorTypeHTTP, MonitorTypeKeyword:
		if c.URL == "" {
			return "config.url is required for HTTP and Keyword monitors"
		}
		if !strings.HasPrefix(c.URL, "http://") && !strings.HasPrefix(c.URL, "https://") {
			return "config.url must start with http:// or https://"
		}
		if !containsString(HTTPMethods, c.Method) {
			return fmt.Sprintf("config.method must be one of %s", strings.Join(HTTPMethods, ", "))
		}
		if !containsString(HTTPEncodings, c.Encoding) {
			return fmt.Sprintf("config.encoding must be one of %s", strings.Join(HTTPEncodings, ", "))
		}
		// The credentials are shared with the WebSocket probe, so they are
		// checked by the same helper.
		if problem := validateAuthConfig(c); problem != "" {
			return problem
		}
		if _, err := ParseStatusRanges(c.AcceptedStatusCodes); err != nil {
			return err.Error()
		}
		// The strategy only describes the web performance run, but it travels
		// with the HTTP configuration, so it is checked with it.
		if c.LighthouseStrategy != "" && !ValidLighthouseStrategy(strings.ToLower(strings.TrimSpace(c.LighthouseStrategy))) {
			return fmt.Sprintf("config.lighthouse_strategy must be one of %s", strings.Join(LighthouseStrategies, ", "))
		}
		// The cadence travels with the same configuration; it is normalized to
		// the documented default on write, so it is only ever absent on a row
		// written before the field existed (which is valid: the job cadence
		// applies).
		if c.LighthouseInterval != "" && !ValidLighthouseInterval(strings.ToLower(strings.TrimSpace(c.LighthouseInterval))) {
			return fmt.Sprintf("config.lighthouse_interval must be one of %s", strings.Join(LighthouseIntervals, ", "))
		}
		if monitorType == MonitorTypeKeyword && strings.TrimSpace(c.Keyword) == "" {
			return "config.keyword is required for Keyword monitors"
		}
	case MonitorTypeTCP:
		if c.Host == "" {
			return "config.host is required for TCP monitors"
		}
		if c.Port < 1 || c.Port > 65535 {
			return "config.port must be between 1 and 65535"
		}
	case MonitorTypeDNS:
		if c.Hostname == "" {
			return "config.hostname is required for DNS monitors"
		}
		if !containsString(DNSRecordTypes, c.RecordType) {
			return fmt.Sprintf("config.record_type must be one of %s", strings.Join(DNSRecordTypes, ", "))
		}
		if c.ResolverServer == "" {
			return "config.resolver_server is required for DNS monitors"
		}
	case MonitorTypeSSL:
		if c.Host == "" {
			return "config.host is required for SSL monitors"
		}
		if c.Port < 1 || c.Port > 65535 {
			return "config.port must be between 1 and 65535"
		}
	case MonitorTypeWebSocket:
		if c.URL == "" {
			return "config.url is required for WebSocket monitors"
		}
		if !strings.HasPrefix(c.URL, "ws://") && !strings.HasPrefix(c.URL, "wss://") {
			return "config.url must start with ws:// or wss://"
		}
		if problem := validateAuthConfig(c); problem != "" {
			return problem
		}
	default:
		return fmt.Sprintf("unknown monitor type %q", monitorType)
	}
	return ""
}

// validateAuthConfig checks the authentication options shared by the HTTP,
// Keyword and WebSocket probes: the same three modes (none, basic, bearer) and
// the same required credentials. It returns the problem description, or "" when
// the configuration describes a usable request.
func validateAuthConfig(c *MonitorConfig) string {
	switch c.AuthType {
	case "", "none":
	case "basic":
		if c.BasicUser == "" || c.BasicPass == "" {
			return "config.basic_user and config.basic_pass are required for basic authentication"
		}
	case "bearer":
		if c.BearerToken == "" {
			return "config.bearer_token is required for bearer authentication"
		}
	default:
		return "config.auth_type must be none, basic or bearer"
	}
	return ""
}

// StatusRange is an inclusive range of accepted HTTP status codes.
type StatusRange struct {
	Min int `json:"min"`
	Max int `json:"max"`
}

// Contains reports whether the given status code is accepted.
func (r StatusRange) Contains(code int) bool {
	return code >= r.Min && code <= r.Max
}

// ParseStatusRanges turns "200-299,301,404" into a list of inclusive ranges.
// An empty input defaults to 200-299.
func ParseStatusRanges(raw string) ([]StatusRange, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return []StatusRange{{Min: 200, Max: 299}}, nil
	}
	ranges := make([]StatusRange, 0, 4)
	for _, part := range SplitList(raw) {
		if strings.Contains(part, "-") {
			bounds := strings.SplitN(part, "-", 2)
			min, errMin := parseHTTPCode(bounds[0])
			max, errMax := parseHTTPCode(bounds[1])
			if errMin != nil || errMax != nil || min > max {
				return nil, fmt.Errorf("invalid accepted status code range %q", part)
			}
			ranges = append(ranges, StatusRange{Min: min, Max: max})
			continue
		}
		code, err := parseHTTPCode(part)
		if err != nil {
			return nil, fmt.Errorf("invalid accepted status code %q", part)
		}
		ranges = append(ranges, StatusRange{Min: code, Max: code})
	}
	if len(ranges) == 0 {
		return nil, fmt.Errorf("invalid accepted status codes %q", raw)
	}
	return ranges, nil
}

// StatusAccepted reports whether the code matches any of the ranges.
func StatusAccepted(ranges []StatusRange, code int) bool {
	for _, r := range ranges {
		if r.Contains(code) {
			return true
		}
	}
	return false
}

func parseHTTPCode(raw string) (int, error) {
	raw = strings.TrimSpace(raw)
	var code int
	if _, err := fmt.Sscanf(raw, "%d", &code); err != nil {
		return 0, err
	}
	if code < 100 || code > 599 {
		return 0, fmt.Errorf("status code out of range: %d", code)
	}
	return code, nil
}

func containsString(list []string, value string) bool {
	for _, item := range list {
		if item == value {
			return true
		}
	}
	return false
}
