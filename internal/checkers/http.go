package checkers

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/ivancarlosti/up/internal/models"
)

// maxBodyBytes caps how much of the response body is read (and searched by the
// keyword checker) to keep memory usage predictable.
const maxBodyBytes = 512 * 1024

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
		return Result{
			Status:    models.StatusDown,
			LatencyMS: latency,
			Message:   describeRequestError(err),
		}, ""
	}
	defer func() { _ = resp.Body.Close() }()

	raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	bodyText := string(raw)

	if !models.StatusAccepted(ranges, resp.StatusCode) {
		return Result{
			Status:     models.StatusDown,
			LatencyMS:  latency,
			StatusCode: resp.StatusCode,
			Message: fmt.Sprintf("%d %s (accepted: %s)", resp.StatusCode,
				http.StatusText(resp.StatusCode), cfg.AcceptedStatusCodes),
		}, bodyText
	}

	return Result{
		Status:     models.StatusUp,
		LatencyMS:  latency,
		StatusCode: resp.StatusCode,
		Message:    fmt.Sprintf("%d %s", resp.StatusCode, http.StatusText(resp.StatusCode)),
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
