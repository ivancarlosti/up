package models

import "strings"

// Header is a single HTTP header (used by HTTP monitors and by webhooks).
type Header struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// MonitorConfig holds every type specific option of a monitor. A single flat
// struct is used on purpose: it keeps the JSON column, the API payload and the
// UI form in sync, and Go simply ignores the fields that do not apply to the
// current monitor type.
type MonitorConfig struct {
	// --- HTTP(s) and HTTP(s) Keyword -------------------------------------
	URL                 string   `json:"url,omitempty"`
	Method              string   `json:"method,omitempty"`    // GET POST PUT PATCH DELETE HEAD OPTIONS
	Encoding            string   `json:"encoding,omitempty"`  // json | form | raw | xml
	Body                string   `json:"body,omitempty"`      // request body
	Headers             []Header `json:"headers,omitempty"`   // dynamic key/value list
	AuthType            string   `json:"auth_type,omitempty"` // none | basic | bearer
	BasicUser           string   `json:"basic_user,omitempty"`
	BasicPass           string   `json:"basic_pass,omitempty"`
	BearerToken         string   `json:"bearer_token,omitempty"`
	IgnoreTLS           bool     `json:"ignore_tls,omitempty"`            // ignore TLS/SSL errors
	MaxRedirects        int      `json:"max_redirects,omitempty"`         // 0 = no redirect, default 10
	CacheBuster         bool     `json:"cache_buster,omitempty"`          // append a random uptime_kuma_cachebuster parameter
	AcceptedStatusCodes string   `json:"accepted_status_codes,omitempty"` // "200-299,301"

	// --- Keyword ----------------------------------------------------------
	Keyword       string `json:"keyword,omitempty"`
	InvertKeyword bool   `json:"invert_keyword,omitempty"`
	CaseSensitive bool   `json:"case_sensitive,omitempty"`

	// --- TCP --------------------------------------------------------------
	Host   string `json:"host,omitempty"`
	Port   int    `json:"port,omitempty"`
	Send   string `json:"send,omitempty"`   // optional payload written after connect
	Expect string `json:"expect,omitempty"` // optional expected response (substring)

	// --- DNS --------------------------------------------------------------
	Hostname       string `json:"hostname,omitempty"`        // name to resolve
	ResolverServer string `json:"resolver_server,omitempty"` // 1.1.1.1 or 8.8.8.8:53
	RecordType     string `json:"record_type,omitempty"`     // A AAAA CNAME MX TXT NS SOA
	ExpectedValue  string `json:"expected_value,omitempty"`  // expected value / keyword
	InvertCheck    bool   `json:"invert_check,omitempty"`

	// --- SSL / TLS --------------------------------------------------------
	// ServerName is the SNI sent on the handshake of a ssl monitor (useful when
	// the certificate names a virtual host that the IP alone does not identify).
	ServerName string `json:"server_name,omitempty"`
}

// HTTPMethods lists the methods accepted by the HTTP and Keyword monitors.
var HTTPMethods = []string{"GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS"}

// HTTPEncodings lists the body encodings accepted by the HTTP monitors.
var HTTPEncodings = []string{"json", "form", "raw", "xml"}

// DNSRecordTypes lists the record types supported by the DNS monitor.
var DNSRecordTypes = []string{"A", "AAAA", "CNAME", "MX", "TXT", "NS", "SOA"}

// Normalize fills in the defaults of the optional fields so the checkers, the
// notifier and the UI can rely on them.
func (c *MonitorConfig) Normalize(monitorType MonitorType) {
	if c.Headers == nil {
		c.Headers = []Header{}
	}
	switch monitorType {
	case MonitorTypeHTTP, MonitorTypeKeyword:
		c.URL = strings.TrimSpace(c.URL)
		c.Method = strings.ToUpper(strings.TrimSpace(c.Method))
		if c.Method == "" {
			c.Method = "GET"
		}
		if c.Encoding == "" {
			c.Encoding = "json"
		}
		if c.AuthType == "" {
			c.AuthType = "none"
		}
		if c.MaxRedirects == 0 {
			c.MaxRedirects = 10
		}
		if strings.TrimSpace(c.AcceptedStatusCodes) == "" {
			c.AcceptedStatusCodes = "200-299"
		}
	case MonitorTypeDNS:
		c.Hostname = strings.TrimSpace(c.Hostname)
		c.RecordType = strings.ToUpper(strings.TrimSpace(c.RecordType))
		if c.RecordType == "" {
			c.RecordType = "A"
		}
		if strings.TrimSpace(c.ResolverServer) == "" {
			c.ResolverServer = "1.1.1.1"
		}
	case MonitorTypeTCP:
		c.Host = strings.TrimSpace(c.Host)
	case MonitorTypeSSL:
		c.Host = strings.TrimSpace(c.Host)
		c.ServerName = strings.TrimSpace(c.ServerName)
		if c.Port == 0 {
			c.Port = DefaultSSLPort
		}
	}
}

// DefaultSSLPort is the port a ssl monitor dials when none is configured.
const DefaultSSLPort = 443

// HeaderMap converts the dynamic header list into a map, ignoring empty keys.
func (c *MonitorConfig) HeaderMap() map[string]string {
	out := make(map[string]string, len(c.Headers))
	for _, h := range c.Headers {
		key := strings.TrimSpace(h.Key)
		if key == "" {
			continue
		}
		out[key] = h.Value
	}
	return out
}
