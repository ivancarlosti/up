package models

import (
	"reflect"
	"testing"
)

// TestMonitorConfigPruneToType documents the Go twin of `CONFIG_FIELDS` of the
// UI: only the options of the type survive, and the options shared by several
// types (host/port for tcp and ssl, ignore_tls for http/keyword/ssl) are kept.
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
}
