package handlers

import (
	"io/fs"
	"log/slog"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/ivancarlosti/up/internal/api"
	"github.com/ivancarlosti/up/internal/i18n"
	"github.com/ivancarlosti/up/web"
)

// NewSPAHandler serves the embedded frontend.
//
//   - /assets/* receives a long lived immutable cache header (Vite fingerprints
//     the file names);
//   - index.html is never cached so a new deployment is picked up immediately;
//   - any unknown path falls back to index.html (Vue Router history mode),
//     except /api/* which answers with a JSON 404.
//
// The shell is read once and written directly: delegating it to
// http.FileServer would trigger its canonical "/index.html -> ./" redirect,
// which breaks the client side routes.
func NewSPAHandler(log *slog.Logger) gin.HandlerFunc {
	dist, err := web.Dist()
	if err != nil || !web.Available() {
		log.Warn("no frontend build embedded in this binary; only the API is served")
		return nil
	}

	shell, err := fs.ReadFile(dist, "index.html")
	if err != nil {
		log.Error("could not read the embedded index.html", "error", err)
		return nil
	}
	fileServer := http.FileServer(http.FS(dist))

	return func(c *gin.Context) {
		requestPath := strings.TrimPrefix(c.Request.URL.Path, "/")
		if requestPath == "" {
			requestPath = "index.html"
		}

		if _, statErr := fs.Stat(dist, requestPath); statErr != nil {
			if strings.HasPrefix(c.Request.URL.Path, "/api/") {
				api.NotFound(c, i18n.CodeNotFoundRoute, "route "+c.Request.URL.Path+" does not exist")
				return
			}
			serveShell(c, shell)
			return
		}

		if requestPath == "index.html" {
			serveShell(c, shell)
			return
		}
		if strings.HasPrefix(requestPath, "assets/") {
			c.Header("Cache-Control", "public, max-age=31536000, immutable")
		}
		fileServer.ServeHTTP(c.Writer, c.Request)
	}
}

// serveShell answers with the single page application shell.
func serveShell(c *gin.Context, shell []byte) {
	c.Header("Cache-Control", "no-store")
	c.Data(http.StatusOK, "text/html; charset=utf-8", shell)
}
