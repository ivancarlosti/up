package checkers

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/ivancarlosti/up/internal/models"
	"github.com/ivancarlosti/up/internal/utils"
)

// maxBodyBytes caps how much of the response body is read (and searched by the
// keyword checker) to keep memory usage predictable.
const maxBodyBytes = 512 * 1024

// cacheBusterParam is the query parameter appended to every request of a
// monitor with cache_buster enabled. The name is the same one Uptime Kuma
// uses, so the behaviour is recognisable to operators coming from it and a
// cache/proxy rule already matching the parameter keeps working.
const cacheBusterParam = "uptime_kuma_cachebuster"

// cacheBusterValue returns a fresh random value for the cache buster
// parameter. The entropy source is the shared one (crypto/rand); when it is
// unavailable the clock is used instead, because a cache buster must never be
// the reason a probe is skipped.
func cacheBusterValue() string {
	if value, err := utils.RandomHex(8); err == nil {
		return value
	}
	return strconv.FormatInt(time.Now().UnixNano(), 36)
}

// checkHTTP performs the HTTP(s) probe and returns the raw (not inverted)
// result together with the response body, which the keyword checker reuses.
func checkHTTP(ctx context.Context, monitor *models.Monitor) Result {
	result, _ := httpRequest(ctx, monitor)
	return result
}

