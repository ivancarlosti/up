package handlers

import (
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/ivancarlosti/up/internal/api"
	"github.com/ivancarlosti/up/internal/boot"
	"github.com/ivancarlosti/up/internal/config"
	"github.com/ivancarlosti/up/internal/i18n"
	"github.com/ivancarlosti/up/internal/middleware"
	"github.com/ivancarlosti/up/internal/version"
)

// bootPayload builds the body shared by GET /api/health and GET /api/boot.
//
// Both answer with the same shape on purpose: the container health check reads
// /api/health and reacts to the HTTP status, while the SPA polls /api/boot
// (always 200, see below) to know which phase it is waiting for. One shape
// means one place to keep in sync, documented in docs/api.md (section 2).
func bootPayload(state *boot.State) gin.H {
	snap := state.Snapshot()
	payload := gin.H{
		"status":     snap.Status,
		"version":    version.Version,
		"node_id":    snap.NodeID,
		"ready":      snap.Ready,
		"phase":      string(snap.Phase),
		"database":   snap.Database,
		"since":      snap.Since,
		"elapsed_ms": snap.ElapsedMS,
		"phase_ms":   snap.PhaseMS,
		"time":       time.Now().UTC(),
	}
	if snap.Code != "" {
		payload["code"] = snap.Code
	}
	if snap.Detail != "" {
		payload["detail"] = snap.Detail
		// The 503 of GET /api/health is an API error from the client's point of
		// view, and every API error body carries a "message": repeating the
		// detail here is what lets the SPA build the same object from either
		// endpoint (docs/api.md, section 11).
		payload["message"] = snap.Detail
	}
	if snap.Attempts > 0 {
		payload["attempts"] = snap.Attempts
		payload["next_retry_ms"] = snap.NextRetryMS
	}
	if snap.Maintenance != "" {
		payload["maintenance"] = snap.Maintenance
	}
	return payload
}

// NewBootstrapEngine returns the engine that answers while the boot sequence is
// still preparing the database.
//
// The process listens from the first millisecond (see cmd/server/main.go), so
// this engine is what a browser finds during the first seconds of a start, or
// for as long as a database stays broken. It serves the embedded SPA - the page
// has to exist for the user to read anything at all - plus the three endpoints
// that need no dependency, and answers every other /api/* route with a 503
// carrying the boot state: /api/settings and /api/auth/session are what the SPA
// asks for first, and that 503 is what turns "the page is stuck on a spinner"
// into "Up is starting, reading the schema".
func NewBootstrapEngine(cfg *config.Config, log *slog.Logger, state *boot.State) *gin.Engine {
	engine := gin.New()
	engine.Use(
		middleware.RequestID(),
		middleware.RealIP(cfg),
		middleware.Logger(log),
		middleware.SecurityHeaders(),
		middleware.Recovery(log),
		middleware.CORS(cfg),
	)
	middleware.TrustedProxyConfig(cfg, engine)

	spa := NewSPAHandler(log)

	engine.GET("/api/health", bootHealth(state))
	engine.GET("/api/boot", bootStatus(state))
	engine.GET("/api/version", func(c *gin.Context) {
		api.OK(c, gin.H{
			"version":  version.Version,
			"commit":   version.Commit,
			"readable": version.Readable(),
		})
	})

	engine.NoRoute(func(c *gin.Context) {
		if strings.HasPrefix(c.Request.URL.Path, "/api/") {
			writeBootUnavailable(c, state)
			return
		}
		if spa == nil {
			c.String(http.StatusServiceUnavailable, "the frontend is not embedded in this binary")
			return
		}
		spa(c)
	})
	return engine
}

// bootHealth answers GET /api/health from the boot state alone: this engine
// runs before any *gorm.DB exists. It follows the same rule as the real health
// handler, 200 only when the boot finished, so the container health check does
// not depend on which engine happens to be installed.
func bootHealth(state *boot.State) gin.HandlerFunc {
	return func(c *gin.Context) {
		payload := bootPayload(state)
		status := http.StatusOK
		if !state.Snapshot().Ready {
			status = http.StatusServiceUnavailable
		}
		c.JSON(status, payload)
	}
}

// bootStatus answers GET /api/boot.
//
// Always 200, never an error status: the endpoint exists to be polled by the
// waiting SPA, and a 503 would be logged by every browser as a failed request.
// The "status" and "ready" fields carry the truth.
func bootStatus(state *boot.State) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.JSON(http.StatusOK, bootPayload(state))
	}
}

// bootStatus answers GET /api/boot on the wired engine.
//
// It is always 200 and always the current boot state: the SPA polls it while it
// waits (see NewBootstrapEngine) and after the boot it keeps working, which is
// how the process reports a database that died later or a background job that
// could not finish.
func (h *Container) bootStatus(c *gin.Context) {
	c.JSON(http.StatusOK, bootPayload(h.State))
}

// writeBootUnavailable answers an API route that needs the wired services.
func writeBootUnavailable(c *gin.Context, state *boot.State) {
	snap := state.Snapshot()
	code := snap.Code
	message := snap.Detail
	if code == "" {
		code = i18n.CodeBootStarting
		message = fmt.Sprintf("the server is still starting (phase %s)", snap.Phase)
	}
	api.WriteError(c, http.StatusServiceUnavailable, code, message)
}
