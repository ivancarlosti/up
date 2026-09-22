package models

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestParseStatusRanges(t *testing.T) {
	ranges, err := ParseStatusRanges("200-299,301,404")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	cases := []struct {
		code int
		want bool
	}{
		{200, true},
		{299, true},
		{301, true},
		{404, true},
		{500, false},
		{300, false},
	}
	for _, tc := range cases {
		if got := StatusAccepted(ranges, tc.code); got != tc.want {
			t.Errorf("StatusAccepted(%d) = %v, want %v", tc.code, got, tc.want)
		}
	}

	single, err := ParseStatusRanges("204")
	if err != nil || !StatusAccepted(single, 204) || StatusAccepted(single, 200) {
		t.Fatalf("single code parsing failed: %v", err)
	}

	def, err := ParseStatusRanges("")
	if err != nil || !StatusAccepted(def, 250) || StatusAccepted(def, 300) {
		t.Fatalf("default range should be 200-299")
	}

	if _, err := ParseStatusRanges("999"); err == nil {
		t.Fatal("an out of range code must be rejected")
	}
	if _, err := ParseStatusRanges("300-200"); err == nil {
		t.Fatal("an inverted range must be rejected")
	}
}

func TestMonitorConfigNormalizeAndValidate(t *testing.T) {
	http := MonitorConfig{}
	http.Normalize(MonitorTypeHTTP)
	if http.Method != "GET" || http.Encoding != "json" || http.AuthType != "none" {
		t.Fatalf("unexpected HTTP defaults: %+v", http)
	}
	if http.AcceptedStatusCodes != "200-299" || http.MaxRedirects != 10 || http.Headers == nil {
		t.Fatalf("unexpected HTTP defaults: %+v", http)
	}
	if problem := http.Validate(MonitorTypeHTTP); problem == "" {
		t.Fatal("an HTTP monitor without URL must be invalid")
	}

	http.URL = "https://example.com/health"
	if problem := http.Validate(MonitorTypeHTTP); problem != "" {
		t.Fatalf("valid HTTP monitor rejected: %s", problem)
	}
	http.AuthType = "basic"
	if problem := http.Validate(MonitorTypeHTTP); problem == "" {
		t.Fatal("basic auth without credentials must be invalid")
	}

	keyword := MonitorConfig{URL: "https://example.com"}
	keyword.Normalize(MonitorTypeKeyword)
	if problem := keyword.Validate(MonitorTypeKeyword); problem == "" {
		t.Fatal("a keyword monitor without keyword must be invalid")
	}

	tcp := MonitorConfig{Host: "db.internal", Port: 3306}
	tcp.Normalize(MonitorTypeTCP)
	if problem := tcp.Validate(MonitorTypeTCP); problem != "" {
		t.Fatalf("valid TCP monitor rejected: %s", problem)
	}
	tcp.Port = 70000
	if problem := tcp.Validate(MonitorTypeTCP); problem == "" {
		t.Fatal("an out of range port must be invalid")
	}

	dnsConfig := MonitorConfig{Hostname: "example.com"}
	dnsConfig.Normalize(MonitorTypeDNS)
	if dnsConfig.RecordType != "A" || dnsConfig.ResolverServer != "1.1.1.1" {
		t.Fatalf("unexpected DNS defaults: %+v", dnsConfig)
	}
	if problem := dnsConfig.Validate(MonitorTypeDNS); problem != "" {
		t.Fatalf("valid DNS monitor rejected: %s", problem)
	}
	dnsConfig.RecordType = "SRV"
	if problem := dnsConfig.Validate(MonitorTypeDNS); problem == "" {
		t.Fatal("an unsupported record type must be invalid")
	}
}

func TestHeartbeatStatusParsing(t *testing.T) {
	// The status is persisted as an integer but exposed as its stable name, so
	// the conversion has to be exercised in both directions.
	if got := StatusUp.String(); got != "up" {
		t.Fatalf("StatusUp.String() = %q, want up", got)
	}
	for raw, want := range map[string]HeartbeatStatus{
		"0":           StatusDown,
		"down":        StatusDown,
		"1":           StatusUp,
		"up":          StatusUp,
		"pending":     StatusPending,
		"maintenance": StatusMaintenance,
	} {
		got, err := ParseHeartbeatStatus(raw)
		if err != nil || got != want {
			t.Fatalf("ParseHeartbeatStatus(%q) = %v (%v), want %v", raw, got, err, want)
		}
	}
	if _, err := ParseHeartbeatStatus("exploded"); err == nil {
		t.Fatal("an unknown status name must fail")
	}

	// Heartbeat.MarshalJSON exposes the readable name.
	encoded, err := json.Marshal(Heartbeat{MonitorID: 1, Status: StatusUp, Message: "200 OK"})
	if err != nil {
		t.Fatalf("marshal heartbeat: %v", err)
	}
	if !strings.Contains(string(encoded), `"status":"up"`) {
		t.Fatalf("heartbeat JSON does not expose the readable status: %s", encoded)
	}
}
