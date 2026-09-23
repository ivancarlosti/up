// Package handlers contains the HTTP layer: route registration, request
// parsing/validation and JSON responses. All business rules live in the
// services package.
package handlers

import (
	"log/slog"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/ivancarlosti/up/internal/config"
	"github.com/ivancarlosti/up/internal/middleware"
	"github.com/ivancarlosti/up/internal/models"
	"github.com/ivancarlosti/up/internal/scheduler"
	"github.com/ivancarlosti/up/internal/services"
	"github.com/ivancarlosti/up/internal/ws"
)

// Container carries every dependency the handlers need.
type Container struct {
	Cfg              *config.Config
	Log              *slog.Logger
	DB               *gorm.DB
	Settings         *services.SettingService
	Monitors         *services.MonitorService
	MonitorGroups    *services.MonitorGroupService
	MonitorTemplates *services.MonitorTemplateService
	Heartbeats       *services.HeartbeatService
	Stats            *services.StatsService
	Notifications    *services.NotificationService
	Cluster          *services.ClusterService
	StatusPages      *services.StatusPageService
	Tokens           *services.TokenService
	IPRules          *services.IPRuleService
	Sessions         *services.SessionService
	Scheduler        *scheduler.Scheduler
	Hub              *ws.Hub
	// SPA serves the embedded frontend (assets and index.html fallback).
	SPA gin.HandlerFunc
	// OIDC is nil unless AUTH_METHOD=keycloak.
	OIDC *OIDCProvider
}

// Register wires every route into the engine.
func (h *Container) Register(engine *gin.Engine) {
	engine.Use(
		middleware.RequestID(),
		middleware.RealIP(h.Cfg),
		middleware.Logger(h.Log),
		middleware.SecurityHeaders(),
		middleware.Recovery(h.Log),
		middleware.CORS(h.Cfg),
	)
	middleware.TrustedProxyConfig(h.Cfg, engine)

	// Single page application fallback (client side routing).
	engine.NoRoute(h.serveSPA)

	// --- Unauthenticated -------------------------------------------------
	engine.GET("/api/health", h.health)
	engine.GET("/api/version", h.version)
	engine.GET("/api/settings", h.publicSettings)

	auth := engine.Group("/api/auth")
	auth.GET("/session", h.session)
	auth.POST("/login", middleware.RateLimit(h.Cfg.SecurityLoginRateLimit, time.Minute), h.login)
	auth.POST("/logout", h.logout)
	auth.GET("/oidc/login", h.oidcLogin)
	auth.GET("/callback", h.oidcCallback)

	// --- Public status pages --------------------------------------------
	public := engine.Group("/api/public",
		middleware.IPFilter(h.IPRules, models.IPRuleScopePublic),
		middleware.RateLimit(h.Cfg.SecurityPublicRateLimit, time.Minute),
	)
	public.GET("/status/:slug", h.publicStatusPage)
	public.GET("/status/:slug/badge.svg", h.publicStatusBadge)

	// --- Public REST API (bearer token) ---------------------------------
	v1 := engine.Group("/api/v1",
		middleware.IPFilter(h.IPRules, models.IPRuleScopeAPI),
		middleware.RateLimit(h.Cfg.SecurityPublicRateLimit, time.Minute),
	)
	v1.GET("/status", middleware.RequireToken(h.Tokens, models.ScopeRead), h.apiStatus)
	v1.GET("/monitors", middleware.RequireToken(h.Tokens, models.ScopeRead), h.apiListMonitors)
	v1.GET("/monitors/:id", middleware.RequireToken(h.Tokens, models.ScopeRead), h.apiGetMonitor)
	v1.GET("/monitors/:id/heartbeats", middleware.RequireToken(h.Tokens, models.ScopeRead), h.apiMonitorHeartbeats)
	v1.GET("/monitors/:id/uptime", middleware.RequireToken(h.Tokens, models.ScopeRead), h.apiMonitorUptime)
	v1.POST("/monitors/:id/pause", middleware.RequireToken(h.Tokens, models.ScopeWrite), h.apiPauseMonitor)
	v1.POST("/monitors/:id/resume", middleware.RequireToken(h.Tokens, models.ScopeWrite), h.apiResumeMonitor)
	v1.GET("/status-pages/:slug", middleware.RequireToken(h.Tokens, models.ScopeRead), h.publicStatusPage)

	// --- Cluster join ----------------------------------------------------
	// The same path serves two flavors (see docs/clustering.md): node to node
	// registration (authenticated with the cluster private key) and the UI
	// action performed on the joining node (authenticated with the session).
	engine.POST("/api/cluster/join",
		middleware.IPFilter(h.IPRules, models.IPRuleScopeDashboard),
		h.clusterJoin)

	h.registerAdmin(engine)
}

// serveSPA serves the embedded frontend.
func (h *Container) serveSPA(c *gin.Context) {
	if h.SPA == nil {
		c.String(503, "the frontend is not embedded in this binary")
		return
	}
	h.SPA(c)
}
