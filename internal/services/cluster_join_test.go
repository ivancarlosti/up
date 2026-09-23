package services

import (
	"context"
	"testing"

	"github.com/ivancarlosti/up/internal/config"
	"github.com/ivancarlosti/up/internal/i18n"
)

// TestClusterPrimaryURLPattern documents the grammar accepted for primary_url:
// a plain http(s) URL of a node, without credentials and without a query string
// or a fragment (the request below appends its own path to it).
func TestClusterPrimaryURLPattern(t *testing.T) {
	cases := []struct {
		name   string
		target string
		want   bool
	}{
		{"host and port", "http://up-node1:3000", true},
		{"https without a port", "https://up-node1", true},
		{"an ipv4 address with a path", "http://10.0.0.7:8080/up", true},
		{"an ipv6 address", "https://[2001:db8::1]:8443", true},
		{"a dns name with a trailing slash", "http://node-1.cluster.local:3000/", true},
		{"a file url", "file:///etc/passwd", false},
		{"a gopher url", "gopher://up-node1:70/_anything", false},
		{"a javascript url", "javascript:alert(1)", false},
		{"another scheme", "ftp://up-node1", false},
		{"a protocol relative url", "//up-node1:3000", false},
		{"a bare authority", "up-node1:3000", false},
		{"embedded credentials", "http://user:password@up-node1:3000", false},
		{"a query string", "http://up-node1:3000/?token=1", false},
		{"a fragment", "http://up-node1:3000/#fragment", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := clusterPrimaryURL.MatchString(tc.target); got != tc.want {
				t.Errorf("clusterPrimaryURL.MatchString(%q) = %v, want %v", tc.target, got, tc.want)
			}
		})
	}
}

// TestJoinRejectsURLsThatAreNotNodeURLs covers the guard in the join flow: the
// URL is refused (with the validation code the frontend translates) before the
// cluster private key can be sent to it.
func TestJoinRejectsURLsThatAreNotNodeURLs(t *testing.T) {
	service := &ClusterService{cfg: &config.Config{ClusterEnabled: true}}

	cases := []struct {
		name   string
		target string
	}{
		{"a file url", "file:///etc/passwd"},
		{"embedded credentials", "http://user:secret@primary:3000"},
		{"a protocol relative url", "//primary:3000"},
		{"a bare authority", "primary:3000"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := service.Join(context.Background(), JoinRequest{PrimaryURL: tc.target})
			apiErr, ok := err.(*APIError)
			if !ok {
				t.Fatalf("Join(%q) error = %v, want an *APIError", tc.target, err)
			}
			if apiErr.Status != 400 || apiErr.Code != i18n.CodeValidation {
				t.Errorf("Join(%q) = %d %s, want 400 %s", tc.target, apiErr.Status, apiErr.Code, i18n.CodeValidation)
			}
		})
	}
}
