package models

import (
	"reflect"
	"testing"
)

// TestMonitorConfigPruneToType documents the Go twin of `CONFIG_FIELDS` of the
// UI: only the options of the type survive, and the options shared by several
// types (host/port for tcp and ssl, ignore_tls and the credentials for http,
// keyword and websocket, send/expect for tcp and websocket, ip_family for every
// type) are kept.
func TestMonitorConfigPruneToType(t *testing.T) {
	full := MonitorConfig{
		URL: "https://example.com", Method: "POST", Encoding: "xml", Body: "{}",
		Headers: []Header{{Key: "X", Value: "1"}}, AuthType: "bearer", BearerToken: "secret",
		IgnoreTLS: true, MaxRedirects: 3, CacheBuster: true, AcceptedStatusCodes: "200",
		Keyword: "ok", InvertKeyword: true, CaseSensitive: true,
		Host: "db.internal", Port: 5432, Send: "PING", Expect: "PONG",
		Hostname: "example.com", ResolverServer: "8.8.8.8", RecordType: "AAAA",
		ExpectedValue: "93.184", InvertCheck: true,
		ServerName: "shop.example.com",
		IPFamily:   IPFamily6,
	}

	tcp := full.PruneToType(MonitorTypeTCP)
	if tcp.Host != "db.internal" || tcp.Port != 5432 || tcp.Send != "PING" || tcp.Expect != "PONG" {
		t.Fatalf("tcp config = %+v", tcp)
	}
	if tcp.URL != "" || tcp.Method != "" || tcp.Headers != nil || tcp.Hostname != "" ||
		tcp.RecordType != "" || tcp.BearerToken != "" || tcp.ServerName != "" {
		t.Fatalf("the options of the other types must be dropped: %+v", tcp)
	}

	ssl := full.PruneToType(MonitorTypeSSL)
	if ssl.Host != "db.internal" || ssl.Port != 5432 || ssl.ServerName != "shop.example.com" || !ssl.IgnoreTLS {
		t.Fatalf("ssl config = %+v", ssl)
	}
	if ssl.Send != "" || ssl.Expect != "" || ssl.URL != "" {
		t.Fatalf("the tcp/http options must be dropped: %+v", ssl)
	}

	http := full.PruneToType(MonitorTypeHTTP)
	if http.URL != "https://example.com" || http.Method != "POST" || !http.IgnoreTLS || http.AcceptedStatusCodes != "200" {
		t.Fatalf("http config = %+v", http)
	}
	if http.Keyword != "" || http.Host != "" || http.Hostname != "" || http.ServerName != "" {
		t.Fatalf("the options of the other types must be dropped: %+v", http)
	}

	keyword := full.PruneToType(MonitorTypeKeyword)
	if keyword.URL != "https://example.com" || keyword.Keyword != "ok" || !keyword.InvertKeyword || !keyword.CaseSensitive {
		t.Fatalf("keyword config = %+v", keyword)
	}
	if keyword.Host != "" || keyword.Hostname != "" {
		t.Fatalf("the options of the other types must be dropped: %+v", keyword)
	}

	ws := full.PruneToType(MonitorTypeWebSocket)
	if ws.URL != "https://example.com" || len(ws.Headers) != 1 || ws.BearerToken != "secret" ||
		!ws.IgnoreTLS || ws.Send != "PING" || ws.Expect != "PONG" {
		t.Fatalf("websocket config = %+v", ws)
	}
	if ws.Method != "" || ws.Host != "" || ws.Keyword != "" || ws.Hostname != "" || ws.ServerName != "" {
		t.Fatalf("the options of the other types must be dropped: %+v", ws)
	}

	dns := full.PruneToType(MonitorTypeDNS)
	if dns.Hostname != "example.com" || dns.RecordType != "AAAA" || !dns.InvertCheck {
		t.Fatalf("dns config = %+v", dns)
	}
	if dns.URL != "" || dns.Host != "" || dns.Method != "" {
		t.Fatalf("the options of the other types must be dropped: %+v", dns)
	}

	// An unknown type keeps nothing rather than leaking every option.
	if pruned := full.PruneToType(MonitorType("planet")); !reflect.DeepEqual(pruned, MonitorConfig{}) {
		t.Fatalf("unknown type = %+v", pruned)
	}

	// The address family belongs to every type: a bulk row that changes the type
	// of its template must not drop it (the UI saves the five lists of
	// CONFIG_FIELDS, so a missing branch would silently reset the monitor to the
	// default rotation).
	for _, monitorType := range []MonitorType{MonitorTypeHTTP, MonitorTypeKeyword, MonitorTypeTCP, MonitorTypeDNS, MonitorTypeSSL, MonitorTypeWebSocket} {
		if got := full.PruneToType(monitorType); got.IPFamily != IPFamily6 {
			t.Fatalf("%s kept ip_family = %q, want %q", monitorType, got.IPFamily, IPFamily6)
		}
	}
}

