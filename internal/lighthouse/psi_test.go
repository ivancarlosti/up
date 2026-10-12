package lighthouse

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ivancarlosti/up/internal/models"
)

// reportBody is a trimmed copy of a real PageSpeed Insights answer: the four
// categories, the five vitals and nothing else.
const reportBody = `{
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

// TestRunnerRunParsesTheReport checks what Up reads from an answer: the query it
// sends, the four scores converted to 0..100 and the five vitals.
func TestRunnerRunParsesTheReport(t *testing.T) {
	var captured *http.Request
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured = r
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(reportBody))
	}))
	defer server.Close()

	runner := NewRunnerWithBaseURL(server.URL, "test-key", 0)
	result, err := runner.Run(context.Background(), Target{URL: "https://example.com/", Strategy: "mobile"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	query := captured.URL.Query()
	if query.Get("url") != "https://example.com/" || query.Get("strategy") != "mobile" {
		t.Fatalf("query = %s", captured.URL.RawQuery)
	}
	if query.Get("key") != "test-key" {
		t.Fatalf("the API key must travel as `key`: %s", captured.URL.RawQuery)
	}
	for _, category := range lighthouseCategories {
		found := false
		for _, value := range query["category"] {
			if value == category {
				found = true
			}
		}
		if !found {
			t.Fatalf("category %q missing from %s", category, captured.URL.RawQuery)
		}
	}

	if result.Status != models.LighthouseStatusOK {
		t.Fatalf("status = %q", result.Status)
	}
	if result.Strategy != models.LighthouseStrategyMobile {
		t.Fatalf("strategy = %q", result.Strategy)
	}
	if result.FinalURL != "https://www.example.com/" {
		t.Fatalf("final url = %q", result.FinalURL)
	}
	if result.FetchTime == nil || result.FetchTime.UTC().Format(time.RFC3339) != "2026-10-10T18:12:31Z" {
		t.Fatalf("fetch time = %v", result.FetchTime)
	}
	if result.Performance == nil || result.Accessibility == nil ||
		result.BestPractices == nil || result.SEO == nil {
		t.Fatalf("a score is missing: %+v", result.LighthouseMeasurements)
	}
	if *result.Performance != 62 || *result.Accessibility != 96 ||
		*result.BestPractices != 100 || *result.SEO != 90 {
		t.Fatalf("scores = %d %d %d %d", *result.Performance, *result.Accessibility,
			*result.BestPractices, *result.SEO)
	}
	if result.FirstContentfulPaintMS != 1834.512 || result.CumulativeLayoutShift != 0.012 {
		t.Fatalf("vitals = %+v", result.LighthouseMeasurements)
	}
}

// TestRunnerRunKeepsTheTargetStrategy pins the run an answer describes: the
// service identifies a stored observation by (monitor, strategy), so a page
// measured on "both" must come back once per run and never as the default one —
// it used to come back without any strategy, which filed the desktop score on
// the mobile row of the monitor.
func TestRunnerRunKeepsTheTargetStrategy(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(reportBody))
	}))
	defer server.Close()

	runner := NewRunnerWithBaseURL(server.URL, "", 0)
	for _, testCase := range []struct {
		target string
		want   string
	}{
		{target: "mobile", want: models.LighthouseStrategyMobile},
		{target: "desktop", want: models.LighthouseStrategyDesktop},
		{target: "DESKTOP", want: models.LighthouseStrategyDesktop},
		{target: "", want: models.DefaultLighthouseStrategy},
	} {
		result, err := runner.Run(context.Background(), Target{URL: "https://example.com/", Strategy: testCase.target})
		if err != nil {
			t.Fatalf("Run(%q): %v", testCase.target, err)
		}
		if result.Strategy != testCase.want {
			t.Fatalf("the strategy of %q = %q, want %q", testCase.target, result.Strategy, testCase.want)
		}
	}
}

// TestRunnerRunWithoutAPIKey checks the anonymous quota: no `key` parameter.
func TestRunnerRunWithoutAPIKey(t *testing.T) {
	var rawQuery string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rawQuery = r.URL.RawQuery
		_, _ = w.Write([]byte(reportBody))
	}))
	defer server.Close()

	if _, err := NewRunnerWithBaseURL(server.URL, "  ", 0).Run(context.Background(),
		Target{URL: "https://example.com/"}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if strings.Contains(rawQuery, "key=") {
		t.Fatalf("an empty key must not be sent: %s", rawQuery)
	}
	if !strings.Contains(rawQuery, "strategy=mobile") {
		t.Fatalf("an empty strategy must default to mobile: %s", rawQuery)
	}
}

// TestRunnerRunMissingCategory documents the null score: a category the API
// could not measure stays nil instead of reading as zero.
func TestRunnerRunMissingCategory(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"lighthouseResult":{"finalUrl":"https://example.com/",
			"categories":{"performance":{"score":0.4},"seo":{"score":null}},
			"audits":{"first-contentful-paint":{"numericValue":null}}}}`))
	}))
	defer server.Close()

	result, err := NewRunnerWithBaseURL(server.URL, "", 0).Run(context.Background(), Target{URL: "https://example.com/"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Performance == nil || *result.Performance != 40 {
		t.Fatalf("performance = %v", result.Performance)
	}
	if result.SEO != nil || result.Accessibility != nil {
		t.Fatalf("a missing score must stay nil: seo=%v accessibility=%v", result.SEO, result.Accessibility)
	}
	if result.FirstContentfulPaintMS != 0 {
		t.Fatalf("a missing vital must read as zero: %v", result.FirstContentfulPaintMS)
	}
}

// TestRunnerRunFailures checks every way the API can refuse: the error object of
// an internal Lighthouse failure, a plain HTTP error and an unreadable body.
func TestRunnerRunFailures(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   string
	}{
		{
			name:   "lighthouse internal error",
			status: http.StatusInternalServerError,
			body:   `{"error":{"code":500,"message":"Lighthouse returned error: ERRORED_DOCUMENT_REQUEST.","status":"INTERNAL"}}`,
			want:   "ERRORED_DOCUMENT_REQUEST",
		},
		{
			name:   "http error without a json body",
			status: http.StatusNotFound,
			body:   "<html>not found</html>",
			want:   "HTTP 404",
		},
		{
			name:   "unreadable body",
			status: http.StatusOK,
			body:   "not json at all",
			want:   "unreadable body",
		},
		{
			name:   "no report",
			status: http.StatusOK,
			body:   `{"kind":"pagespeedonline#result"}`,
			want:   "without a Lighthouse report",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(testCase.status)
				_, _ = w.Write([]byte(testCase.body))
			}))
			defer server.Close()

			result, err := NewRunnerWithBaseURL(server.URL, "", 0).Run(context.Background(),
				Target{URL: "https://example.com/"})
			if err == nil || result != nil {
				t.Fatalf("Run = %v, %v; want an error and no result", result, err)
			}
			if !strings.Contains(err.Error(), testCase.want) {
				t.Fatalf("error = %q, want it to mention %q", err.Error(), testCase.want)
			}
		})
	}
}

// TestRunnerRunEmptyTarget rejects a target without a page before any call.
func TestRunnerRunEmptyTarget(t *testing.T) {
	runner := NewRunnerWithBaseURL("http://127.0.0.1:1/", "", 0)
	if _, err := runner.Run(context.Background(), Target{URL: "  "}); err == nil {
		t.Fatal("an empty target must be refused")
	}
}

// TestRunnerRunHonoursTheContext checks that a cancelled parent stops the call
// instead of waiting for the client timeout.
func TestRunnerRunHonoursTheContext(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	}))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	started := time.Now()
	if _, err := NewRunnerWithBaseURL(server.URL, "", 0).Run(ctx, Target{URL: "https://example.com/"}); err == nil {
		t.Fatal("a cancelled context must fail the run")
	}
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Fatalf("the context was not honoured (%s)", elapsed)
	}
}
