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

	cluster := admin.Group("/cluster")
	cluster.GET("/status", h.clusterStatus)
	cluster.GET("/nodes", h.clusterNodes)
	cluster.POST("/leave", h.clusterLeave)
	cluster.GET("/settings", h.clusterGetSettings)
	cluster.PUT("/settings", h.clusterUpdateSettings)
	cluster.GET("/private-key", h.clusterPrivateKey)
	cluster.POST("/private-key/regenerate", h.clusterRegenerateKey)
	cluster.POST("/heartbeat", h.clusterHeartbeat)

	admin.GET("/admin/settings", h.adminSettings)
	admin.PUT("/admin/settings", h.updateAdminSettings)

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
