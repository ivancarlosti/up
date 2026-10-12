package handlers

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/ivancarlosti/up/internal/api"
	"github.com/ivancarlosti/up/internal/config"
	"github.com/ivancarlosti/up/internal/i18n"
	"github.com/ivancarlosti/up/internal/models"
	"github.com/ivancarlosti/up/internal/services"
)

// analyzeTestLogger discards the handler logs: the tests assert the response.
func analyzeTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// newAnalyzeEngine mounts POST /api/v1/analyze with the given service.
//
// The token middleware of the real router is left out on purpose: this file
// asserts the handler, `TestAnalyzeRouteIsRegistered` below asserts the wiring.
func newAnalyzeEngine(analysis *services.AnalysisService) *gin.Engine {
	gin.SetMode(gin.TestMode)
	container := &Container{Cfg: &config.Config{}, Log: analyzeTestLogger(), Analysis: analysis}
	engine := gin.New()
	engine.POST("/api/v1/analyze", container.apiAnalyze)
	return engine
}

// newAnalyzeService builds the real service with no optional collaborator: the
// http cases of this file only need the uptime probe.
func newAnalyzeService() *services.AnalysisService {
	return services.NewAnalysisService(&config.Config{}, analyzeTestLogger(), nil, nil)
}

// postAnalyze sends a body to the endpoint under test.
func postAnalyze(engine *gin.Engine, body string) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/analyze", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	engine.ServeHTTP(recorder, request)
	return recorder
}

// decodeAPIError reads the error contract of a failed response.
func decodeAPIError(t *testing.T, recorder *httptest.ResponseRecorder) api.ErrorBody {
	t.Helper()
	var body api.ErrorBody
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("could not decode the error body %q: %v", recorder.Body.String(), err)
	}
	return body
}

// TestAnalyzeRouteIsRegistered makes sure the endpoint is mounted where the
// documentation says it is: a body-less POST on the public API, behind the token
// middleware (a nil token store is fine here, the request never gets that far).
func TestAnalyzeRouteIsRegistered(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	container := &Container{Cfg: &config.Config{}, Log: analyzeTestLogger()}

	defer func() {
		if recovered := recover(); recovered != nil {
			t.Fatalf("registering the routes panicked: %v", recovered)
		}
	}()
	container.Register(engine)

	for _, route := range engine.Routes() {
		if route.Method == http.MethodPost && route.Path == "/api/v1/analyze" {
			return
		}
	}
	t.Fatal("POST /api/v1/analyze is not registered")
}

// TestAnalyzeHandlerRunsTheProbesOfTheBody is the wire contract: the body is the
// flat monitor configuration, and the answer carries one section per probe that
// ran and nothing else.
func TestAnalyzeHandlerRunsTheProbesOfTheBody(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte("ok"))
	}))
	defer upstream.Close()

	engine := newAnalyzeEngine(newAnalyzeService())
	recorder := postAnalyze(engine, `{"type":"http","url":"`+upstream.URL+`/health","method":"GET"}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d (%s), want 200", recorder.Code, recorder.Body.String())
	}

	// The sections that were not asked for are ABSENT from the JSON: that is
	// what lets a client tell "not measured" from "measured and empty".
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(recorder.Body.Bytes(), &raw); err != nil {
		t.Fatalf("could not decode the answer: %v", err)
	}
	for _, absent := range []string{"certificate", "domain", "performance"} {
		if _, ok := raw[absent]; ok {
			t.Fatalf("the answer carries %q: %s", absent, recorder.Body.String())
		}
	}

	var result services.AnalysisResult
	if err := json.Unmarshal(recorder.Body.Bytes(), &result); err != nil {
		t.Fatalf("could not decode the answer: %v", err)
	}
	if result.Target != upstream.URL+"/health" || result.Type != models.MonitorTypeHTTP {
		t.Fatalf("target = %q %q", result.Target, result.Type)
	}
	if strings.Join(result.Checks, ",") != services.AnalysisCheckUptime {
		t.Fatalf("checks = %v, want the default list", result.Checks)
	}
	if result.Uptime == nil || result.Uptime.Status != models.StatusUp.String() {
		t.Fatalf("uptime = %+v, want up", result.Uptime)
	}
	if result.Uptime.StatusCode != http.StatusOK || result.Uptime.Attempts != 1 {
		t.Fatalf("uptime = %+v", result.Uptime)
	}
	if result.GeneratedAt.IsZero() {
		t.Fatal("generated_at must be filled in")
	}
	if result.TimingsMS.Total < 0 {
		t.Fatalf("timings = %+v", result.TimingsMS)
	}
}

// TestAnalyzeHandlerRefusesAMalformedBody: a body gin cannot parse never
// reaches the service.
func TestAnalyzeHandlerRefusesAMalformedBody(t *testing.T) {
	engine := newAnalyzeEngine(newAnalyzeService())
	recorder := postAnalyze(engine, `{"type":"http",`)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", recorder.Code)
	}
	body := decodeAPIError(t, recorder)
	if body.Code != i18n.CodeInvalidPayload {
		t.Fatalf("code = %q, want %q", body.Code, i18n.CodeInvalidPayload)
	}
	if !strings.Contains(body.Message, "invalid request body") {
		t.Fatalf("message = %q", body.Message)
	}
}

// TestAnalyzeHandlerReportsAValidationRefusal: the codes of the monitor write
// path come through untouched, so a client that already handles them needs no new
// branch to handle the refusals of this endpoint.
func TestAnalyzeHandlerReportsAValidationRefusal(t *testing.T) {
	engine := newAnalyzeEngine(newAnalyzeService())
	cases := []struct{ name, body, code string }{
		{"an unknown check", `{"type":"tcp","host":"example.com","port":443,"checks":["latency"]}`, i18n.CodeValidation},
		{"an unknown type", `{"type":"icmp"}`, i18n.CodeMonitorTypeInvalid},
		{"a certificate of a tcp target", `{"type":"tcp","host":"example.com","port":443,"checks":["certificate"]}`, i18n.CodeMonitorCert},
		{"a target without a url", `{"type":"http"}`, i18n.CodeMonitorConfig},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			recorder := postAnalyze(engine, testCase.body)
			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("status = %d (%s), want 400", recorder.Code, recorder.Body.String())
			}
			body := decodeAPIError(t, recorder)
			if body.Code != testCase.code {
				t.Fatalf("code = %q, want %q (%s)", body.Code, testCase.code, body.Message)
			}
			if body.Message == "" {
				t.Fatal("a refusal must say why")
			}
		})
	}
}

// TestAnalyzeHandlerWithoutTheService: a process that did not wire the endpoint
// answers 503 instead of dereferencing a nil service.
func TestAnalyzeHandlerWithoutTheService(t *testing.T) {
	engine := newAnalyzeEngine(nil)
	recorder := postAnalyze(engine, `{"type":"http","url":"https://example.com"}`)
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", recorder.Code)
	}
	body := decodeAPIError(t, recorder)
	if body.Code != i18n.CodeInternal || body.Message != "the analysis endpoint is not wired" {
		t.Fatalf("error = %+v", body)
	}
}