// TestMonitorConfigNormalizeIPFamily covers the default the checkers rely on: a
// stored monitor (or an old payload) without the field ends up as `auto` (the
// dialer chooses), never as an empty preference - and an unreadable value is left
// alone, so Validate rejects the write instead of silently applying the default.
func TestMonitorConfigNormalizeIPFamily(t *testing.T) {
	cases := map[string]string{
		"":     IPFamilyAuto,
		"nope": "nope",
		"IPv6": IPFamily6,
		"auto": IPFamilyAuto,
		"ipv4": IPFamily4,
	}
	for input, want := range cases {
		config := MonitorConfig{IPFamily: input}
		config.Normalize(MonitorTypeHTTP)
		if config.IPFamily != want {
			t.Errorf("Normalize(%q) produced ip_family = %q, want %q", input, config.IPFamily, want)
		}
	}
	// The default of a fresh monitor is the dialer's own choice (auto), whatever
	// the type.
	for _, monitorType := range []MonitorType{MonitorTypeHTTP, MonitorTypeKeyword, MonitorTypeTCP, MonitorTypeDNS, MonitorTypeSSL, MonitorTypeWebSocket} {
		config := MonitorConfig{}
		config.Normalize(monitorType)
		if config.IPFamily != DefaultIPFamily {
			t.Errorf("%s default ip_family = %q, want %q", monitorType, config.IPFamily, DefaultIPFamily)
		}
	}
}

// TestMonitorConfigLighthouseStrategy documents where the strategy lives: it
// travels with the two HTTP(s) types PageSpeed Insights can audit (defaulting to
// mobile) and it is dropped from every other type, like the web performance
// switch itself.
func TestMonitorConfigLighthouseStrategy(t *testing.T) {
	config := MonitorConfig{URL: "https://example.com", LighthouseStrategy: "desktop"}
	config.Normalize(MonitorTypeHTTP)
	if config.LighthouseStrategy != LighthouseStrategyDesktop {
		t.Fatalf("strategy = %q", config.LighthouseStrategy)
	}
	if pruned := config.PruneToType(MonitorTypeHTTP); pruned.LighthouseStrategy != LighthouseStrategyDesktop {
		t.Fatalf("the http prune must keep the strategy: %+v", pruned)
	}
	if pruned := config.PruneToType(MonitorTypeKeyword); pruned.LighthouseStrategy != LighthouseStrategyDesktop {
		t.Fatalf("the keyword prune must keep the strategy: %+v", pruned)
	}
	if pruned := config.PruneToType(MonitorTypeTCP); pruned.LighthouseStrategy != "" {
		t.Fatalf("the tcp prune must drop the strategy: %+v", pruned)
	}

	empty := MonitorConfig{URL: "https://example.com"}
	empty.Normalize(MonitorTypeHTTP)
	if empty.LighthouseStrategy != DefaultLighthouseStrategy {
		t.Fatalf("a monitor without a strategy must default to %q, got %q",
			DefaultLighthouseStrategy, empty.LighthouseStrategy)
	}

	if problem := (&MonitorConfig{URL: "https://example.com", Method: "GET", Encoding: "json",
		LighthouseStrategy: "phone"}).Validate(MonitorTypeHTTP); problem == "" {
		t.Fatal("an unknown strategy must be rejected")
	}
}

