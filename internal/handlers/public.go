package handlers

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/ivancarlosti/up/internal/api"
	"github.com/ivancarlosti/up/internal/config"
	"github.com/ivancarlosti/up/internal/i18n"
	"github.com/ivancarlosti/up/internal/models"
	"github.com/ivancarlosti/up/internal/version"
)

// health is used by the container health check and by load balancers.
func (h *Container) health(c *gin.Context) {
	payload := gin.H{
		"status":  "ok",
		"version": version.Version,
		"node_id": h.Cfg.NodeID,
		"time":    time.Now().UTC(),
	}
	// The database is the only hard dependency: report it explicitly so a broken
	// connection is visible in the health check.
	if h.DB != nil {
		sqlDB, err := h.DB.DB()
		if err != nil || sqlDB.Ping() != nil {
			payload["status"] = "degraded"
			payload["database"] = "unreachable"
			c.JSON(http.StatusServiceUnavailable, payload)
			return
		}
		payload["database"] = "ok"
	}
	c.JSON(http.StatusOK, payload)
}

// version returns the build metadata.
func (h *Container) version(c *gin.Context) {
	api.OK(c, gin.H{
		"version":  version.Version,
		"commit":   version.Commit,
		"readable": version.Readable(),
	})
}

// publicSettings is consumed by the frontend on boot: it tells the SPA which
// auth method is active, which defaults to apply and what the instance is.
func (h *Container) publicSettings(c *gin.Context) {
	api.OK(c, gin.H{
		"app_name":               h.Settings.GetOr(models.SettingAppName, "Up"),
		"version":                version.Version,
		"auth_method":            string(h.Cfg.AuthMethod),
		"auth_enabled":           h.Cfg.AuthEnabled(),
		"recaptcha_enabled":      h.Cfg.RecaptchaEnabled,
		"recaptcha_client_id":    h.Cfg.RecaptchaClientID,
		"default_locale":         h.Settings.DefaultLocale(),
		"default_theme":          h.Settings.DefaultTheme(),
		"time_format":            h.Settings.TimeFormat(),
		"uptime_window_hours":    h.Settings.UptimeWindowHours(),
		"uptime_windows":         models.UptimeWindowHoursAllowed,
		"supported_locales":      config.SupportedLocales,
		"supported_themes":       config.SupportedThemes,
		"supported_time_formats": config.SupportedTimeFormats,
		"cluster_enabled":        h.Cfg.ClusterEnabled,
		"node_id":                h.Cfg.NodeID,
		"node_name":              h.Cfg.NodeName,
	})
}

