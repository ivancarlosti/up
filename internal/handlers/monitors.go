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
	// GroupID is the single group of the monitor, a pointer for the same reason:
	// an omitted field keeps the current group on update, while 0 moves the
	// monitor to no group.
	//
	// The name shadows the runtime GroupID of the embedded model (gorm:"-"), so
	// the value the handler reads is always the one the body carried: a body
	// without the key cannot inject a group through the model.
	GroupID *uint `json:"group_id"`
}

// monitorFilter reads the listing filters from the query string.
func monitorFilter(c *gin.Context) services.MonitorFilter {
	filter := services.MonitorFilter{
		Type:    queryString(c, "type"),
		Search:  queryString(c, "search"),
		Tag:     queryString(c, "tag"),
		GroupID: uint(queryInt(c, "group_id", 0)),
	}
	if raw := strings.TrimSpace(c.Query("active")); raw != "" {
		active := queryBool(c, "active", true)
		filter.Active = &active
	}
	return filter
}

// listMonitorTags returns the tags in use, each with the number of monitors that
// carry it, optionally narrowed to one monitor type (`?type=http`).
//
// It is what the tag pickers read: the dashboard has no tag vocabulary of its own
// (Monitor.Tags is a free form list), so the only honest list is the one the
// monitors actually carry.
func (h *Container) listMonitorTags(c *gin.Context) {
	tags, err := h.Monitors.ListTags(c.Request.Context(), strings.TrimSpace(c.Query("type")))
	if err != nil {
		api.WriteServiceError(c, err)
		return
	}
	api.OK(c, tags)
}

// bulkUpdateMonitorTags adds and/or removes tags on a set of monitors
// (dry run supported).
//
// The selection is the union of the given ids, the monitors of the given groups
// and the monitors carrying the given tag, so "everything tagged prod" — the
// rename case — is one request. dry_run previews the before/after of every row
// without writing, which is what the confirmation shows.
func (h *Container) bulkUpdateMonitorTags(c *gin.Context) {
	var payload struct {
		MonitorIDs []uint   `json:"monitor_ids"`
		GroupIDs   []uint   `json:"group_ids"`
		Tag        string   `json:"tag"`
		Add        []string `json:"add"`
		Remove     []string `json:"remove"`
		DryRun     bool     `json:"dry_run"`
	}
	if !bindJSON(c, &payload) {
		return
	}
	report, changed, err := h.Monitors.BulkUpdateTags(c.Request.Context(), services.BulkTagOptions{
		MonitorIDs: payload.MonitorIDs,
		GroupIDs:   payload.GroupIDs,
		Tag:        payload.Tag,
		Add:        payload.Add,
		Remove:     payload.Remove,
		DryRun:     payload.DryRun,
	})
	if err != nil {
		api.WriteServiceError(c, err)
		return
	}
	h.scheduleUpserts(changed)
	api.OK(c, report)
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
		if err := h.Monitors.DecorateList(ctx, monitors); err != nil {
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
	// A nil group_id means the same as 0 here: a create has no current group to
	// keep, so "not mentioned" and "no group" describe the same new row.
	if err := h.Monitors.Create(c.Request.Context(), &payload.Monitor, ids, payload.GroupID); err != nil {
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
	// An omitted group_id keeps the current group, while 0 moves the monitor to
	// no group: the pointer carries both states, exactly like notification_ids.
	if err := h.Monitors.Update(c.Request.Context(), &payload.Monitor, notificationIDs, payload.GroupID); err != nil {
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

// cloneMonitor duplicates a monitor (optionally its channels and its group).
func (h *Container) cloneMonitor(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	var payload struct {
		Name              string `json:"name"`
		CopyNotifications bool   `json:"copy_notifications"`
		CopyGroup         bool   `json:"copy_group"`
	}
	// An empty body is valid: the copy gets "<name> (copy)" and keeps neither
	// the channels nor the group.
	if c.Request.ContentLength > 0 {
		if !bindJSON(c, &payload) {
			return
		}
	}
	clone, err := h.Monitors.Clone(c.Request.Context(), id, services.MonitorCloneOptions{
		Name:              payload.Name,
		CopyNotifications: payload.CopyNotifications,
		CopyGroup:         payload.CopyGroup,
	})
	if err != nil {
		api.WriteServiceError(c, err)
		return
	}
	// The copy must start being checked by this node now instead of waiting for
	// the periodic reconciliation.
	if clone.Active {
		h.scheduleUpsert(clone.ID)
	}
	api.Created(c, clone)
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
