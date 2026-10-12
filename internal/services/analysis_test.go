package services

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/ivancarlosti/up/internal/config"
	"github.com/ivancarlosti/up/internal/i18n"
	"github.com/ivancarlosti/up/internal/lighthouse"
	"github.com/ivancarlosti/up/internal/models"
)

// psiReport is a trimmed copy of a real PageSpeed Insights answer: the four
// scored categories and the five vitals, nothing else. The stub of the
// performance cases returns it verbatim, so the analysis section under test
// carries the same numbers the lighthouse package's own test pins.
const psiReport = `{
  "kind": "pagespeedonline#result",
  "lighthouseResult": {
    "requestedUrl": "https://example.com/",
    "finalUrl": "https://www.example.com/",
    "fetchTime": "2026-10-10T18:12:31.442Z",
    "categories": {
      "performance": {"score": 0.62},
      "accessibility": {"score": 0.955},
      "best-practices": {"score": 1},
      "seo": {"score": 0.9}
    },
    "audits": {
      "first-contentful-paint": {"numericValue": 1834.512},
      "largest-contentful-paint": {"numericValue": 2450.75},
      "total-blocking-time": {"numericValue": 320.5},
      "cumulative-layout-shift": {"numericValue": 0.012},
      "speed-index": {"numericValue": 3120.9}
    }
  }
}`

// analysisTestLogger discards the service logs: the tests assert the ANSWER, and
// the debug line of every analysis would otherwise bury a failure.
func analysisTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// analysisTestConfig is the deployment of the tests: a node name, so the NodeID
// of the answer can be asserted, and no API key of its own.
func analysisTestConfig() *config.Config { return &config.Config{NodeID: "verify"} }

// newAnalysisService builds the service with nothing half wired: the two
// optional collaborators (the web performance job and the domain resolver) are
// nil, which is what the uptime and certificate cases need.
func newAnalysisService() *AnalysisService {
	return NewAnalysisService(analysisTestConfig(), analysisTestLogger(), nil, nil)
}

// newAnalysisServiceWithPSI builds the service with a web performance job that
// points at a local stub instead of Google.
func newAnalysisServiceWithPSI(psiURL string) *AnalysisService {
	cfg := analysisTestConfig()
	return NewAnalysisService(cfg, analysisTestLogger(), stubLighthouseService(cfg, psiURL), nil)
}

// stubLighthouseService is the web performance service of the tests.
//
// It has no database at all: MeasureTarget never touches one (and the whole
// point of the analysis endpoint is that it stores nothing), so a service that
// tried to record an observation would panic here instead of silently passing.
//
// The settings store is the cache alone (no database either) with the pace
// turned off, so the two runs of a "both" target do not sleep the production
// default (five minutes) between them.
func stubLighthouseService(cfg *config.Config, psiURL string) *LighthouseService {
	return &LighthouseService{
		cfg:     cfg,
		log:     analysisTestLogger(),
		limiter: lighthouse.NewLimiter(),
		settings: &SettingService{cache: map[string]string{
			models.SettingLighthouseRateLimitMS:    "0",
			models.SettingLighthouseTimeoutSeconds: "5",
			models.SettingLighthouseAPIKey:         "test-key",
		}},
		psiBaseURL: psiURL,
	}
}

// apiErrorOf unwraps the *APIError of a failed call.
func apiErrorOf(t *testing.T, err error) *APIError {
	t.Helper()
	if err == nil {
		t.Fatal("the call must fail, it returned no error")
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("error = %v, want an *APIError", err)
	}
	return apiErr
}

// serverPort reports the port a test server listens on, so a tcp or ssl target
// can point at it.
func serverPort(t *testing.T, server *httptest.Server) int {
	t.Helper()
	addr, ok := server.Listener.Addr().(*net.TCPAddr)
	if !ok {
		t.Fatalf("the test server listens on %v, want a TCP address", server.Listener.Addr())
	}
	return addr.Port
}

