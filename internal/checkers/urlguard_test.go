package checkers

import (
	"context"
	"strings"
	"testing"

	"github.com/ivancarlosti/up/internal/models"
)

// TestProbeURLPattern documents the grammar accepted for the URL of a monitor:
// http(s) or ws(s), a host name or an IP literal, an optional port, path and
// query string - and nothing else.
func TestProbeURLPattern(t *testing.T) {
	cases := []struct {
		name   string
		target string
		want   bool
	}{
		{"a host name", "https://example.com", true},
		{"a host name with a trailing slash", "https://example.com/", true},
		{"a host name with a path and a query", "https://example.com/api/health?deep=1", true},
		{"a host name with a port", "http://app.internal:3000/login", true},
		{"an ipv4 address with a port", "http://127.0.0.1:9123/health", true},
		{"an ipv6 address", "https://[2001:db8::1]:8443/health", true},
		{"a bare ipv6 address", "https://[::1]/", true},
		{"a localhost target", "http://localhost:3000", true},
		{"a ws url", "ws://127.0.0.1:1/socket", true},
		{"a wss url", "wss://socket.example.com/live", true},
		{"a url with a percent escape", "https://example.com/a%20b", true},
		{"a url with a fragment", "https://example.com/page#section", true},
		{"an empty url", "", false},
		{"a file url", "file:///etc/passwd", false},
		{"a gopher url", "gopher://example.com:70/_anything", false},
		{"an ftp url", "ftp://example.com/file", false},
		{"a javascript url", "javascript:alert(1)", false},
		{"a data url", "data:text/html,<script>alert(1)</script>", false},
		{"a protocol relative url", "//169.254.169.254/latest/meta-data", false},
		{"a path without a host", "/api/health", false},
		{"a bare authority", "example.com:3000", false},
		{"an empty host", "http:///etc/passwd", false},
		{"embedded credentials", "https://user:secret@example.com/app", false},
		{"a header injection", "https://example.com/\r\nX-Injected: 1", false},
		{"a space in the path", "https://example.com/a b", false},
		{"a trailing newline", "https://example.com\n", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := probeURLPattern.MatchString(tc.target); got != tc.want {
				t.Errorf("probeURLPattern.MatchString(%q) = %v, want %v", tc.target, got, tc.want)
			}
		})
	}
}

// TestProbesRejectUndialableURLs covers the guard in both probes that take a
// URL: the refused target is down (never a panic, never a dial) and the message
// names the schemes the monitor accepts.
func TestProbesRejectUndialableURLs(t *testing.T) {
	targets := []struct {
		name   string
		target string
	}{
		{"a file url", "file:///etc/passwd"},
		{"a gopher url", "gopher://127.0.0.1:70/_anything"},
		{"embedded credentials", "http://user:secret@127.0.0.1:1/"},
		{"a bare authority", "127.0.0.1:1"},
		{"an empty host", "http:///health"},
	}

	t.Run("http", func(t *testing.T) {
		for _, tc := range targets {
			t.Run(tc.name, func(t *testing.T) {
				result := Check(context.Background(), httpMonitor(tc.target, nil), "")
				if result.Status != models.StatusDown {
					t.Fatalf("status = %v, want down", result.Status)
				}
				if !strings.Contains(result.Message, "invalid url") {
					t.Fatalf("message = %q, want the guard message", result.Message)
				}
				if !strings.Contains(result.Message, "http://") {
					t.Fatalf("message = %q, want the accepted schemes", result.Message)
				}
			})
		}
	})

	t.Run("keyword", func(t *testing.T) {
		monitor := httpMonitor(targets[0].target, nil)
		monitor.Type = models.MonitorTypeKeyword
		result := Check(context.Background(), monitor, "")
		if result.Status != models.StatusDown || !strings.Contains(result.Message, "invalid url") {
			t.Fatalf("status = %v, message = %q", result.Status, result.Message)
		}
	})

	t.Run("websocket", func(t *testing.T) {
		for _, tc := range targets {
			t.Run(tc.name, func(t *testing.T) {
				result := Check(context.Background(), websocketMonitor(tc.target, nil), "")
				if result.Status != models.StatusDown {
					t.Fatalf("status = %v, want down", result.Status)
				}
				if !strings.Contains(result.Message, "invalid url") {
					t.Fatalf("message = %q, want the guard message", result.Message)
				}
				if !strings.Contains(result.Message, "ws://") {
					t.Fatalf("message = %q, want the accepted schemes", result.Message)
				}
			})
		}
	})
}
