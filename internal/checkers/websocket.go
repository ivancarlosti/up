package checkers

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gorilla/websocket"

	"github.com/ivancarlosti/up/internal/models"
)

// checkWebSocket opens a WebSocket connection (the RFC 6455 upgrade over ws://
// or wss://) and, when the configuration asks for it, exchanges one frame with
// the target.
//
// The upgrade is the probe: a target that refuses the handshake (a 404 on the
// path, a stripped Upgrade header, an invalid Sec-WebSocket-Accept) is down,
// exactly what a browser client would see. A wss:// target exposes its leaf
// certificate the way the https probes do, so `cert_watch` costs nothing extra.
//
// config.send is written as a single text frame and config.expect, when set, is
// searched in the frames read until the timeout: the pair reuses the TCP monitor
// fields, so the two probes document the same options.
func checkWebSocket(ctx context.Context, monitor *models.Monitor, plan probePlan) Result {
	cfg := monitor.Config
	rawURL := strings.TrimSpace(cfg.URL)
	if rawURL == "" {
		return Result{Status: models.StatusDown, Message: "the url is required"}
	}
	target, err := url.Parse(rawURL)
	if err != nil {
		return Result{Status: models.StatusDown, Message: "invalid url: " + err.Error()}
	}
	if !strings.EqualFold(target.Scheme, "ws") && !strings.EqualFold(target.Scheme, "wss") {
		return Result{Status: models.StatusDown, Message: "the url must start with ws:// or wss://"}
	}

	timeout := timeoutFromContext(ctx)
	dialer := websocket.Dialer{
		HandshakeTimeout: timeout,
		TLSClientConfig:  &tls.Config{InsecureSkipVerify: cfg.IgnoreTLS}, //nolint:gosec // explicit operator opt-in
		// The plan is bound to the dialer, so the connection follows the address
		// family of this execution while the SNI and the Host header stay the
		// ones of the URL (see probeTarget).
		NetDialContext: func(dialCtx context.Context, _, address string) (net.Conn, error) {
			return dialProbe(dialCtx, plan, address, timeoutFromContext(dialCtx))
		},
	}

	started := time.Now()
	conn, resp, err := dialer.DialContext(ctx, rawURL, upgradeHeaders(cfg))
	latency := time.Since(started).Milliseconds()
	now := time.Now().UTC()
	if err != nil {
		return Result{
			Status:      models.StatusDown,
			LatencyMS:   latency,
			StatusCode:  responseStatus(resp),
			Message:     describeHandshakeError(err, resp),
			Certificate: watchedCertificateFromError(err, monitor, now),
		}
	}
	defer func() { _ = conn.Close() }()

	result := Result{Status: models.StatusUp, LatencyMS: latency, Message: "websocket upgrade ok"}
	if monitor.CertWatch && monitor.Type.SupportsCertificate() {
		result.Certificate = certificateFromConn(conn, now)
	}

	// A frame that does not match is a down probe: the upgrade alone would say
	// nothing about a server that accepts every connection and never answers.
	if err := exchangeFrames(conn, cfg, timeout); err != nil {
		result.Status = models.StatusDown
		result.Message = "websocket upgrade ok but " + err.Error()
		return result
	}
	if expect := strings.TrimSpace(cfg.Expect); expect != "" {
		result.Message = fmt.Sprintf("websocket upgrade ok and matched %q", expect)
	}
	return result
}

// upgradeHeaders builds the handshake request: the same user agent as the HTTP
// probe, the configured headers and the configured credentials. The WebSocket
// handshake is an ordinary GET request, so it carries them all.
func upgradeHeaders(cfg models.MonitorConfig) http.Header {
	headers := http.Header{}
	headers.Set("User-Agent", probeUserAgent)
	for key, value := range cfg.HeaderMap() {
		headers.Set(key, value)
	}
	switch cfg.AuthType {
	case "basic":
		headers.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(cfg.BasicUser+":"+cfg.BasicPass)))
	case "bearer":
		headers.Set("Authorization", "Bearer "+cfg.BearerToken)
	}
	return headers
}

// exchangeFrames writes the send frame and waits for the expected answer: it
// returns nil when the configuration asks for nothing, when the payload alone
// was sent, or when the expectation matched.
//
// The frames are searched one by one because a chatty endpoint may greet the
// client before answering, and the loop is bounded by the read deadline.
func exchangeFrames(conn *websocket.Conn, cfg models.MonitorConfig, timeout time.Duration) error {
	send := strings.TrimSpace(cfg.Send)
	expect := strings.TrimSpace(cfg.Expect)
	if send == "" && expect == "" {
		return nil
	}
	// An answer is capped like the body of the HTTP probes.
	conn.SetReadLimit(maxBodyBytes)

	if send != "" {
		if err := conn.WriteMessage(websocket.TextMessage, []byte(cfg.Send)); err != nil {
			return fmt.Errorf("sending the payload: %w", err)
		}
	}
	if expect == "" {
		return nil
	}

	if timeout > 0 {
		if err := conn.SetReadDeadline(time.Now().Add(timeout)); err != nil {
			return fmt.Errorf("could not set the read deadline")
		}
	}
	for {
		if _, payload, err := conn.ReadMessage(); err != nil {
			return fmt.Errorf("waiting for %q: %w", cfg.Expect, err)
		} else if strings.Contains(string(payload), cfg.Expect) {
			return nil
		}
	}
}

// certificateFromConn reads the leaf certificate of an upgraded wss connection.
// gorilla exposes the connection it built, so the handshake state is read on the
// tls.Conn exactly like the ssl probe reads it.
func certificateFromConn(conn *websocket.Conn, now time.Time) *models.CertificateInfo {
	tlsConn, ok := conn.UnderlyingConn().(*tls.Conn)
	if !ok {
		return nil
	}
	state := tlsConn.ConnectionState()
	return captureCertificate(&state, now)
}

// watchedCertificateFromError extracts the certificate a failed handshake
// carried, and only for a monitor that watches it: a plain ws:// target and a
// monitor without cert_watch have nothing to store.
func watchedCertificateFromError(err error, monitor *models.Monitor, now time.Time) *models.CertificateInfo {
	if !monitor.CertWatch || !monitor.Type.SupportsCertificate() {
		return nil
	}
	return certificateFromError(err, now)
}

// describeHandshakeError shortens a failed upgrade into a readable message. A
// refused handshake carries the HTTP response of the rejection, which says much
// more than the bare ErrBadHandshake gorilla returns.
func describeHandshakeError(err error, resp *http.Response) string {
	if resp != nil {
		return fmt.Sprintf("websocket upgrade failed: %s", resp.Status)
	}
	return "websocket upgrade failed: " + describeRequestError(err)
}

// responseStatus is the status code of a refused handshake, or 0 when the
// failure happened before the server answered (DNS, dial, TLS).
func responseStatus(resp *http.Response) int {
	if resp == nil {
		return 0
	}
	return resp.StatusCode
}
