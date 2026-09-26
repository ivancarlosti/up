package handlers

import (
	"time"

	"github.com/gin-gonic/gin"

	"github.com/ivancarlosti/up/internal/api"
	"github.com/ivancarlosti/up/internal/models"
	"github.com/ivancarlosti/up/internal/services"
)

// secretPlaceholder replaces credentials in the public API responses.
const secretPlaceholder = "***"

// redactMonitor returns a copy of the monitor with the credentials masked,
// which is the default behaviour of the public API. Pass ?include_secrets=true
// to receive the raw configuration (documented in docs/public-api.md).
func redactMonitor(monitor *models.Monitor) *models.Monitor {
	copy := *monitor
	if copy.Config.BasicPass != "" {
		copy.Config.BasicPass = secretPlaceholder
	}
	if copy.Config.BearerToken != "" {
		copy.Config.BearerToken = secretPlaceholder
	}
	return &copy
}

// apiStatus returns a compact overall status used by scripts and dashboards.
func (h *Container) apiStatus(c *gin.Context) {
	ctx := c.Request.Context()
	monitors, err := h.Monitors.List(ctx, services.MonitorFilter{})
	if err != nil {
		api.WriteServiceError(c, err)
		return
	}
	if err := h.Monitors.DecorateList(ctx, monitors); err != nil {
		api.WriteServiceError(c, err)
		return
	}

	payload := gin.H{
		"overall":      models.AggregateUnknown,
		"total":        len(monitors),
		"up":           0,
		"down":         0,
		"degraded":     0,
		"unknown":      0,
		"node_id":      h.Cfg.NodeID,
		"generated_at": time.Now().UTC(),
		"monitors":     []any{},
	}
	summary := []*models.Monitor{}
	for _, monitor := range monitors {
		switch monitor.Status {
		case models.AggregateUp:
			payload["up"] = payload["up"].(int) + 1
		case models.AggregateDown:
			payload["down"] = payload["down"].(int) + 1
		case models.AggregateDegraded:
			payload["degraded"] = payload["degraded"].(int) + 1
		default:
			payload["unknown"] = payload["unknown"].(int) + 1
		}
		if queryBool(c, "include_monitors", false) {
			summary = append(summary, redactMonitor(monitor))
		}
	}
	switch {
	case payload["down"].(int) > 0:
		payload["overall"] = models.AggregateDown
	case payload["degraded"].(int) > 0:
		payload["overall"] = models.AggregateDegraded
	case payload["up"].(int) > 0:
		payload["overall"] = models.AggregateUp
	}
	payload["monitors"] = summary
	api.OK(c, payload)
}

// apiListMonitors lists the monitors with their current status.
func (h *Container) apiListMonitors(c *gin.Context) {
	ctx := c.Request.Context()
	monitors, err := h.Monitors.List(ctx, monitorFilter(c))
	if err != nil {
		api.WriteServiceError(c, err)
		return
	}
	if err := h.Monitors.DecorateList(ctx, monitors); err != nil {
		api.WriteServiceError(c, err)
		return
	}
	if !queryBool(c, "include_secrets", false) {
		for i, monitor := range monitors {
			monitors[i] = redactMonitor(monitor)
		}
	}
	api.OK(c, monitors)
}

// apiGetMonitor returns a single monitor with its statistics.
func (h *Container) apiGetMonitor(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	ctx := c.Request.Context()
	monitor, err := h.Monitors.Get(ctx, id)
	if err != nil {
		api.WriteServiceError(c, err)
		return
	}
	// Decorate adds the runtime fields the list endpoint already returns (uptime,
	// votes, certificate, domain): asking for one monitor used to answer with a
	// thinner payload than the list, which is never what a client expects when
	// the expiry data is part of the contract.
	if err := h.Monitors.Decorate(ctx, []*models.Monitor{monitor}); err != nil {
		api.WriteServiceError(c, err)
		return
	}
	stats, err := h.Stats.Window(ctx, id, queryInt(c, "hours", 24))
	if err != nil {
		api.WriteServiceError(c, err)
		return
	}
	if !queryBool(c, "include_secrets", false) {
		monitor = redactMonitor(monitor)
	}
	api.OK(c, gin.H{"monitor": monitor, "stats": stats})
}

// apiMonitorHeartbeats lists the heartbeats of a monitor.
func (h *Container) apiMonitorHeartbeats(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	heartbeats, err := h.Stats.List(c.Request.Context(), id,
		queryInt(c, "hours", 24), queryInt(c, "limit", 100))
	if err != nil {
		api.WriteServiceError(c, err)
		return
	}
	api.OK(c, heartbeats)
}

// apiMonitorUptime returns the uptime/latency window of a monitor.
func (h *Container) apiMonitorUptime(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	stats, err := h.Stats.Window(c.Request.Context(), id, queryInt(c, "hours", 24))
	if err != nil {
		api.WriteServiceError(c, err)
		return
	}
	api.OK(c, stats)
}

// apiPauseMonitor pauses a monitor (write scope).
func (h *Container) apiPauseMonitor(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	monitor, err := h.Monitors.SetActive(c.Request.Context(), id, false)
	if err != nil {
		api.WriteServiceError(c, err)
		return
	}
	h.scheduleRemove(id)
	api.OK(c, redactMonitor(monitor))
}

// apiResumeMonitor resumes a monitor (write scope).
func (h *Container) apiResumeMonitor(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	monitor, err := h.Monitors.SetActive(c.Request.Context(), id, true)
	if err != nil {
		api.WriteServiceError(c, err)
		return
	}
	h.scheduleUpsert(id)
	api.OK(c, redactMonitor(monitor))
}
