package handlers

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/ivancarlosti/up/internal/api"
	"github.com/ivancarlosti/up/internal/models"
	"github.com/ivancarlosti/up/internal/services"
)

// monitorPayload is the create/update body. Injecting the model keeps the API
// payload, the JSON column and the UI form in sync.
type monitorPayload struct {
	models.Monitor
	// NotificationIDs is a pointer so an omitted field keeps the current links
	// on update, while an empty list clears them.
	NotificationIDs *[]uint `json:"notification_ids"`
}

// monitorFilter reads the listing filters from the query string.
func monitorFilter(c *gin.Context) services.MonitorFilter {
	filter := services.MonitorFilter{
		Type:   queryString(c, "type"),
		Search: queryString(c, "search"),
		Tag:    queryString(c, "tag"),
	}
	if raw := strings.TrimSpace(c.Query("active")); raw != "" {
		active := queryBool(c, "active", true)
		filter.Active = &active
	}
	return filter
}

// listMonitors returns the monitors, decorated with status and uptime.
func (h *Container) listMonitors(c *gin.Context) {
	ctx := c.Request.Context()
	monitors, err := h.Monitors.List(ctx, monitorFilter(c))
	if err != nil {
		api.WriteServiceError(c, err)
		return
	}
	if queryBool(c, "decorate", true) {
		if err := h.Monitors.Decorate(ctx, monitors); err != nil {
			api.WriteServiceError(c, err)
			return
		}
	}
	api.OK(c, monitors)
}

// getMonitor returns a single monitor.
func (h *Container) getMonitor(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	monitor, err := h.Monitors.Get(c.Request.Context(), id)
	if err != nil {
		api.WriteServiceError(c, err)
		return
	}
	// The status bars of the detail view need the recent heartbeats; the field is
	// part of the model (json:"heartbeats") and the public status page already
	// fills it the same way. A failure here is not fatal: the charts just stay
	// empty instead of failing the whole request.
	if series, seriesErr := h.Monitors.SeriesFor(c.Request.Context(), monitor.ID, 40); seriesErr == nil {
		monitor.Heartbeats = series
	}
	api.OK(c, monitor)
}

// createMonitor stores a new monitor.
func (h *Container) createMonitor(c *gin.Context) {
	var payload monitorPayload
	if !bindJSON(c, &payload) {
		return
	}
	ids := []uint{}
	if payload.NotificationIDs != nil {
		ids = *payload.NotificationIDs
	}
	if err := h.Monitors.Create(c.Request.Context(), &payload.Monitor, ids); err != nil {
		api.WriteServiceError(c, err)
		return
	}
	h.scheduleUpsert(payload.Monitor.ID)

	created, err := h.Monitors.Get(c.Request.Context(), payload.Monitor.ID)
	if err != nil {
		api.Created(c, payload.Monitor)
		return
	}
	api.Created(c, created)
}

// updateMonitor saves an existing monitor.
func (h *Container) updateMonitor(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	var payload monitorPayload
	if !bindJSON(c, &payload) {
		return
	}
	payload.Monitor.ID = id
	// A nil slice keeps the current notification links, an empty slice clears
	// them: that is why the field is a pointer in the payload struct.
	var notificationIDs []uint
	if payload.NotificationIDs != nil {
		notificationIDs = *payload.NotificationIDs
		if notificationIDs == nil {
			notificationIDs = []uint{}
		}
	}
	if err := h.Monitors.Update(c.Request.Context(), &payload.Monitor, notificationIDs); err != nil {
		api.WriteServiceError(c, err)
		return
	}
	h.scheduleUpsert(id)

	updated, err := h.Monitors.Get(c.Request.Context(), id)
	if err != nil {
		api.OK(c, payload.Monitor)
		return
	}
	api.OK(c, updated)
}

// deleteMonitor removes a monitor and stops its worker.
func (h *Container) deleteMonitor(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	if err := h.Monitors.Delete(c.Request.Context(), id); err != nil {
		api.WriteServiceError(c, err)
		return
	}
	h.scheduleRemove(id)
	api.NoContent(c)
}

// pauseMonitor stops the checks of a monitor.
func (h *Container) pauseMonitor(c *gin.Context) {
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
	api.OK(c, monitor)
}

// resumeMonitor restarts the checks of a monitor.
func (h *Container) resumeMonitor(c *gin.Context) {
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
	api.OK(c, monitor)
}

// checkMonitor triggers an immediate heartbeat.
func (h *Container) checkMonitor(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	if _, err := h.Monitors.Get(c.Request.Context(), id); err != nil {
		api.WriteServiceError(c, err)
		return
	}
	h.scheduleCheckNow(id)
	c.JSON(http.StatusAccepted, gin.H{"queued": true, "monitor_id": id})
}

// monitorHeartbeats lists the raw heartbeats of a monitor.
func (h *Container) monitorHeartbeats(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	hours := queryInt(c, "hours", 24)
	limit := queryInt(c, "limit", 500)
	heartbeats, err := h.Stats.List(c.Request.Context(), id, hours, limit)
	if err != nil {
		api.WriteServiceError(c, err)
		return
	}
	api.OK(c, heartbeats)
}

// monitorStats returns the aggregated statistics and the heartbeat bars.
func (h *Container) monitorStats(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	ctx := c.Request.Context()
	hours := queryInt(c, "hours", 24)
	limit := queryInt(c, "limit", 60)

	stats, err := h.Stats.Window(ctx, id, hours)
	if err != nil {
		api.WriteServiceError(c, err)
		return
	}
	series, err := h.Stats.Series(ctx, id, limit)
	if err != nil {
		api.WriteServiceError(c, err)
		return
	}
	api.OK(c, gin.H{"stats": stats, "series": series})
}