// TestPrepareResolvesTheDefaultChecksFromTheTarget pins the implicit list of the
// endpoint: the uptime probe plus, only when the TARGET really has a TLS
// endpoint, the certificate one.
//
// The rule is decided by the target and not by the type alone: an http monitor
// pointed at an http:// url supports a certificate check as a type but has none
// to read, and defaulting to it would answer every such request with a section
// that can only say "there is no certificate here".
func TestPrepareResolvesTheDefaultChecksFromTheTarget(t *testing.T) {
	service := newAnalysisService()
	cases := []struct {
		name string
		req  AnalysisRequest
		want []string
	}{
		{
			"a page in the clear has no certificate",
			AnalysisRequest{Type: models.MonitorTypeHTTP, Config: models.MonitorConfig{URL: "http://example.com/health"}},
			[]string{AnalysisCheckUptime},
		},
		{
			"a page over tls has one",
			AnalysisRequest{Type: models.MonitorTypeHTTP, Config: models.MonitorConfig{URL: "https://example.com/health"}},
			[]string{AnalysisCheckUptime, AnalysisCheckCertificate},
		},
		{
			"the keyword probe reads it the same way",
			AnalysisRequest{Type: models.MonitorTypeKeyword, Config: models.MonitorConfig{URL: "https://example.com", Keyword: "ok"}},
			[]string{AnalysisCheckUptime, AnalysisCheckCertificate},
		},
		{
			"a plain websocket has none",
			AnalysisRequest{Type: models.MonitorTypeWebSocket, Config: models.MonitorConfig{URL: "ws://example.com/socket"}},
			[]string{AnalysisCheckUptime},
		},
		{
			"an upgraded websocket has one",
			AnalysisRequest{Type: models.MonitorTypeWebSocket, Config: models.MonitorConfig{URL: "wss://example.com/socket"}},
			[]string{AnalysisCheckUptime, AnalysisCheckCertificate},
		},
		{
			"the ssl probe IS the handshake",
			AnalysisRequest{Type: models.MonitorTypeSSL, Config: models.MonitorConfig{Host: "example.com", Port: 443}},
			[]string{AnalysisCheckUptime, AnalysisCheckCertificate},
		},
		{
			"a tcp target has no chain to read",
			AnalysisRequest{Type: models.MonitorTypeTCP, Config: models.MonitorConfig{Host: "example.com", Port: 443}},
			[]string{AnalysisCheckUptime},
		},
		{
			"a dns target has none either",
			AnalysisRequest{Type: models.MonitorTypeDNS, Config: models.MonitorConfig{
				Hostname: "example.com", RecordType: "A", ResolverServer: "1.1.1.1",
			}},
			[]string{AnalysisCheckUptime},
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			monitor, checks, err := service.prepare(testCase.req)
			if err != nil {
				t.Fatalf("prepare: %v", err)
			}
			if strings.Join(checks, ",") != strings.Join(testCase.want, ",") {
				t.Fatalf("checks = %v, want %v", checks, testCase.want)
			}
			// The watch switches of the in-memory monitor follow the checks: the
			// probes read them, so a caller asking for a certificate does not
			// have to know that a monitor carries cert_watch for it.
			if monitor.CertWatch != containsCheck(checks, AnalysisCheckCertificate) {
				t.Fatalf("cert_watch = %v with checks %v", monitor.CertWatch, checks)
			}
			if monitor.DomainWatch || monitor.LighthouseEnabled {
				t.Fatalf("the opt-in checks must not be turned on: %+v", monitor)
			}
			// The monitor is a probe, never a row.
			if monitor.ID != 0 {
				t.Fatalf("the analysis monitor has an id: %d", monitor.ID)
			}
			// The defaults of the request options are filled in.
			if monitor.TimeoutSeconds != analysisDefaultTimeoutSeconds {
				t.Fatalf("timeout = %d", monitor.TimeoutSeconds)
			}
			if monitor.RetriesIntervalSeconds != analysisDefaultRetrySeconds {
				t.Fatalf("retry interval = %d", monitor.RetriesIntervalSeconds)
			}
		})
	}
}