// httpRequest executes the request described by the monitor configuration.
func httpRequest(ctx context.Context, monitor *models.Monitor) (Result, string) {
	cfg := monitor.Config

	ranges, err := models.ParseStatusRanges(cfg.AcceptedStatusCodes)
	if err != nil {
		return Result{Status: models.StatusDown, Message: err.Error()}, ""
	}

	body, contentType, err := encodeBody(cfg)
	if err != nil {
		return Result{Status: models.StatusDown, Message: err.Error()}, ""
	}

	req, err := http.NewRequestWithContext(ctx, cfg.Method, cfg.URL, body)
	if err != nil {
		return Result{Status: models.StatusDown, Message: "invalid request: " + err.Error()}, ""
	}
	// The cache buster is appended through url.Values so a query string the
	// operator typed in the URL is preserved next to it.
	if cfg.CacheBuster {
		query := req.URL.Query()
		query.Set(cacheBusterParam, cacheBusterValue())
		req.URL.RawQuery = query.Encode()
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	req.Header.Set("User-Agent", "Up-Uptime-Monitor/1.0")
	for key, value := range cfg.HeaderMap() {
		req.Header.Set(key, value)
	}
	switch cfg.AuthType {
	case "basic":
		req.SetBasicAuth(cfg.BasicUser, cfg.BasicPass)
	case "bearer":
		req.Header.Set("Authorization", "Bearer "+cfg.BearerToken)
	}

	maxRedirects := cfg.MaxRedirects
	if maxRedirects < 0 {
		maxRedirects = 10
	}
	client := &http.Client{
		Timeout: timeoutFromContext(ctx),
		Transport: &http.Transport{
			Proxy: http.ProxyFromEnvironment,
			DialContext: (&net.Dialer{
				Timeout:   timeoutFromContext(ctx),
				KeepAlive: 30 * time.Second,
			}).DialContext,
			TLSClientConfig:       &tls.Config{InsecureSkipVerify: cfg.IgnoreTLS}, //nolint:gosec // explicit operator opt-in
			TLSHandshakeTimeout:   timeoutFromContext(ctx),
			ResponseHeaderTimeout: timeoutFromContext(ctx),
			ForceAttemptHTTP2:     true,
		},
		CheckRedirect: func(_ *http.Request, via []*http.Request) error {
			if len(via) >= maxRedirects {
				return http.ErrUseLastResponse
			}
			return nil
		},
	}

	started := time.Now()
	resp, err := client.Do(req)
	latency := time.Since(started).Milliseconds()
	if err != nil {
		result := Result{
			Status:    models.StatusDown,
			LatencyMS: latency,
			Message:   describeRequestError(err),
		}
		// A TLS failure still tells us which certificate the target presented
		// (expired, self signed, wrong name): the certificate watcher must be
		// able to report it instead of staying blind.
		if monitor.CertWatch && monitor.Type.SupportsCertificate() {
			result.Certificate = certificateFromError(err, time.Now().UTC())
		}
		return result, ""
	}
	defer func() { _ = resp.Body.Close() }()

	var certificate *models.CertificateInfo
	if monitor.CertWatch && monitor.Type.SupportsCertificate() {
		certificate = captureCertificate(resp.TLS, time.Now().UTC())
	}

	raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	bodyText := string(raw)

	if !models.StatusAccepted(ranges, resp.StatusCode) {
		return Result{
			Status:      models.StatusDown,
			LatencyMS:   latency,
			StatusCode:  resp.StatusCode,
			Message:     fmt.Sprintf("%d %s (accepted: %s)", resp.StatusCode, http.StatusText(resp.StatusCode), cfg.AcceptedStatusCodes),
			Certificate: certificate,
		}, bodyText
	}

	return Result{
		Status:      models.StatusUp,
		LatencyMS:   latency,
		StatusCode:  resp.StatusCode,
		Message:     fmt.Sprintf("%d %s", resp.StatusCode, http.StatusText(resp.StatusCode)),
		Certificate: certificate,
	}, bodyText
}

// checkKeyword runs an HTTP probe and validates the presence of the keyword.
func checkKeyword(ctx context.Context, monitor *models.Monitor) Result {
	cfg := monitor.Config
	result, body := httpRequest(ctx, monitor)

	// A failed request can never match a keyword.
	if result.Status != models.StatusUp {
		return result
	}

	haystack := body
	needle := cfg.Keyword
	if !cfg.CaseSensitive {
		haystack = strings.ToLower(haystack)
		needle = strings.ToLower(needle)
	}
	found := strings.Contains(haystack, needle)

	// Invert flips the expectation: the keyword must NOT be present.
	if cfg.InvertKeyword {
		found = !found
	}

	if !found {
		mode := "keyword not found"
		if cfg.InvertKeyword {
			mode = "keyword found but invert is enabled"
		}
		result.Status = models.StatusDown
		result.Message = fmt.Sprintf("%s: %q (%s)", mode, cfg.Keyword, result.Message)
		return result
	}
	result.Message = fmt.Sprintf("keyword %q matched (%s)", cfg.Keyword, result.Message)
	return result
}

// encodeBody renders the request body according to the configured encoding.
func encodeBody(cfg models.MonitorConfig) (io.Reader, string, error) {
	if strings.TrimSpace(cfg.Body) == "" {
		return nil, "", nil
	}
	switch cfg.Encoding {
	case "form":
		values, err := url.ParseQuery(strings.TrimSpace(cfg.Body))
		if err != nil {
			// Fall back to sending the payload verbatim.
			return strings.NewReader(cfg.Body), "application/x-www-form-urlencoded", nil
		}
		return strings.NewReader(values.Encode()), "application/x-www-form-urlencoded", nil
	case "xml":
		return strings.NewReader(cfg.Body), "application/xml", nil
	case "raw":
		return strings.NewReader(cfg.Body), "text/plain; charset=utf-8", nil
	default: // json
		return strings.NewReader(cfg.Body), "application/json", nil
	}
}

// describeRequestError shortens the network errors into a readable message.
func describeRequestError(err error) string {
	message := err.Error()
	switch {
	case strings.Contains(message, "no such host"):
		return "DNS resolution failed"
	case strings.Contains(message, "connection refused"):
		return "connection refused"
	case strings.Contains(message, "context deadline exceeded"), strings.Contains(message, "Client.Timeout"):
		return "timeout exceeded"
	case strings.Contains(message, "certificate"), strings.Contains(message, "x509"):
		return "TLS certificate error: " + message
	}
	return message
}
