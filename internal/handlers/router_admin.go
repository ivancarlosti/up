package handlers

import (
	"github.com/gin-gonic/gin"

	"github.com/ivancarlosti/up/internal/middleware"
	"github.com/ivancarlosti/up/internal/models"
)

// registerAdmin wires the authenticated dashboard API.
func (h *Container) registerAdmin(engine *gin.Engine) {
	admin := engine.Group("/api",
		middleware.IPFilter(h.IPRules, models.IPRuleScopeDashboard),
		middleware.RequireSession(h.Sessions),
	)

	admin.GET("/dashboard", h.dashboard)
	admin.GET("/ws", h.websocket)

	monitors := admin.Group("/monitors")
	monitors.GET("", h.listMonitors)
	monitors.POST("", h.createMonitor)
	monitors.GET("/:id", h.getMonitor)
	monitors.PUT("/:id", h.updateMonitor)
	monitors.DELETE("/:id", h.deleteMonitor)
	monitors.POST("/:id/clone", h.cloneMonitor)
	monitors.POST("/bulk", h.bulkCreateMonitors)
	monitors.POST("/:id/pause", h.pauseMonitor)
	monitors.POST("/:id/resume", h.resumeMonitor)
	monitors.POST("/:id/check", h.checkMonitor)
	monitors.GET("/:id/heartbeats", h.monitorHeartbeats)
	monitors.GET("/:id/stats", h.monitorStats)

	groups := admin.Group("/monitor-groups")
	groups.GET("", h.listMonitorGroups)
	groups.POST("", h.createMonitorGroup)
	groups.GET("/:id", h.getMonitorGroup)
	groups.PUT("/:id", h.updateMonitorGroup)
	groups.DELETE("/:id", h.deleteMonitorGroup)
	groups.PUT("/:id/monitors", h.setMonitorGroupMonitors)
	groups.POST("/:id/clone", h.cloneMonitorGroup)

	templates := admin.Group("/monitor-templates")
	templates.GET("", h.listMonitorTemplates)
	templates.POST("", h.createMonitorTemplate)
	templates.GET("/:id", h.getMonitorTemplate)
	templates.PUT("/:id", h.updateMonitorTemplate)
	templates.DELETE("/:id", h.deleteMonitorTemplate)
	templates.POST("/:id/apply", h.applyMonitorTemplate)
	templates.POST("/:id/link-all", h.linkAllMonitorTemplate)

	notifications := admin.Group("/notifications")
	notifications.GET("", h.listNotifications)
	notifications.POST("", h.createNotification)
	notifications.GET("/logs", h.notificationLogs)
	notifications.GET("/:id", h.getNotification)
	notifications.PUT("/:id", h.updateNotification)
	notifications.DELETE("/:id", h.deleteNotification)
	notifications.POST("/:id/test", h.testNotification)

	pages := admin.Group("/status-pages")
	pages.GET("", h.listStatusPages)
	pages.POST("", h.createStatusPage)
	pages.GET("/:id", h.getStatusPage)
	pages.PUT("/:id", h.updateStatusPage)
	pages.DELETE("/:id", h.deleteStatusPage)
	pages.GET("/:id/monitors", h.statusPageMonitors)
	pages.PUT("/:id/monitors", h.setStatusPageMonitors)
	pages.GET("/:id/groups", h.statusPageGroups)
	pages.PUT("/:id/groups", h.setStatusPageGroups)

	cluster := admin.Group("/cluster")
	cluster.GET("/status", h.clusterStatus)
	cluster.GET("/sync/status", h.syncStatus)
	cluster.GET("/nodes", h.clusterNodes)
	cluster.POST("/leave", h.clusterLeave)
	cluster.GET("/settings", h.clusterGetSettings)
	cluster.PUT("/settings", h.clusterUpdateSettings)
	cluster.GET("/private-key", h.clusterPrivateKey)
	cluster.POST("/private-key/regenerate", h.clusterRegenerateKey)
	cluster.POST("/heartbeat", h.clusterHeartbeat)

	admin.GET("/admin/settings", h.adminSettings)
	admin.PUT("/admin/settings", h.updateAdminSettings)

	expiry := admin.Group("/admin/expiry")
	expiry.GET("", h.expirySettings)
	expiry.PUT("", h.updateExpirySettings)
	expiry.POST("/run", h.runExpiryNow)
	expiry.GET("/targets", h.expiryTargets)
	expiry.POST("/targets/refresh", h.refreshExpiryTarget)
	expiry.GET("/whois-parsers", h.listWhoisParsers)
	expiry.POST("/whois-parsers", h.createWhoisParser)
	expiry.POST("/whois-parsers/test", h.testWhoisParser)
	expiry.PUT("/whois-parsers/:id", h.updateWhoisParser)
	expiry.DELETE("/whois-parsers/:id", h.deleteWhoisParser)

	tokens := admin.Group("/tokens")
	tokens.GET("", h.listTokens)
	tokens.POST("", h.createToken)
	tokens.PUT("/:id", h.updateToken)
	tokens.POST("/:id/revoke", h.revokeToken)
	tokens.DELETE("/:id", h.deleteToken)

	rules := admin.Group("/ip-rules")
	rules.GET("", h.listIPRules)
	rules.POST("", h.createIPRule)
	rules.PUT("/:id", h.updateIPRule)
	rules.DELETE("/:id", h.deleteIPRule)
}