// TestAnalyzeRunsTheUptimeProbeOfAnHTTPMonitor checks the happy path end to end
// against a local server: the verdict, the status code and the accepted range.
func TestAnalyzeRunsTheUptimeProbeOfAnHTTPMonitor(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("ok"))
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	service := newAnalysisService()
	result, err := service.Analyze(context.Background(), AnalysisRequest{
		Type:   models.MonitorTypeHTTP,
		Config: models.MonitorConfig{URL: server.URL + "/health", Method: "GET"},
	})
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if result.Target != server.URL+"/health" || result.Type != models.MonitorTypeHTTP {
		t.Fatalf("target = %q %q", result.Target, result.Type)
	}
	if result.NodeID != "verify" {
		t.Fatalf("node_id = %q, want the configured node", result.NodeID)
	}
	if result.GeneratedAt.IsZero() {
		t.Fatal("generated_at must be filled in")
	}
	if result.Uptime == nil || result.Uptime.Status != models.StatusUp.String() {
		t.Fatalf("uptime = %+v, want up", result.Uptime)
	}
	if result.Uptime.StatusCode != http.StatusOK {
		t.Fatalf("status_code = %d, want 200", result.Uptime.StatusCode)
	}
	if result.Uptime.Attempts != 1 {
		t.Fatalf("attempts = %d, want a single probe of an up target", result.Uptime.Attempts)
	}
	// Only the default checks ran: nothing the caller did not ask for.
	if result.Certificate != nil || result.Domain != nil || result.Performance != nil {
		t.Fatalf("an unrequested check ran: %+v", result)
	}
	if result.TimingsMS.Total < 0 {
		t.Fatalf("timings = %+v", result.TimingsMS)
	}

	// A status outside the accepted range is a DOWN verdict, exactly like a
	// monitor (200-299 is the default).
	failed, err := service.Analyze(context.Background(), AnalysisRequest{
		Type:   models.MonitorTypeHTTP,
		Config: models.MonitorConfig{URL: server.URL + "/broken", Method: "GET"},
	})
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if failed.Uptime.Status != models.StatusDown.String() || failed.Uptime.StatusCode != http.StatusInternalServerError {
		t.Fatalf("uptime = %+v, want down 500", failed.Uptime)
	}
}

// TestAnalyzeRetriesOnlyADownProbe pins the retry policy of the endpoint: an up
// target is probed once (a second look would only spend the timeout), a down one
// is retried as many times as the caller asked for, and the inverted verdict of
// an upside down analysis is the one of the last attempt (exactly what a monitor
// would store).
func TestAnalyzeRetriesOnlyADownProbe(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("hello world"))
	}))
	defer server.Close()

	service := newAnalysisService()
	request := AnalysisRequest{
		Type:                   models.MonitorTypeKeyword,
		Config:                 models.MonitorConfig{URL: server.URL, Keyword: "hello", Method: "GET"},
		Retries:                2,
		RetriesIntervalSeconds: 1,
	}

	up, err := service.Analyze(context.Background(), request)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if up.Uptime.Status != models.StatusUp.String() || up.Uptime.Attempts != 1 {
		t.Fatalf("uptime = %+v, want up after one probe", up.Uptime)
	}

	// The keyword is not there: the probe is down and uses both retries.
	request.Config.Keyword = "absent"
	down, err := service.Analyze(context.Background(), request)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if down.Uptime.Status != models.StatusDown.String() {
		t.Fatalf("uptime = %+v, want down", down.Uptime)
	}
	if down.Uptime.Attempts != 3 {
		t.Fatalf("attempts = %d, want 1+2 retries", down.Uptime.Attempts)
	}
	if !strings.Contains(down.Uptime.Message, "keyword not found") ||
		!strings.Contains(down.Uptime.Message, "absent") {
		t.Fatalf("message = %q, want the missing keyword", down.Uptime.Message)
	}

	// Upside down inverts the verdict, so a target that fails its keyword reads
	// as UP (and the message keeps the reason).
	request.UpsideDown = true
	flipped, err := service.Analyze(context.Background(), request)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if flipped.Uptime.Status != models.StatusUp.String() {
		t.Fatalf("uptime = %+v, want the inverted verdict", flipped.Uptime)
	}
	if !strings.HasPrefix(flipped.Uptime.Message, "upside down: ") {
		t.Fatalf("message = %q", flipped.Uptime.Message)
	}
}