// TestMonitorConfigLighthouseInterval documents where the cadence lives: with the
// two HTTP(s) types, defaulting to daily, dropped from every other type and
// rejected when it is not one of the accepted values.
func TestMonitorConfigLighthouseInterval(t *testing.T) {
	config := MonitorConfig{URL: "https://example.com", LighthouseInterval: "6h"}
	config.Normalize(MonitorTypeHTTP)
	if config.LighthouseInterval != LighthouseIntervalEvery6H {
		t.Fatalf("interval = %q", config.LighthouseInterval)
	}
	if pruned := config.PruneToType(MonitorTypeHTTP); pruned.LighthouseInterval != LighthouseIntervalEvery6H {
		t.Fatalf("the http prune must keep the cadence: %+v", pruned)
	}
	if pruned := config.PruneToType(MonitorTypeKeyword); pruned.LighthouseInterval != LighthouseIntervalEvery6H {
		t.Fatalf("the keyword prune must keep the cadence: %+v", pruned)
	}
	if pruned := config.PruneToType(MonitorTypeTCP); pruned.LighthouseInterval != "" {
		t.Fatalf("the tcp prune must drop the cadence: %+v", pruned)
	}

	empty := MonitorConfig{URL: "https://example.com"}
	empty.Normalize(MonitorTypeHTTP)
	if empty.LighthouseInterval != DefaultLighthouseInterval {
		t.Fatalf("a monitor without a cadence must default to %q, got %q",
			DefaultLighthouseInterval, empty.LighthouseInterval)
	}

	if problem := (&MonitorConfig{URL: "https://example.com", Method: "GET", Encoding: "json",
		LighthouseInterval: "minutely"}).Validate(MonitorTypeHTTP); problem == "" {
		t.Fatal("an unknown cadence must be rejected")
	}
	// An absent cadence stays valid: the job setting applies to such a row.
	if problem := (&MonitorConfig{URL: "https://example.com", Method: "GET", Encoding: "json",
		LighthouseInterval: ""}).Validate(MonitorTypeHTTP); problem != "" {
		t.Fatalf("an omitted cadence must be valid: %s", problem)
	}
}

// TestMonitorConfigWithoutAuth documents the helper that makes a template
// credential free: every credential goes, the auth type falls back to "none", and
// nothing else of the probe is touched (a monitor created from the template must
// still know how to probe).
func TestMonitorConfigWithoutAuth(t *testing.T) {
	full := MonitorConfig{
		URL: "https://example.com", Method: "POST", Encoding: "xml", Body: "{}",
		Headers:     []Header{{Key: "X", Value: "1"}},
		AuthType:    "basic",
		BasicUser:   "operator",
		BasicPass:   "hunter2",
		BearerToken: "token",
		IgnoreTLS:   true, MaxRedirects: 3, AcceptedStatusCodes: "200",
		Keyword: "ok",
	}

	clean := full.WithoutAuth()
	if clean.AuthType != "none" || clean.BasicUser != "" || clean.BasicPass != "" || clean.BearerToken != "" {
		t.Fatalf("credentials survived: %+v", clean)
	}
	if clean.URL != full.URL || clean.Method != full.Method || clean.Encoding != full.Encoding ||
		clean.Body != full.Body || !clean.IgnoreTLS || clean.MaxRedirects != full.MaxRedirects ||
		clean.AcceptedStatusCodes != full.AcceptedStatusCodes || clean.Keyword != full.Keyword {
		t.Fatalf("the probe options must be kept: %+v", clean)
	}
	if len(clean.Headers) != 1 || clean.Headers[0].Key != "X" {
		t.Fatalf("the headers must be kept: %+v", clean.Headers)
	}
	// The receiver is a value, so stripping twice (Normalize then the store) is
	// harmless and the original configuration is never mutated.
	if full.AuthType != "basic" || full.BasicPass != "hunter2" {
		t.Fatalf("the source config was mutated: %+v", full)
	}
}
