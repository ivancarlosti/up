package lighthouse

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/ivancarlosti/up/internal/models"
)

// DefaultBaseURL is the PageSpeed Insights v5 endpoint Up calls.
const DefaultBaseURL = "https://www.googleapis.com/pagespeedonline/v5/runPagespeed"

// lighthouseCategories are the four categories Up asks for. The API returns a
// score for each of them (null when the page could not be audited for it).
var lighthouseCategories = []string{"performance", "accessibility", "best-practices", "seo"}

// lighthouseAudits are the Core Web Vitals read from the report, as numeric
// milliseconds (except the layout shift, which is unitless).
const (
	auditFCP         = "first-contentful-paint"
	auditLCP         = "largest-contentful-paint"
	auditTBT         = "total-blocking-time"
	auditCLS         = "cumulative-layout-shift"
	auditSpeedIndex  = "speed-index"
	maxResponseBytes = 16 << 20
	maxErrorSnippet  = 300
	minRunnerTimeout = 5 * time.Second
)

// Runner performs the PageSpeed Insights calls.
//
// The base URL is a field (not a constant used inline) so a test can point the
// runner at an httptest server: nothing in the package reaches Google unless
// the caller leaves the default in place.
type Runner struct {
	baseURL string
	client  *http.Client
	apiKey  string
}

// NewRunner builds a runner against the real PageSpeed Insights endpoint.
func NewRunner(apiKey string, timeout time.Duration) *Runner {
	return NewRunnerWithBaseURL(DefaultBaseURL, apiKey, timeout)
}

// NewRunnerWithBaseURL builds a runner against another endpoint (tests, a
// corporate proxy that republishes the API).
func NewRunnerWithBaseURL(baseURL, apiKey string, timeout time.Duration) *Runner {
	if timeout < minRunnerTimeout {
		timeout = time.Duration(models.DefaultLighthouseTimeoutSeconds) * time.Second
	}
	return &Runner{
		baseURL: strings.TrimSpace(baseURL),
		client:  &http.Client{Timeout: timeout},
		apiKey:  strings.TrimSpace(apiKey),
	}
}

// Run scores one target and returns the observation to store.
//
// A returned error means the API could not answer (unreachable, refused, an
// invalid URL, an internal Lighthouse failure): the caller stores it as a
// "status: error" observation, exactly like a certificate that could not be
// read. The result is never nil without an error.
func (r *Runner) Run(ctx context.Context, target Target) (*models.LighthouseResult, error) {
	page := strings.TrimSpace(target.URL)
	if page == "" {
		return nil, errors.New("the target has no URL")
	}
	endpoint, err := url.Parse(r.baseURL)
	if err != nil {
		return nil, fmt.Errorf("the PageSpeed Insights base URL %q is not a URL: %w", r.baseURL, err)
	}
	query := endpoint.Query()
	query.Set("url", page)
	query.Set("strategy", models.NormalizeLighthouseStrategy(target.Strategy))
	// The categories are asked for explicitly: without them the API answers with
	// the performance score alone.
	for _, category := range lighthouseCategories {
		query.Add("category", category)
	}
	if r.apiKey != "" {
		query.Set("key", r.apiKey)
	}
	endpoint.RawQuery = query.Encode()

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("building the PageSpeed Insights request: %w", err)
	}
	request.Header.Set("Accept", "application/json")

	response, err := r.client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("calling the PageSpeed Insights API: %w", err)
	}
	defer response.Body.Close()

	// The report is bounded: a Lighthouse answer is normally around a megabyte
	// of JSON, and an unexpected body must not be read into memory unbounded.
	body, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes))
	if err != nil {
		return nil, fmt.Errorf("reading the PageSpeed Insights answer: %w", err)
	}
	var payload psiResponse
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("the PageSpeed Insights API answered HTTP %d with an unreadable body: %s",
			response.StatusCode, snippet(body))
	}
	// The API reports its own failures in an `error` object, sometimes with HTTP
	// 200: its message is the useful part ("Lighthouse returned error: ...").
	if payload.Error != nil && strings.TrimSpace(payload.Error.Message) != "" {
		return nil, errors.New(trimMessage(payload.Error.Message))
	}
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("the PageSpeed Insights API answered HTTP %d: %s",
			response.StatusCode, snippet(body))
	}
	if payload.LighthouseResult == nil {
		return nil, errors.New("the PageSpeed Insights API answered without a Lighthouse report")
	}
	return payload.result(target.Strategy), nil
}