// TestAnalyzeReadsTheCertificateOfATLSTarget covers the certificate section
// against a real handshake.
//
// The self signed chain of the test server is the interesting case: the section
// reports the failure AND the observation, because "expires in N days, issuer X"
// is the answer the caller came for (a monitor stores exactly the same pair).
func TestAnalyzeReadsTheCertificateOfATLSTarget(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	service := newAnalysisService()
	result, err := service.Analyze(context.Background(), AnalysisRequest{
		Type:   models.MonitorTypeHTTP,
		Config: models.MonitorConfig{URL: server.URL, Method: "GET"},
	})
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if strings.Join(result.Checks, ",") != AnalysisCheckUptime+","+AnalysisCheckCertificate {
		t.Fatalf("checks = %v, want the uptime and certificate defaults", result.Checks)
	}
	if result.Uptime == nil || result.Uptime.Status != models.StatusDown.String() {
		// The probe verifies the chain too, so the untrusted certificate makes
		// the target DOWN (which is what a browser would show).
		t.Fatalf("uptime = %+v, want down on an untrusted chain", result.Uptime)
	}
	section := result.Certificate
	if section == nil {
		t.Fatal("the certificate section is missing")
	}
	if section.Info == nil {
		t.Fatalf("the chain must be reported even when it does not verify: %+v", section)
	}
	if section.Status != analysisStatusError || section.Error == "" {
		t.Fatalf("certificate = %+v, want an error carrying the reason", section)
	}
	if section.Info.Issuer == "" || section.Info.NotAfter.IsZero() {
		t.Fatalf("certificate info = %+v", section.Info)
	}
	if section.DaysLeft != section.Info.DaysLeft {
		t.Fatalf("days_left = %d, info = %d", section.DaysLeft, section.Info.DaysLeft)
	}
	if section.Info.CapturedByNode != "verify" {
		t.Fatalf("captured_by_node = %q", section.Info.CapturedByNode)
	}

	// With the operator opt-in (ignore_tls) the same handshake completes: the
	// section is an answer and the remaining validity is positive.
	trusted, err := service.Analyze(context.Background(), AnalysisRequest{
		Type:   models.MonitorTypeHTTP,
		Config: models.MonitorConfig{URL: server.URL, Method: "GET", IgnoreTLS: true},
	})
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if trusted.Certificate == nil || trusted.Certificate.Status != analysisStatusOK {
		t.Fatalf("certificate = %+v, want ok", trusted.Certificate)
	}
	if trusted.Certificate.Error != "" || trusted.Certificate.Info == nil {
		t.Fatalf("certificate = %+v", trusted.Certificate)
	}
	if trusted.Certificate.DaysLeft <= 0 {
		t.Fatalf("days_left = %d, want a future expiration", trusted.Certificate.DaysLeft)
	}
	if trusted.Uptime.Status != models.StatusUp.String() {
		t.Fatalf("uptime = %+v", trusted.Uptime)
	}
}

// TestAnalyzeRunsExactlyTheRequestedChecks keeps the answer focused: a check
// list is lower cased, trimmed and deduplicated, and an unrequested probe leaves
// its section out of the answer.
func TestAnalyzeRunsExactlyTheRequestedChecks(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	service := newAnalysisService()
	result, err := service.Analyze(context.Background(), AnalysisRequest{
		Type:   models.MonitorTypeHTTP,
		Config: models.MonitorConfig{URL: server.URL, Method: "GET"},
		Checks: []string{" UPTIME ", "uptime", "Certificate"},
	})
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if strings.Join(result.Checks, ",") != AnalysisCheckUptime+","+AnalysisCheckCertificate {
		t.Fatalf("checks = %v, want the two named once", result.Checks)
	}

	// A certificate-only request does not run the uptime probe.
	only, err := service.Analyze(context.Background(), AnalysisRequest{
		Type:   models.MonitorTypeHTTP,
		Config: models.MonitorConfig{URL: server.URL, Method: "GET"},
		Checks: []string{AnalysisCheckCertificate},
	})
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if only.Uptime != nil {
		t.Fatalf("uptime = %+v, want no section at all", only.Uptime)
	}
	if only.Certificate == nil {
		t.Fatal("the certificate section is missing")
	}
	if strings.Join(only.Checks, ",") != AnalysisCheckCertificate {
		t.Fatalf("checks = %v", only.Checks)
	}
}

// analysisTargetFor builds the smallest valid request of a type, so a table can
// talk about the type and not about the configuration.
func analysisTargetFor(monitorType models.MonitorType) AnalysisRequest {
	switch monitorType {
	case models.MonitorTypeHTTP, models.MonitorTypeKeyword:
		return AnalysisRequest{Type: monitorType, Config: models.MonitorConfig{
			URL: "https://example.com/health", Keyword: "ok",
		}}
	case models.MonitorTypeTCP, models.MonitorTypeSSL:
		return AnalysisRequest{Type: monitorType, Config: models.MonitorConfig{Host: "example.com", Port: 443}}
	case models.MonitorTypeDNS:
		return AnalysisRequest{Type: monitorType, Config: models.MonitorConfig{
			Hostname: "example.com", RecordType: "A", ResolverServer: "1.1.1.1",
		}}
	case models.MonitorTypeWebSocket:
		return AnalysisRequest{Type: monitorType, Config: models.MonitorConfig{URL: "wss://example.com/socket"}}
	}
	return AnalysisRequest{Type: monitorType}
}

