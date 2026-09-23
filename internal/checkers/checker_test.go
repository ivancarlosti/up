package checkers

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/miekg/dns"

	"github.com/ivancarlosti/up/internal/models"
)

func httpMonitor(url string, extra func(*models.MonitorConfig)) *models.Monitor {
	config := models.MonitorConfig{URL: url}
	if extra != nil {
		extra(&config)
	}
	monitor := &models.Monitor{Name: "test", Type: models.MonitorTypeHTTP, TimeoutSeconds: 5, Config: config}
	monitor.Config.Normalize(monitor.Type)
	return monitor
}

func TestCheckHTTP(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/health":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("all good"))
		case "/teapot":
			w.WriteHeader(http.StatusTeapot)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	t.Run("accepted status is up", func(t *testing.T) {
		result := Check(context.Background(), httpMonitor(server.URL+"/health", nil))
		if result.Status != models.StatusUp {
			t.Fatalf("status = %v, message = %s", result.Status, result.Message)
		}
		if result.StatusCode != http.StatusOK {
			t.Fatalf("status code = %d", result.StatusCode)
		}
	})

	t.Run("unexpected status is down", func(t *testing.T) {
		result := Check(context.Background(), httpMonitor(server.URL+"/teapot", nil))
		if result.Status != models.StatusDown {
			t.Fatalf("status = %v", result.Status)
		}
	})

	t.Run("accepted codes are honoured", func(t *testing.T) {
		result := Check(context.Background(), httpMonitor(server.URL+"/teapot", func(c *models.MonitorConfig) {
			c.AcceptedStatusCodes = "200-299,418"
		}))
		if result.Status != models.StatusUp {
			t.Fatalf("status = %v, message = %s", result.Status, result.Message)
		}
	})

	t.Run("unreachable target is down", func(t *testing.T) {
		result := Check(context.Background(), httpMonitor("http://127.0.0.1:1/", nil))
		if result.Status != models.StatusDown {
			t.Fatalf("status = %v", result.Status)
		}
	})

	t.Run("upside down inverts the result", func(t *testing.T) {
		monitor := httpMonitor("http://127.0.0.1:1/", nil)
		monitor.UpsideDown = true
		result := Check(context.Background(), monitor)
		if result.Status != models.StatusUp {
			t.Fatalf("status = %v", result.Status)
		}
	})
}

// TestCheckHTTPCacheBuster covers the cache_buster option: by default the
// parameter is absent and a query string typed in the URL is untouched; when
// enabled the parameter is added with a fresh value on every check and the
// operator's own parameters survive.
func TestCheckHTTPCacheBuster(t *testing.T) {
	var (
		mu       sync.Mutex
		requests []url.Values
	)
	last := func() url.Values {
		mu.Lock()
		defer mu.Unlock()
		if len(requests) == 0 {
			return url.Values{}
		}
		return requests[len(requests)-1]
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requests = append(requests, r.URL.Query())
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	t.Run("disabled leaves the query string alone", func(t *testing.T) {
		result := Check(context.Background(), httpMonitor(server.URL+"/health?page=2", nil))
		if result.Status != models.StatusUp {
			t.Fatalf("status = %v, message = %s", result.Status, result.Message)
		}
		if value := last().Get(cacheBusterParam); value != "" {
			t.Fatalf("cache buster sent while disabled: %q", value)
		}
		if page := last().Get("page"); page != "2" {
			t.Fatalf("existing query parameter lost: page = %q", page)
		}
	})

	t.Run("enabled sends a fresh value per check", func(t *testing.T) {
		monitor := httpMonitor(server.URL+"/health?page=2", func(c *models.MonitorConfig) {
			c.CacheBuster = true
		})

		Check(context.Background(), monitor)
		first := last().Get(cacheBusterParam)
		Check(context.Background(), monitor)
		second := last().Get(cacheBusterParam)

		if first == "" || second == "" {
			t.Fatalf("cache buster missing: first = %q, second = %q", first, second)
		}
		if first == second {
			t.Fatalf("cache buster did not change between checks: %q", first)
		}
		if page := last().Get("page"); page != "2" {
			t.Fatalf("existing query parameter lost: page = %q", page)
		}
	})

	t.Run("applies to keyword monitors too", func(t *testing.T) {
		config := models.MonitorConfig{URL: server.URL + "/health", Keyword: "ok", CacheBuster: true}
		monitor := &models.Monitor{Name: "kw", Type: models.MonitorTypeKeyword, TimeoutSeconds: 5, Config: config}
		monitor.Config.Normalize(monitor.Type)

		Check(context.Background(), monitor)
		if value := last().Get(cacheBusterParam); value == "" {
			t.Fatal("cache buster missing on a keyword monitor")
		}
	})
}

func TestCheckKeyword(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("Welcome to the API"))
	}))
	defer server.Close()

	keyword := func(extra func(*models.MonitorConfig)) *models.Monitor {
		config := models.MonitorConfig{URL: server.URL, Keyword: "welcome"}
		if extra != nil {
			extra(&config)
		}
		monitor := &models.Monitor{Name: "kw", Type: models.MonitorTypeKeyword, TimeoutSeconds: 5, Config: config}
		monitor.Config.Normalize(monitor.Type)
		return monitor
	}

	if result := Check(context.Background(), keyword(nil)); result.Status != models.StatusUp {
		t.Fatalf("case insensitive match failed: %v %s", result.Status, result.Message)
	}

	if result := Check(context.Background(), keyword(func(c *models.MonitorConfig) { c.CaseSensitive = true })); result.Status != models.StatusDown {
		t.Fatalf("case sensitive mismatch should be down: %v", result.Status)
	}

	if result := Check(context.Background(), keyword(func(c *models.MonitorConfig) { c.InvertKeyword = true })); result.Status != models.StatusDown {
		t.Fatalf("an inverted keyword that is present should be down: %v", result.Status)
	}

	if result := Check(context.Background(), keyword(func(c *models.MonitorConfig) { c.Keyword = "absent"; c.InvertKeyword = true })); result.Status != models.StatusUp {
		t.Fatalf("an inverted keyword that is absent should be up: %v", result.Status)
	}
}

