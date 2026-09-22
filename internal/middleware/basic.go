package middleware

import (
	"log/slog"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/ivancarlosti/up/internal/config"
	"github.com/ivancarlosti/up/internal/utils"
)

// RequestID assigns (or reuses) an identifier for the request, useful to
// correlate the access log with the application log.
func RequestID() gin.HandlerFunc {
	return func(c *gin.Context) {
		requestID := c.GetHeader("X-Request-Id")
		if requestID == "" {
			requestID = uuid.NewString()
		}
		c.Set(CtxRequestID, requestID)
		c.Header("X-Request-Id", requestID)
		c.Next()
	}
}

// RealIP resolves the client address honouring APP_TRUST_PROXY. The value is
// stored in the context so the IP rules, the rate limiter and the audit fields
// all use the same address.
func RealIP(cfg *config.Config) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Set(CtxClientIP, utils.ClientIP(c.Request, cfg.AppTrustProxy))
		c.Next()
	}
}

// Logger writes one structured access log line per request.
func Logger(log *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		started := time.Now()
		c.Next()

		status := c.Writer.Status()
		level := slog.LevelInfo
		switch {
		case status >= 500:
			level = slog.LevelError
		case status >= 400:
			level = slog.LevelWarn
		}

		attributes := []any{
			"method", c.Request.Method,
			"path", c.Request.URL.Path,
			"status", status,
			"duration_ms", time.Since(started).Milliseconds(),
			"ip", ClientIP(c),
			"request_id", c.GetString(CtxRequestID),
			"bytes", c.Writer.Size(),
		}
		if len(c.Errors) > 0 {
			attributes = append(attributes, "errors", c.Errors.String())
		}
		log.Log(c.Request.Context(), level, "http request", attributes...)
	}
}

// SecurityHeaders sets a small set of defensive response headers. They are
// harmless for the SPA (which loads its own scripts) and useful when the
// instance is exposed directly without a reverse proxy.
func SecurityHeaders() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("X-Content-Type-Options", "nosniff")
		c.Header("X-Frame-Options", "SAMEORIGIN")
		c.Header("Referrer-Policy", "strict-origin-when-cross-origin")
		c.Header("Permissions-Policy", "geolocation=(), microphone=(), camera=()")
		c.Next()
	}
}

// Recovery turns a panic into a 500 response without killing the process.
func Recovery(log *slog.Logger) gin.HandlerFunc {
	return gin.CustomRecoveryWithWriter(nil, func(c *gin.Context, recovered any) {
		log.Error("panic recovered",
			"path", c.Request.URL.Path,
			"method", c.Request.Method,
			"request_id", c.GetString(CtxRequestID),
			"panic", recovered,
		)
		c.AbortWithStatusJSON(500, gin.H{
			"code":    "ERR_INTERNAL",
			"message": "internal server error",
		})
	})
}

// CORS allows the SPA to call the API when it is served from the canonical
// APP_URL origin (which is the case behind every supported reverse proxy).
func CORS(cfg *config.Config) gin.HandlerFunc {
	return func(c *gin.Context) {
		origin := c.GetHeader("Origin")
		if origin != "" && (origin == cfg.AppURL || cfg.AppURL == "") {
			c.Header("Access-Control-Allow-Origin", origin)
			c.Header("Vary", "Origin")
			c.Header("Access-Control-Allow-Credentials", "true")
			c.Header("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Cluster-Key, X-Request-Id")
			c.Header("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
		}
		if c.Request.Method == "OPTIONS" {
			c.AbortWithStatus(204)
			return
		}
		c.Next()
	}
}