// TestAnalyzeRejectsAnImpossibleRequest pins the boundary between an invalid
// request (400, nothing runs) and an unreachable target (200, a down section).
//
// The codes are the ones of the monitor write path, so a client that already
// handles cert_watch or lighthouse_enabled recognizes the refusal.
func TestAnalyzeRejectsAnImpossibleRequest(t *testing.T) {
	tcp := analysisTargetFor(models.MonitorTypeTCP)
	dns := analysisTargetFor(models.MonitorTypeDNS)
	plainPage := AnalysisRequest{Type: models.MonitorTypeHTTP,
		Config: models.MonitorConfig{URL: "http://example.com/health"}}
	plainSocket := AnalysisRequest{Type: models.MonitorTypeWebSocket,
		Config: models.MonitorConfig{URL: "ws://example.com/socket"}}

	cases := []struct {
		name    string
		req     AnalysisRequest
		code    string
		message string
	}{
		{
			"an unknown type",
			AnalysisRequest{Type: "icmp"},
			i18n.CodeMonitorTypeInvalid, "type must be one of",
		},
		{
			"a target without a url",
			AnalysisRequest{Type: models.MonitorTypeHTTP},
			i18n.CodeMonitorConfig, "config.url is required",
		},
		{
			"a url without a scheme",
			AnalysisRequest{Type: models.MonitorTypeHTTP, Config: models.MonitorConfig{URL: "example.com"}},
			i18n.CodeMonitorConfig, "config.url must start with http://",
		},
		{
			"a timeout above the bound",
			AnalysisRequest{Type: tcp.Type, Config: tcp.Config, TimeoutSeconds: analysisMaxTimeoutSeconds + 1},
			i18n.CodeValidation, "timeout_seconds must be between 1 and 300",
		},
		{
			"a timeout below the bound",
			AnalysisRequest{Type: tcp.Type, Config: tcp.Config, TimeoutSeconds: -1},
			i18n.CodeValidation, "timeout_seconds must be between 1 and 300",
		},
		{
			"too many retries",
			AnalysisRequest{Type: tcp.Type, Config: tcp.Config, Retries: analysisMaxRetries + 1},
			i18n.CodeValidation, "retries must be between 0 and 5",
		},
		{
			"a negative retry pause",
			AnalysisRequest{Type: tcp.Type, Config: tcp.Config, RetriesIntervalSeconds: -1},
			i18n.CodeValidation, "retries_interval_seconds must be between 1 and 60",
		},
		{
			"a retry pause above the bound",
			AnalysisRequest{Type: tcp.Type, Config: tcp.Config, RetriesIntervalSeconds: analysisMaxRetrySeconds + 1},
			i18n.CodeValidation, "retries_interval_seconds must be between 1 and 60",
		},
		{
			"an unknown check",
			AnalysisRequest{Type: tcp.Type, Config: tcp.Config, Checks: []string{"latency"}},
			i18n.CodeValidation, "checks must be one of uptime, certificate, domain, performance",
		},
		{
			"a certificate of a tcp target",
			AnalysisRequest{Type: tcp.Type, Config: tcp.Config, Checks: []string{AnalysisCheckCertificate}},
			i18n.CodeMonitorCert, "the certificate check is only available for http, keyword, ssl and websocket targets",
		},
		{
			"a certificate of a page in the clear",
			AnalysisRequest{Type: plainPage.Type, Config: plainPage.Config, Checks: []string{AnalysisCheckCertificate}},
			i18n.CodeMonitorCert, "the certificate check needs an https:// or wss:// url, or a ssl host",
		},
		{
			"a certificate of a plain websocket",
			AnalysisRequest{Type: plainSocket.Type, Config: plainSocket.Config, Checks: []string{AnalysisCheckCertificate}},
			i18n.CodeMonitorCert, "the certificate check needs an https:// or wss:// url, or a ssl host",
		},
		{
			"a page score of a tcp target",
			AnalysisRequest{Type: tcp.Type, Config: tcp.Config, Checks: []string{AnalysisCheckPerformance}},
			i18n.CodeMonitorLighthouse, "the performance check is only available for http and keyword targets",
		},
		{
			"a page score of a dns target",
			AnalysisRequest{Type: dns.Type, Config: dns.Config, Checks: []string{AnalysisCheckPerformance}},
			i18n.CodeMonitorLighthouse, "the performance check is only available for http and keyword targets",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			result, err := newAnalysisService().Analyze(context.Background(), testCase.req)
			if result != nil {
				t.Fatalf("an impossible request must not produce an answer: %+v", result)
			}
			apiErr := apiErrorOf(t, err)
			if apiErr.Status != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", apiErr.Status)
			}
			if apiErr.Code != testCase.code {
				t.Fatalf("code = %q, want %q", apiErr.Code, testCase.code)
			}
			if !strings.Contains(apiErr.Message, testCase.message) {
				t.Fatalf("message = %q, want it to mention %q", apiErr.Message, testCase.message)
			}
		})
	}
}