func TestCheckTCP(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = listener.Close() }()

	go func() {
		for {
			conn, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}
			_, _ = conn.Write([]byte("PONG"))
			_ = conn.Close()
		}
	}()

	port := listener.Addr().(*net.TCPAddr).Port
	config := models.MonitorConfig{Host: "127.0.0.1", Port: port, Send: "PING", Expect: "PONG"}
	monitor := &models.Monitor{Name: "tcp", Type: models.MonitorTypeTCP, TimeoutSeconds: 5, Config: config}
	monitor.Config.Normalize(monitor.Type)

	if result := Check(context.Background(), monitor); result.Status != models.StatusUp {
		t.Fatalf("tcp send/expect failed: %v %s", result.Status, result.Message)
	}

	monitor.Config.Expect = "NOPE"
	if result := Check(context.Background(), monitor); result.Status != models.StatusDown {
		t.Fatalf("a mismatched expectation must be down: %v", result.Status)
	}

	monitor.Config.Host = "127.0.0.1"
	monitor.Config.Port = 1
	if result := Check(context.Background(), monitor); result.Status != models.StatusDown {
		t.Fatalf("a refused connection must be down: %v", result.Status)
	}
}

// TestCheckDNS runs a real DNS server on a loopback port so the resolver path is
// covered without depending on the internet.
func TestCheckDNS(t *testing.T) {
	server := &dns.Server{Addr: "127.0.0.1:0", Net: "udp"}
	server.Handler = dns.HandlerFunc(func(w dns.ResponseWriter, r *dns.Msg) {
		message := new(dns.Msg)
		message.SetReply(r)
		for _, question := range r.Question {
			if question.Qtype == dns.TypeA && strings.HasPrefix(question.Name, "good.") {
				message.Answer = append(message.Answer, &dns.A{
					Hdr: dns.RR_Header{Name: question.Name, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 60},
					A:   net.ParseIP("192.0.2.42"),
				})
			}
		}
		_ = w.WriteMsg(message)
	})

	started := make(chan struct{})
	go func() {
		close(started)
		if err := server.ListenAndServe(); err != nil {
			t.Logf("dns server stopped: %v", err)
		}
	}()
	<-started
	time.Sleep(50 * time.Millisecond)
	defer func() { _ = server.Shutdown() }()

	resolver := server.PacketConn.LocalAddr().String()
	dnsMonitor := func(hostname, expected string, invert bool) *models.Monitor {
		config := models.MonitorConfig{Hostname: hostname, ResolverServer: resolver, RecordType: "A", ExpectedValue: expected, InvertCheck: invert}
		monitor := &models.Monitor{Name: "dns", Type: models.MonitorTypeDNS, TimeoutSeconds: 5, Config: config}
		monitor.Config.Normalize(monitor.Type)
		return monitor
	}

	if result := Check(context.Background(), dnsMonitor("good.example.com", "192.0.2.42", false)); result.Status != models.StatusUp {
		t.Fatalf("expected value match failed: %v %s", result.Status, result.Message)
	}
	if result := Check(context.Background(), dnsMonitor("good.example.com", "198.51.100.1", false)); result.Status != models.StatusDown {
		t.Fatalf("expected value mismatch must be down: %v", result.Status)
	}
	if result := Check(context.Background(), dnsMonitor("good.example.com", "198.51.100.1", true)); result.Status != models.StatusUp {
		t.Fatalf("inverted check should be up: %v %s", result.Status, result.Message)
	}
	if result := Check(context.Background(), dnsMonitor("missing.example.com", "", false)); result.Status != models.StatusDown {
		t.Fatalf("a name without records must be down: %v", result.Status)
	}
}

func TestDescribeRequestError(t *testing.T) {
	cases := map[string]string{
		"dial tcp: lookup nothing: no such host":            "DNS resolution failed",
		"dial tcp 127.0.0.1:1: connect: connection refused": "connection refused",
		"context deadline exceeded":                         "timeout exceeded",
	}
	for input, want := range cases {
		if got := describeRequestError(fmt.Errorf("%s", input)); got != want {
			t.Errorf("describeRequestError(%q) = %q, want %q", input, got, want)
		}
	}
}
