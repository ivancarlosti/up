package middleware

import (
	"compress/gzip"
	"io"
	"net/http"
	"strings"
	"sync"

	"github.com/gin-gonic/gin"
)

// gzipPool recycles the compressors: they hold a deflate window of 32 KiB that
// would otherwise be allocated for every request.
var gzipPool = sync.Pool{
	New: func() any {
		// BestSpeed is the right trade here: the payload is JSON, which compresses
		// to ~10 % of its size at this level, and the request is on the critical
		// path of a page the operator is waiting for.
		writer, err := gzip.NewWriterLevel(io.Discard, gzip.BestSpeed)
		if err != nil {
			return gzip.NewWriter(io.Discard)
		}
		return writer
	},
}

// Gzip compresses the response of the API when the client accepts it.
//
// The dashboard payload of a 1 000 monitor installation is megabytes of JSON -
// heartbeat bars, uptimes, statuses, the summary - and it used to leave the server
// uncompressed: negligible over a container network, most of the time the operator
// waited over anything slower than a LAN. The same payload arrives at ~10 % of its
// size for a few milliseconds of CPU.
//
// Only bodies that compress are wrapped: JSON, text, the embedded single page
// application (its assets are hashed and cached by the browser, so the compressor
// runs once per asset per client) and the SVG badge. The websocket handshake is
// never wrapped, because its response is a hijacked connection rather than a body.
func Gzip() gin.HandlerFunc {
	return func(c *gin.Context) {
		if !acceptsGzip(c.Request) {
			c.Next()
			return
		}
		writer := &gzipResponseWriter{ResponseWriter: c.Writer}
		c.Writer = writer
		// Deferred, so a panic that unwinds through this middleware still writes
		// the gzip trailer: a truncated stream is a broken response.
		defer writer.close()
		c.Next()
	}
}

// gzipResponseWriter compresses what the handler writes when the response is one
// of the compressible types and passes everything else through untouched. The
// decision is taken once, when the headers are known and before they are sent.
type gzipResponseWriter struct {
	gin.ResponseWriter
	writer   *gzip.Writer
	decided  bool
	compress bool
}

// WriteHeader records the status. It deliberately does not decide anything: gin
// calls it (through Context.Status) before the renderer writes the Content-Type,
// and nothing is sent yet. The decision is taken in WriteHeaderNow, which is the
// last point before the headers leave (and the first one that always knows the
// type).
func (w *gzipResponseWriter) WriteHeader(code int) {
	w.ResponseWriter.WriteHeader(code)
}

func (w *gzipResponseWriter) WriteHeaderNow() {
	w.decide(w.ResponseWriter.Status())
	w.ResponseWriter.WriteHeaderNow()
}

func (w *gzipResponseWriter) Write(data []byte) (int, error) {
	if !w.decide(w.ResponseWriter.Status()) {
		return w.ResponseWriter.Write(data)
	}
	return w.writer.Write(data)
}

func (w *gzipResponseWriter) WriteString(text string) (int, error) {
	if !w.decide(w.ResponseWriter.Status()) {
		return w.ResponseWriter.WriteString(text)
	}
	return w.writer.Write([]byte(text))
}

// Flush pushes what has been compressed so far, so a handler that streams keeps
// streaming instead of buffering the whole response. It decides first: flushing
// sends the headers, after which compressing the body would be impossible.
func (w *gzipResponseWriter) Flush() {
	w.decide(w.ResponseWriter.Status())
	if w.writer != nil {
		_ = w.writer.Flush()
	}
	w.ResponseWriter.Flush()
}

// decide inspects the headers once and reports whether the body is compressed.
func (w *gzipResponseWriter) decide(status int) bool {
	if w.decided {
		return w.compress
	}
	w.decided = true
	header := w.ResponseWriter.Header()
	// A body that is already encoded, a partial response (compressing it would
	// break the byte ranges the client asked for), a status that carries no body
	// and a type that does not compress are all passed through.
	w.compress = header.Get("Content-Encoding") == "" &&
		header.Get("Content-Range") == "" &&
		status != http.StatusNoContent && status != http.StatusNotModified &&
		compressibleType(header.Get("Content-Type"))
	if !w.compress {
		return false
	}
	header.Set("Content-Encoding", "gzip")
	header.Add("Vary", "Accept-Encoding")
	// The length of the uncompressed body is wrong now, and the compressed one is
	// only known at the end: the response is sent chunked.
	header.Del("Content-Length")
	w.writer = gzipPool.Get().(*gzip.Writer)
	w.writer.Reset(w.ResponseWriter)
	return true
}

// close writes the gzip trailer and returns the compressor to the pool.
func (w *gzipResponseWriter) close() {
	if w.writer == nil {
		return
	}
	_ = w.writer.Close()
	w.writer.Reset(io.Discard)
	gzipPool.Put(w.writer)
	w.writer = nil
}

// acceptsGzip reports whether the client accepts a gzip encoded response.
func acceptsGzip(r *http.Request) bool {
	// A protocol upgrade (the /api/ws handshake) has no body to compress, and the
	// connection is handed to the websocket library once the headers are written.
	if r.Header.Get("Upgrade") != "" {
		return false
	}
	for _, part := range strings.Split(r.Header.Get("Accept-Encoding"), ",") {
		encoding, params, _ := strings.Cut(part, ";")
		switch strings.ToLower(strings.TrimSpace(encoding)) {
		case "gzip", "*":
			// A q of zero forbids the encoding even though it is listed.
			if strings.ReplaceAll(strings.ToLower(params), " ", "") == "q=0" {
				return false
			}
			return true
		}
	}
	return false
}

// compressibleType reports whether a Content-Type is worth compressing.
func compressibleType(contentType string) bool {
	media, _, _ := strings.Cut(contentType, ";")
	media = strings.ToLower(strings.TrimSpace(media))
	switch {
	case media == "text/event-stream":
		// An event stream is written one event at a time: compressing it would
		// hide the events in the window of the compressor.
		return false
	case strings.HasPrefix(media, "text/"):
		return true
	case strings.HasPrefix(media, "application/json"),
		strings.HasPrefix(media, "application/xml"),
		strings.HasPrefix(media, "application/javascript"),
		strings.HasSuffix(media, "+json"),
		strings.HasSuffix(media, "+xml"),
		media == "image/svg+xml":
		return true
	}
	return false
}