// TestAnalyzeAcceptsTheDomainCheckEverywhere: every current type has a target a
// registrable domain can be derived from, so the registry lookup is never a
// request problem (whether it answers is the target's business).
func TestAnalyzeAcceptsTheDomainCheckEverywhere(t *testing.T) {
	service := newAnalysisService()
	for _, monitorType := range models.AllMonitorTypes() {
		request := analysisTargetFor(monitorType)
		request.Checks = []string{AnalysisCheckDomain}
		monitor, checks, err := service.prepare(request)
		if err != nil {
			t.Fatalf("%s: %v", monitorType, err)
		}
		if strings.Join(checks, ",") != AnalysisCheckDomain {
			t.Fatalf("%s: checks = %v", monitorType, checks)
		}
		if !monitor.DomainWatch {
			t.Fatalf("%s: the domain watch must follow the check", monitorType)
		}
		if monitor.CertWatch || monitor.LighthouseEnabled {
			t.Fatalf("%s: the other switches must stay off: %+v", monitorType, monitor)
		}
	}
}

// TestAnalyzeFailsOneSectionWithoutFailingTheRequest is the promise that makes
// the endpoint useful on a partly broken target: a TLS handshake that never
// happens (and a resolver that was never wired) is reported inside the section
// that asked for it, while the uptime probe still answers.
func TestAnalyzeFailsOneSectionWithoutFailingTheRequest(t *testing.T) {
	// A port nothing listens on: the connection is refused at once, which is
	// deterministic and offline.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	service := newAnalysisService()
	result, err := service.Analyze(context.Background(), AnalysisRequest{
		Type:   models.MonitorTypeSSL,
		Config: models.MonitorConfig{Host: "127.0.0.1", Port: port},
	})
	if err != nil {
		t.Fatalf("a refused target is an observation, not an error: %v", err)
	}
	if result.Uptime == nil || result.Uptime.Status != models.StatusDown.String() {
		t.Fatalf("uptime = %+v, want down", result.Uptime)
	}
	if result.Uptime.Message == "" {
		t.Fatal("a down verdict must say why")
	}
	if result.Certificate == nil || result.Certificate.Status != analysisStatusError {
		t.Fatalf("certificate = %+v, want an error section", result.Certificate)
	}
	if result.Certificate.Error == "" {
		t.Fatal("the certificate section must carry the reason")
	}
	if result.Certificate.Info != nil {
		t.Fatalf("there is no chain to report: %+v", result.Certificate.Info)
	}

	// A domain check on a deployment without a resolver is the same story: the
	// section explains itself, the request still succeeds.
	unwired, err := service.Analyze(context.Background(), AnalysisRequest{
		Type:   models.MonitorTypeHTTP,
		Config: models.MonitorConfig{URL: "https://example.com/"},
		Checks: []string{AnalysisCheckDomain},
	})
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if unwired.Domain == nil || unwired.Domain.Status != analysisStatusError {
		t.Fatalf("domain = %+v, want an error section", unwired.Domain)
	}
	if unwired.Domain.Error != "the domain resolver is not wired" {
		t.Fatalf("domain error = %q", unwired.Domain.Error)
	}
	if unwired.Uptime != nil {
		t.Fatalf("the uptime probe must not run: %+v", unwired.Uptime)
	}

	// With a resolver but a target that has no registrable domain (an IP), the
	// lookup is not attempted at all.
	wired := NewAnalysisService(analysisTestConfig(), analysisTestLogger(), nil,
		NewDomainService(nil, analysisTestConfig(), analysisTestLogger()))
	lookup, err := wired.Analyze(context.Background(), AnalysisRequest{
		Type:   models.MonitorTypeHTTP,
		Config: models.MonitorConfig{URL: "http://127.0.0.1/health"},
		Checks: []string{AnalysisCheckDomain},
	})
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if lookup.Domain == nil || lookup.Domain.Status != analysisStatusError {
		t.Fatalf("domain = %+v, want an error section", lookup.Domain)
	}
	if lookup.Domain.Error != "the target has no registrable domain to look up" {
		t.Fatalf("domain error = %q", lookup.Domain.Error)
	}
}

