package middleware

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

// gzipEngine builds an engine with the middleware and the payloads the API
// actually serves: a JSON object, a binary body, an already encoded body and a
// status without a body.
func gzipEngine() *gin.Engine {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.Use(Gzip())
	engine.GET("/json", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"monitors": []gin.H{{"id": 1, "name": "Health"}}})
	})
	engine.GET("/binary", func(c *gin.Context) {
		c.Data(http.StatusOK, "image/png", []byte{0x89, 'P', 'N', 'G'})
	})
	engine.GET("/precompressed", func(c *gin.Context) {
		c.Header("Content-Encoding", "br")
		c.Data(http.StatusOK, "application/json", []byte(`{"ok":true}`))
	})
	engine.GET("/empty", func(c *gin.Context) {
		c.Status(http.StatusNoContent)
	})
	return engine
}

// do runs one request and returns the raw response.
func do(t *testing.T, engine *gin.Engine, path string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, path, nil)
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, request)
	return recorder
}

// TestGzipCompressesJSON is the change itself: a listing is compressed for a
// client that accepts it, and it still decodes to the payload the handler wrote.
func TestGzipCompressesJSON(t *testing.T) {
	engine := gzipEngine()
	recorder := do(t, engine, "/json", map[string]string{"Accept-Encoding": "gzip, deflate, br"})

	if got := recorder.Header().Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("Content-Encoding = %q, want gzip", got)
	}
	if got := recorder.Header().Get("Vary"); !strings.Contains(got, "Accept-Encoding") {
		t.Fatalf("Vary = %q, want it to mention Accept-Encoding", got)
	}
	if got := recorder.Header().Get("Content-Length"); got != "" {
		t.Fatalf("Content-Length = %q, the compressed length is not known before the end", got)
	}
	if recorder.Body.Len() == 0 {
		t.Fatal("the response has no body")
	}
	reader, err := gzip.NewReader(bytes.NewReader(recorder.Body.Bytes()))
	if err != nil {
		t.Fatalf("the body is not gzip: %v", err)
	}
	defer reader.Close()
	payload, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("reading the compressed body: %v", err)
	}
	var decoded struct {
		Monitors []struct {
			ID   int    `json:"id"`
			Name string `json:"name"`
		} `json:"monitors"`
	}
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatalf("the decompressed body is not the JSON payload: %v (%s)", err, payload)
	}
	if len(decoded.Monitors) != 1 || decoded.Monitors[0].Name != "Health" {
		t.Fatalf("payload = %s", payload)
	}
}

// TestGzipWithoutAcceptEncoding checks the pass through: a client that does not ask
// for gzip receives exactly what the handler wrote.
func TestGzipWithoutAcceptEncoding(t *testing.T) {
	engine := gzipEngine()
	for _, headers := range []map[string]string{
		nil,
		{"Accept-Encoding": "identity"},
		{"Accept-Encoding": "gzip;q=0"},
	} {
		recorder := do(t, engine, "/json", headers)
		if got := recorder.Header().Get("Content-Encoding"); got != "" {
			t.Errorf("%v: Content-Encoding = %q, want none", headers, got)
		}
		if body := recorder.Body.String(); !strings.Contains(body, `"monitors"`) {
			t.Errorf("%v: body = %q, want the JSON payload", headers, body)
		}
	}
}

// TestGzipLeavesIncompressibleBodiesAlone covers the three bodies that must not be
// wrapped: a binary payload (the badge PNG), an already encoded one and a status
// without a body.
func TestGzipLeavesIncompressibleBodiesAlone(t *testing.T) {
	engine := gzipEngine()
	gzipHeaders := map[string]string{"Accept-Encoding": "gzip"}

	binary := do(t, engine, "/binary", gzipHeaders)
	if got := binary.Header().Get("Content-Encoding"); got != "" {
		t.Errorf("binary: Content-Encoding = %q, want none", got)
	}
	if got := binary.Body.Bytes(); !bytes.Equal(got, []byte{0x89, 'P', 'N', 'G'}) {
		t.Errorf("binary: body = %v, want the bytes the handler wrote", got)
	}

	precompressed := do(t, engine, "/precompressed", gzipHeaders)
	if got := precompressed.Header().Get("Content-Encoding"); got != "br" {
		t.Errorf("precompressed: Content-Encoding = %q, want the handler's br", got)
	}
	if body := precompressed.Body.String(); body != `{"ok":true}` {
		t.Errorf("precompressed: body = %q, want it untouched", body)
	}

	empty := do(t, engine, "/empty", gzipHeaders)
	if got := empty.Header().Get("Content-Encoding"); got != "" {
		t.Errorf("204: Content-Encoding = %q, want none", got)
	}
	if empty.Body.Len() != 0 {
		t.Errorf("204: body = %q, want none", empty.Body.String())
	}
}

// TestGzipSkipsProtocolUpgrade pins the websocket handshake: the response is a
// hijacked connection, so the writer must not be wrapped even when the browser
// advertises gzip.
func TestGzipSkipsProtocolUpgrade(t *testing.T) {
	engine := gzipEngine()
	recorder := do(t, engine, "/json", map[string]string{
		"Accept-Encoding": "gzip",
		"Upgrade":         "websocket",
		"Connection":      "Upgrade",
	})
	if got := recorder.Header().Get("Content-Encoding"); got != "" {
		t.Fatalf("Content-Encoding = %q on an upgrade request, want none", got)
	}
	if body := recorder.Body.String(); !strings.Contains(body, `"monitors"`) {
		t.Fatalf("body = %q, want the JSON payload", body)
	}
}

// TestCompressibleType pins the media types the middleware wraps.
func TestCompressibleType(t *testing.T) {
	cases := map[string]bool{
		"application/json":                      true,
		"application/json; charset=utf-8":       true,
		"application/problem+json":              true,
		"text/html; charset=utf-8":              true,
		"text/plain":                            true,
		"image/svg+xml":                         true,
		"text/event-stream":                     false,
		"image/png":                             false,
		"application/octet-stream":              false,
		"application/javascript; charset=utf-8": true,
		"":                                      false,
	}
	for contentType, want := range cases {
		if got := compressibleType(contentType); got != want {
			t.Errorf("compressibleType(%q) = %v, want %v", contentType, got, want)
		}
	}
}