// Wire types ----------------------------------------------------------------
//
// The API answers with a full Lighthouse report (several megabytes of audits);
// only the categories and the five vitals are decoded, everything else is
// ignored by encoding/json.

type psiResponse struct {
	Error            *psiError `json:"error"`
	LighthouseResult *struct {
		RequestedURL string                 `json:"requestedUrl"`
		FinalURL     string                 `json:"finalUrl"`
		FetchTime    string                 `json:"fetchTime"`
		Categories   map[string]psiCategory `json:"categories"`
		Audits       map[string]psiAudit    `json:"audits"`
	} `json:"lighthouseResult"`
}

type psiError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Status  string `json:"status"`
}

// psiCategory is one scored category. The score is a pointer because the API
// sends null for a category it could not measure.
type psiCategory struct {
	Score *float64 `json:"score"`
}

// psiAudit is one audit; only the numeric value (milliseconds for the vitals)
// is read.
type psiAudit struct {
	NumericValue *float64 `json:"numericValue"`
}

// result converts the answer into the observation stored and displayed.
//
// The strategy travels with the TARGET, not with the answer: PageSpeed Insights
// echoes it in the report (in `configSettings`, which Up does not decode) and the
// caller identifies the stored observation by (monitor, strategy). A result
// without it is filed under the default run, so a page measured on "both" would
// see its desktop score upserted onto its mobile row and no desktop row at all.
func (p *psiResponse) result(strategy string) *models.LighthouseResult {
	report := p.LighthouseResult
	out := &models.LighthouseResult{
		Status:   models.LighthouseStatusOK,
		Strategy: models.NormalizeLighthouseStrategy(strategy),
		FinalURL: strings.TrimSpace(report.FinalURL),
	}
	if out.FinalURL == "" {
		out.FinalURL = strings.TrimSpace(report.RequestedURL)
	}
	if fetched, err := time.Parse(time.RFC3339, strings.TrimSpace(report.FetchTime)); err == nil {
		out.FetchTime = &fetched
	}
	out.Performance = scoreOf(report.Categories["performance"])
	out.Accessibility = scoreOf(report.Categories["accessibility"])
	out.BestPractices = scoreOf(report.Categories["best-practices"])
	out.SEO = scoreOf(report.Categories["seo"])

	out.FirstContentfulPaintMS = numericValue(report.Audits, auditFCP)
	out.LargestContentfulPaintMS = numericValue(report.Audits, auditLCP)
	out.TotalBlockingTimeMS = numericValue(report.Audits, auditTBT)
	out.CumulativeLayoutShift = numericValue(report.Audits, auditCLS)
	out.SpeedIndexMS = numericValue(report.Audits, auditSpeedIndex)
	return out
}

// scoreOf converts a Lighthouse score (0..1) into the 0..100 integer the UI
// shows. A missing score stays nil instead of reading as zero.
func scoreOf(category psiCategory) *int {
	if category.Score == nil {
		return nil
	}
	score := int(math.Round(*category.Score * 100))
	if score < 0 {
		score = 0
	}
	if score > 100 {
		score = 100
	}
	return &score
}

// numericValue reads the numeric value of an audit (0 when the report does not
// carry it, which is what the UI renders as "not measured").
func numericValue(audits map[string]psiAudit, id string) float64 {
	audit, ok := audits[id]
	if !ok || audit.NumericValue == nil {
		return 0
	}
	return *audit.NumericValue
}

// snippet renders the beginning of an unreadable body for the error message.
func snippet(body []byte) string {
	text := strings.TrimSpace(string(body))
	if text == "" {
		return "(empty body)"
	}
	if len(text) > maxErrorSnippet {
		text = text[:maxErrorSnippet] + "..."
	}
	// The message travels into a log line and into the database: a newline
	// would break both.
	return strings.Join(strings.Fields(text), " ")
}

// trimMessage clips what the API reported to something a log line and the
// message column can hold.
func trimMessage(message string) string {
	text := strings.Join(strings.Fields(strings.TrimSpace(message)), " ")
	if len(text) > maxErrorSnippet {
		text = text[:maxErrorSnippet] + "..."
	}
	return text
}