// psiStub is a local PageSpeed Insights endpoint: it records every call it
// received and answers with the shared report, or with the API's own error
// payload when the test asks it to refuse a strategy.
//
// It is what lets the performance cases run offline: the runner only reaches
// Google when the base URL is left alone.
type psiStub struct {
	server *httptest.Server
	mu     sync.Mutex
	calls  []url.Values
}

// newPSIStub starts the stub. `refuse` (optional) decides per strategy whether
// the call is answered with HTTP 429 and the API's error object.
func newPSIStub(t *testing.T, refuse func(strategy string) bool) *psiStub {
	t.Helper()
	stub := &psiStub{}
	stub.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		stub.mu.Lock()
		stub.calls = append(stub.calls, query)
		stub.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if refuse != nil && refuse(query.Get("strategy")) {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":{"code":429,"message":"Quota exceeded for quota metric 'Queries'",` +
				`"status":"RESOURCE_EXHAUSTED"}}`))
			return
		}
		_, _ = w.Write([]byte(psiReport))
	}))
	t.Cleanup(stub.server.Close)
	return stub
}

// strategies reports the strategy of every call the stub answered, in order.
func (s *psiStub) strategies() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.calls))
	for _, query := range s.calls {
		out = append(out, query.Get("strategy"))
	}
	return out
}

// TestAnalyzeMeasuresThePageThroughThePacedJob covers the performance section:
// the request carries the page, the strategy, the API key and the four
// categories, and the answer carries the scores and the vitals.
func TestAnalyzeMeasuresThePageThroughThePacedJob(t *testing.T) {
	stub := newPSIStub(t, nil)
	service := newAnalysisServiceWithPSI(stub.server.URL)

	result, err := service.Analyze(context.Background(), AnalysisRequest{
		Type:   models.MonitorTypeHTTP,
		Config: models.MonitorConfig{URL: "https://example.com", Method: "GET"},
		Checks: []string{AnalysisCheckPerformance},
	})
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	section := result.Performance
	if section == nil || section.Status != analysisStatusOK {
		t.Fatalf("performance = %+v, want an answer", section)
	}
	if section.Error != "" {
		t.Fatalf("performance error = %q", section.Error)
	}
	if len(section.Runs) != 1 || section.Runs[0].Strategy != models.LighthouseStrategyMobile {
		t.Fatalf("runs = %+v, want the single default run", section.Runs)
	}
	run := section.Runs[0]
	if run.Status != models.LighthouseStatusOK {
		t.Fatalf("run status = %q", run.Status)
	}
	if run.Performance == nil || *run.Performance != 62 {
		t.Fatalf("performance score = %v, want 62", run.Performance)
	}
	if run.Accessibility == nil || *run.Accessibility != 96 {
		t.Fatalf("accessibility = %v", run.Accessibility)
	}
	if run.BestPractices == nil || *run.BestPractices != 100 {
		t.Fatalf("best practices = %v", run.BestPractices)
	}
	if run.SEO == nil || *run.SEO != 90 {
		t.Fatalf("seo = %v", run.SEO)
	}
	if run.LargestContentfulPaintMS != 2450.75 || run.TotalBlockingTimeMS != 320.5 {
		t.Fatalf("vitals = %+v", run)
	}
	if run.FinalURL != "https://www.example.com/" {
		t.Fatalf("final url = %q, want the url the report measured", run.FinalURL)
	}
	// A measurement is not stored, but it is stamped: the caller learns which
	// node measured and when.
	if run.RanByNode != "verify" || run.RanAt.IsZero() {
		t.Fatalf("run = %+v, want the node and the instant", run)
	}

	// The outbound call is the one of the job: normalized page, strategy, the
	// operator key and the four categories asked for explicitly.
	if len(stub.calls) != 1 {
		t.Fatalf("calls = %d, want one", len(stub.calls))
	}
	query := stub.calls[0]
	if query.Get("url") != "https://example.com/" {
		t.Fatalf("url = %q, want the canonicalized page", query.Get("url"))
	}
	if query.Get("strategy") != models.LighthouseStrategyMobile {
		t.Fatalf("strategy = %q", query.Get("strategy"))
	}
	if query.Get("key") != "test-key" {
		t.Fatalf("key = %q, want the configured key", query.Get("key"))
	}
	if len(query["category"]) != 4 {
		t.Fatalf("categories = %v, want the four of the report", query["category"])
	}
	// The other sections were not requested, so nothing else ran.
	if result.Uptime != nil || result.Certificate != nil || result.Domain != nil {
		t.Fatalf("an unrequested check ran: %+v", result)
	}
}

