package checkers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/gorilla/websocket"

	"github.com/ivancarlosti/up/internal/models"
)

// websocketEchoServer upgrades every request and echoes the frames it receives.
func websocketEchoServer(t *testing.T, inspect func(*http.Request)) *httptest.Server {
	t.Helper()
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if inspect != nil {
			inspect(r)
		}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		for {
			messageType, payload, err := conn.ReadMessage()
			if err != nil {
				return
			}
			if err := conn.WriteMessage(messageType, payload); err != nil {
				return
			}
		}
	}))
	t.Cleanup(server.Close)
	return server
}

// wsTarget turns the httptest http:// URL into the ws:// one a monitor needs.
func wsTarget(server *httptest.Server) string {
	return "ws://" + strings.TrimPrefix(server.URL, "http://")
}

func websocketMonitor(target string, extra func(*models.MonitorConfig)) *models.Monitor {
	config := models.MonitorConfig{URL: target}
	if extra != nil {
		extra(&config)
	}
	monitor := &models.Monitor{Name: "ws", Type: models.MonitorTypeWebSocket, TimeoutSeconds: 5, Config: config}
	monitor.Config.Normalize(monitor.Type)
	return monitor
}

func TestCheckWebSocket(t *testing.T) {
	server := websocketEchoServer(t, nil)
	base := wsTarget(server)

	t.Run("the upgrade alone is up", func(t *testing.T) {
		result := Check(context.Background(), websocketMonitor(base+"/socket", nil), "")
		if result.Status != models.StatusUp {
			t.Fatalf("status = %v, message = %s", result.Status, result.Message)
		}
		if !strings.Contains(result.Message, "upgrade ok") {
			t.Fatalf("message = %q", result.Message)
		}
	})

	t.Run("send and expect are matched", func(t *testing.T) {
		monitor := websocketMonitor(base+"/socket", func(c *models.MonitorConfig) {
			c.Send = `{"op":"ping","id":7}`
			c.Expect = `"op":"ping"` // the echo server returns what it received
		})
		result := Check(context.Background(), monitor, "")
		if result.Status != models.StatusUp {
			t.Fatalf("status = %v, message = %s", result.Status, result.Message)
		}
		if !strings.Contains(result.Message, "matched") {
			t.Fatalf("message = %q", result.Message)
		}
	})

	t.Run("an unmet expectation is down", func(t *testing.T) {
		// The echo never answers the expected payload, so the probe fails on the
		// read deadline; a short timeout keeps the test quick.
		monitor := websocketMonitor(base+"/socket", func(c *models.MonitorConfig) {
			c.Send = "ping"
			c.Expect = "pong"
		})
		monitor.TimeoutSeconds = 1
		result := Check(context.Background(), monitor, "")
		if result.Status != models.StatusDown {
			t.Fatalf("status = %v, message = %s", result.Status, result.Message)
		}
		if !strings.Contains(result.Message, "upgrade ok") {
			t.Fatalf("the message must say the upgrade succeeded: %q", result.Message)
		}
	})

	t.Run("a refused handshake is down", func(t *testing.T) {
		plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNotFound)
		}))
		defer plain.Close()

		result := Check(context.Background(), websocketMonitor(wsTarget(plain), nil), "")
		if result.Status != models.StatusDown {
			t.Fatalf("status = %v", result.Status)
		}
		if result.StatusCode != http.StatusNotFound {
			t.Fatalf("status code = %d", result.StatusCode)
		}
		if !strings.Contains(result.Message, "websocket upgrade failed") {
			t.Fatalf("message = %q", result.Message)
		}
	})

	t.Run("an unreachable target is down", func(t *testing.T) {
		result := Check(context.Background(), websocketMonitor("ws://127.0.0.1:1/socket", nil), "")
		if result.Status != models.StatusDown {
			t.Fatalf("status = %v", result.Status)
		}
	})

	t.Run("another scheme is rejected", func(t *testing.T) {
		result := Check(context.Background(), websocketMonitor("https://example.com/socket", nil), "")
		if result.Status != models.StatusDown {
			t.Fatalf("status = %v", result.Status)
		}
		if !strings.Contains(result.Message, "ws://") {
			t.Fatalf("message = %q", result.Message)
		}
	})
}