// publicStatusPage renders the payload of a public status page. Administrators
// holding a valid session can also preview a non public page.
func (h *Container) publicStatusPage(c *gin.Context) {
	_, isAdmin := h.Sessions.CurrentIdentity(c.Request.Context(), c.Request)
	page, err := h.StatusPages.PublicPayload(c.Request.Context(), c.Param("slug"), isAdmin)
	if err != nil {
		api.WriteServiceError(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	api.OK(c, page)
}

// publicStatusBadge renders the shields.io style badge of a status page.
func (h *Container) publicStatusBadge(c *gin.Context) {
	svg, err := h.StatusPages.BadgeSVG(c.Request.Context(), c.Param("slug"))
	if err != nil {
		api.WriteServiceError(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.Data(http.StatusOK, "image/svg+xml; charset=utf-8", []byte(svg))
}

// dashboard returns the decorated monitor list plus the summary counters shown
// in the header (also used as the initial WebSocket snapshot).
func (h *Container) dashboard(c *gin.Context) {
	ctx := c.Request.Context()
	monitors, err := h.Monitors.List(ctx, monitorFilter(c))
	if err != nil {
		api.WriteServiceError(c, err)
		return
	}
	if err := h.Monitors.Decorate(ctx, monitors); err != nil {
		api.WriteServiceError(c, err)
		return
	}

	summary := gin.H{
		"total":         len(monitors),
		"up":            0,
		"down":          0,
		"degraded":      0,
		"pending":       0,
		"maintenance":   0,
		"unknown":       0,
		"paused":        0,
		"heartbeats_1h": int64(0),
		"ws_clients":    0,
	}
	for _, monitor := range monitors {
		if !monitor.Active {
			summary["paused"] = summary["paused"].(int) + 1
			continue
		}
		switch monitor.Status {
		case models.AggregateUp:
			summary["up"] = summary["up"].(int) + 1
		case models.AggregateDown:
			summary["down"] = summary["down"].(int) + 1
		case models.AggregateDegraded:
			summary["degraded"] = summary["degraded"].(int) + 1
		case models.AggregatePending:
			summary["pending"] = summary["pending"].(int) + 1
		case models.AggregateMaintenance:
			summary["maintenance"] = summary["maintenance"].(int) + 1
		default:
			summary["unknown"] = summary["unknown"].(int) + 1
		}
	}
	if count, countErr := h.Heartbeats.PingStats(ctx); countErr == nil {
		summary["heartbeats_1h"] = count
	}
	if h.Hub != nil {
		summary["ws_clients"] = h.Hub.ClientCount()
	}
	if clusterStatus, clusterErr := h.Cluster.Status(ctx); clusterErr == nil {
		summary["cluster"] = clusterStatus
	}

	api.OK(c, gin.H{"monitors": monitors, "summary": summary})
}

// adminSettings returns the runtime settings managed from Admin > Settings.
func (h *Container) adminSettings(c *gin.Context) {
	api.OK(c, gin.H{
		"default_locale":         h.Settings.DefaultLocale(),
		"default_theme":          h.Settings.DefaultTheme(),
		"time_format":            h.Settings.TimeFormat(),
		"app_name":               h.Settings.GetOr(models.SettingAppName, "Up"),
		"app_url":                h.Cfg.AppURL,
		"supported_locales":      config.SupportedLocales,
		"supported_themes":       config.SupportedThemes,
		"supported_time_formats": config.SupportedTimeFormats,
		"auth_method":            string(h.Cfg.AuthMethod),
		"cluster_enabled":        h.Cfg.ClusterEnabled,
		"version":                version.Version,
		"settings":               h.Settings.All(),
		// History retention and the global uptime window (see the settings
		// card of the same name).
		"heartbeat_retention_days": h.Settings.HeartbeatRetentionDays(),
		"default_retention_days":   models.DefaultHeartbeatRetentionDays,
		"max_retention_days":       models.MaxHeartbeatRetentionDays,
		"uptime_window_hours":      h.Settings.UptimeWindowHours(),
		"uptime_windows":           models.UptimeWindowHoursAllowed,
	})
}

// updateAdminSettings stores the locale/theme defaults, the instance name and
// the housekeeping values. Every field is optional: an omitted one keeps its
// current value.
func (h *Container) updateAdminSettings(c *gin.Context) {
	var payload struct {
		DefaultLocale string `json:"default_locale"`
		DefaultTheme  string `json:"default_theme"`
		TimeFormat    string `json:"time_format"`
		AppName       string `json:"app_name"`
		// Pointers so an omitted field keeps the current value (0 is a valid
		// retention: it means "never purge").
		HeartbeatRetentionDays *int `json:"heartbeat_retention_days"`
		UptimeWindowHours      *int `json:"uptime_window_hours"`
	}
	if !bindJSON(c, &payload) {
		return
	}
	if err := h.Settings.SetDefaults(c.Request.Context(), payload.DefaultLocale, payload.DefaultTheme, payload.TimeFormat); err != nil {
		api.WriteError(c, http.StatusBadRequest, i18n.CodeValidation, err.Error())
		return
	}
	if payload.AppName != "" {
		if err := h.Settings.Set(c.Request.Context(), models.SettingAppName, payload.AppName); err != nil {
			api.WriteServiceError(c, err)
			return
		}
	}
	if payload.HeartbeatRetentionDays != nil {
		if err := h.Settings.SetHeartbeatRetentionDays(c.Request.Context(), *payload.HeartbeatRetentionDays); err != nil {
			api.WriteServiceError(c, err)
			return
		}
	}
	if payload.UptimeWindowHours != nil {
		if err := h.Settings.SetUptimeWindowHours(c.Request.Context(), *payload.UptimeWindowHours); err != nil {
			api.WriteServiceError(c, err)
			return
		}
	}
	c.Status(http.StatusNoContent)
}