// TestAnalyzeMeasuresBothRunsOfABothTarget: a "both" target is measured twice
// (mobile then desktop) and each observation keeps its own strategy, which is
// what makes the two scores distinguishable.
func TestAnalyzeMeasuresBothRunsOfABothTarget(t *testing.T) {
	stub := newPSIStub(t, nil)
	service := newAnalysisServiceWithPSI(stub.server.URL)

	result, err := service.Analyze(context.Background(), AnalysisRequest{
		Type: models.MonitorTypeHTTP,
		Config: models.MonitorConfig{
			URL: "https://example.com", Method: "GET",
			LighthouseStrategy: models.LighthouseStrategyBoth,
			LighthouseInterval: "daily",
		},
		Checks: []string{AnalysisCheckPerformance},
	})
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	section := result.Performance
	if section == nil || section.Status != analysisStatusOK || len(section.Runs) != 2 {
		t.Fatalf("performance = %+v, want both runs", section)
	}
	if section.Runs[0].Strategy != models.LighthouseStrategyMobile ||
		section.Runs[1].Strategy != models.LighthouseStrategyDesktop {
		t.Fatalf("runs = %q %q, want mobile then desktop",
			section.Runs[0].Strategy, section.Runs[1].Strategy)
	}
	if got := stub.strategies(); strings.Join(got, ",") != "mobile,desktop" {
		t.Fatalf("calls = %v, want one per run in order", got)
	}

	// A refused run does not lose the one that answered: the section is an
	// answer that mentions what it could not measure.
	partial := newPSIStub(t, func(strategy string) bool { return strategy == "desktop" })
	service = newAnalysisServiceWithPSI(partial.server.URL)
	mixed, err := service.Analyze(context.Background(), AnalysisRequest{
		Type: models.MonitorTypeHTTP,
		Config: models.MonitorConfig{
			URL: "https://example.com", Method: "GET",
			LighthouseStrategy: models.LighthouseStrategyBoth,
			LighthouseInterval: "daily",
		},
		Checks: []string{AnalysisCheckPerformance},
	})
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if mixed.Performance.Status != analysisStatusOK || len(mixed.Performance.Runs) != 1 {
		t.Fatalf("performance = %+v, want the mobile run", mixed.Performance)
	}
	if !strings.HasPrefix(mixed.Performance.Error, "desktop: ") ||
		!strings.Contains(mixed.Performance.Error, "Quota exceeded") {
		t.Fatalf("performance error = %q, want the refused run", mixed.Performance.Error)
	}
}

// TestAnalyzeReportsARefusedMeasurementInThePerformanceSection: when every run is
// refused the section is an error that carries the API's own message, and the
// request is still a success (the page exists, the quota ran out).
func TestAnalyzeReportsARefusedMeasurementInThePerformanceSection(t *testing.T) {
	stub := newPSIStub(t, func(string) bool { return true })
	service := newAnalysisServiceWithPSI(stub.server.URL)

	result, err := service.Analyze(context.Background(), AnalysisRequest{
		Type:   models.MonitorTypeHTTP,
		Config: models.MonitorConfig{URL: "https://example.com", Method: "GET"},
		Checks: []string{AnalysisCheckPerformance},
	})
	if err != nil {
		t.Fatalf("a refused measurement is an observation, not an error: %v", err)
	}
	section := result.Performance
	if section == nil || section.Status != analysisStatusError {
		t.Fatalf("performance = %+v, want an error section", section)
	}
	if len(section.Runs) != 0 {
		t.Fatalf("runs = %+v, want none", section.Runs)
	}
	if !strings.HasPrefix(section.Error, "mobile: ") || !strings.Contains(section.Error, "Quota exceeded") {
		t.Fatalf("performance error = %q", section.Error)
	}
}

// TestAnalyzeWithoutThePerformanceService: a deployment that never wired the web
// performance job answers with a section that says so instead of panicking.
func TestAnalyzeWithoutThePerformanceService(t *testing.T) {
	result, err := newAnalysisService().Analyze(context.Background(), AnalysisRequest{
		Type:   models.MonitorTypeHTTP,
		Config: models.MonitorConfig{URL: "https://example.com", Method: "GET"},
		Checks: []string{AnalysisCheckPerformance},
	})
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if result.Performance == nil || result.Performance.Status != analysisStatusError {
		t.Fatalf("performance = %+v, want an error section", result.Performance)
	}
	if result.Performance.Error != "the web performance service is not wired" {
		t.Fatalf("performance error = %q", result.Performance.Error)
	}
}