// TestCheckWebSocketHeaders covers the handshake request: the upgrade is an
// ordinary GET, so the HTTP options of the monitor have to reach it.
func TestCheckWebSocketHeaders(t *testing.T) {
	var (
		mu   sync.Mutex
		seen http.Header
	)
	server := websocketEchoServer(t, func(r *http.Request) {
		mu.Lock()
		seen = r.Header.Clone()
		mu.Unlock()
	})
	last := func() http.Header {
		mu.Lock()
		defer mu.Unlock()
		return seen
	}

	t.Run("bearer and custom headers reach the handshake", func(t *testing.T) {
		monitor := websocketMonitor(wsTarget(server), func(c *models.MonitorConfig) {
			c.AuthType = "bearer"
			c.BearerToken = "secret"
			c.Headers = []models.Header{{Key: "X-Api-Key", Value: "k-1"}}
		})
		if result := Check(context.Background(), monitor, ""); result.Status != models.StatusUp {
			t.Fatalf("status = %v, message = %s", result.Status, result.Message)
		}
		if got := last().Get("Authorization"); got != "Bearer secret" {
			t.Errorf("authorization = %q", got)
		}
		if got := last().Get("X-Api-Key"); got != "k-1" {
			t.Errorf("x-api-key = %q", got)
		}
		if got := last().Get("User-Agent"); got != probeUserAgent {
			t.Errorf("user agent = %q", got)
		}
	})

	t.Run("basic authentication is encoded", func(t *testing.T) {
		monitor := websocketMonitor(wsTarget(server), func(c *models.MonitorConfig) {
			c.AuthType = "basic"
			c.BasicUser = "user"
			c.BasicPass = "pass"
		})
		if result := Check(context.Background(), monitor, ""); result.Status != models.StatusUp {
			t.Fatalf("status = %v, message = %s", result.Status, result.Message)
		}
		// base64("user:pass").
		if got := last().Get("Authorization"); got != "Basic dXNlcjpwYXNz" {
			t.Errorf("authorization = %q", got)
		}
	})
}

// TestCheckWebSocketReadsLaterFrames covers the expectation against a chatty
// endpoint: the frames of an answer are searched one by one.
func TestCheckWebSocketReadsLaterFrames(t *testing.T) {
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		// A chatty endpoint greets the client before answering.
		if err := conn.WriteMessage(websocket.TextMessage, []byte("welcome")); err != nil {
			return
		}
		for {
			_, payload, err := conn.ReadMessage()
			if err != nil {
				return
			}
			if err := conn.WriteMessage(websocket.TextMessage, []byte("answer:"+string(payload))); err != nil {
				return
			}
		}
	}))
	defer server.Close()

	t.Run("the greeting is skipped", func(t *testing.T) {
		monitor := websocketMonitor(wsTarget(server), func(c *models.MonitorConfig) {
			c.Send = "ping"
			c.Expect = "answer:ping"
		})
		if result := Check(context.Background(), monitor, ""); result.Status != models.StatusUp {
			t.Fatalf("status = %v, message = %s", result.Status, result.Message)
		}
	})

	t.Run("expect without send", func(t *testing.T) {
		monitor := websocketMonitor(wsTarget(server), func(c *models.MonitorConfig) { c.Expect = "welcome" })
		if result := Check(context.Background(), monitor, ""); result.Status != models.StatusUp {
			t.Fatalf("status = %v, message = %s", result.Status, result.Message)
		}
	})
}

// TestCheckWebSocketCertificate covers wss://: the upgrade completes a TLS
// handshake, so a watched monitor gets its certificate exactly like the https
// probes do.
func TestCheckWebSocketCertificate(t *testing.T) {
	upgrader := websocket.Upgrader{}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}))
	defer server.Close()
	target := "wss://" + strings.TrimPrefix(server.URL, "https://")

	t.Run("a watched wss target is captured", func(t *testing.T) {
		// The self signed test certificate is trusted on purpose: the capture is
		// what this test covers.
		monitor := websocketMonitor(target, func(c *models.MonitorConfig) { c.IgnoreTLS = true })
		monitor.CertWatch = true

		result := Check(context.Background(), monitor, "")
		if result.Status != models.StatusUp {
			t.Fatalf("status = %v, message = %s", result.Status, result.Message)
		}
		if result.Certificate == nil {
			t.Fatal("the certificate was not captured")
		}
		if result.Certificate.DaysLeft <= 0 {
			t.Fatalf("days_left = %d", result.Certificate.DaysLeft)
		}
	})

	t.Run("a refused handshake still carries the certificate", func(t *testing.T) {
		// The chain is not trusted, so the probe is down; the certificate watcher
		// must still be able to report the leaf the target presented.
		monitor := websocketMonitor(target, nil)
		monitor.CertWatch = true

		result := Check(context.Background(), monitor, "")
		if result.Status != models.StatusDown {
			t.Fatalf("status = %v", result.Status)
		}
		if result.Certificate == nil {
			t.Fatal("a refused handshake must still carry the certificate")
		}
	})

	t.Run("a monitor that does not watch captures nothing", func(t *testing.T) {
		monitor := websocketMonitor(target, func(c *models.MonitorConfig) { c.IgnoreTLS = true })
		result := Check(context.Background(), monitor, "")
		if result.Status != models.StatusUp {
			t.Fatalf("status = %v, message = %s", result.Status, result.Message)
		}
		if result.Certificate != nil {
			t.Fatalf("certificate = %+v", result.Certificate)
		}
	})

	t.Run("a plain ws target has no certificate", func(t *testing.T) {
		echo := websocketEchoServer(t, nil)
		monitor := websocketMonitor(wsTarget(echo), nil)
		monitor.CertWatch = true

		result := Check(context.Background(), monitor, "")
		if result.Status != models.StatusUp {
			t.Fatalf("status = %v, message = %s", result.Status, result.Message)
		}
		if result.Certificate != nil {
			t.Fatalf("a ws:// target speaks no TLS: %+v", result.Certificate)
		}
	})
}
