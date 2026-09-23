package handlers

import (
	"github.com/gin-gonic/gin"

	"github.com/ivancarlosti/up/internal/api"
	"github.com/ivancarlosti/up/internal/models"
	"github.com/ivancarlosti/up/internal/services"
)

// monitorGroupPayload is the create/update body of a monitor group.
type monitorGroupPayload struct {
	models.MonitorGroup
	// MonitorIDs is a pointer so an omitted field keeps the current members on
	// update, while an empty list clears them (same contract as the monitor
	// notification links).
	MonitorIDs *[]uint `json:"monitor_ids"`
}

// listMonitorGroups returns every group with its monitor ids.
func (h *Container) listMonitorGroups(c *gin.Context) {
	groups, err := h.MonitorGroups.List(c.Request.Context())
	if err != nil {
		api.WriteServiceError(c, err)
		return
	}
	api.OK(c, groups)
}

// getMonitorGroup returns a single group.
func (h *Container) getMonitorGroup(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	group, err := h.MonitorGroups.Get(c.Request.Context(), id)
	if err != nil {
		api.WriteServiceError(c, err)
		return
	}
	api.OK(c, group)
}

// createMonitorGroup stores a new group.
func (h *Container) createMonitorGroup(c *gin.Context) {
	var payload monitorGroupPayload
	if !bindJSON(c, &payload) {
		return
	}
	ids := []uint{}
	if payload.MonitorIDs != nil {
		ids = *payload.MonitorIDs
	}
	if err := h.MonitorGroups.Create(c.Request.Context(), &payload.MonitorGroup, ids); err != nil {
		api.WriteServiceError(c, err)
		return
	}
	created, err := h.MonitorGroups.Get(c.Request.Context(), payload.MonitorGroup.ID)
	if err != nil {
		api.Created(c, payload.MonitorGroup)
		return
	}
	api.Created(c, created)
}

// updateMonitorGroup saves an existing group.
func (h *Container) updateMonitorGroup(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	var payload monitorGroupPayload
	if !bindJSON(c, &payload) {
		return
	}
	payload.MonitorGroup.ID = id
	// A nil slice keeps the current members, an empty slice clears them.
	var monitorIDs []uint
	if payload.MonitorIDs != nil {
		monitorIDs = *payload.MonitorIDs
		if monitorIDs == nil {
			monitorIDs = []uint{}
		}
	}
	if err := h.MonitorGroups.Update(c.Request.Context(), &payload.MonitorGroup, monitorIDs); err != nil {
		api.WriteServiceError(c, err)
		return
	}
	updated, err := h.MonitorGroups.Get(c.Request.Context(), id)
	if err != nil {
		api.OK(c, payload.MonitorGroup)
		return
	}
	api.OK(c, updated)
}

// deleteMonitorGroup removes a group (the monitors stay).
func (h *Container) deleteMonitorGroup(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	if err := h.MonitorGroups.Delete(c.Request.Context(), id); err != nil {
		api.WriteServiceError(c, err)
		return
	}
	api.NoContent(c)
}

// setMonitorGroupMonitors rewrites the members of a group.
func (h *Container) setMonitorGroupMonitors(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	var payload struct {
		MonitorIDs []uint `json:"monitor_ids"`
	}
	if !bindJSON(c, &payload) {
		return
	}
	ids := payload.MonitorIDs
	if ids == nil {
		ids = []uint{}
	}
	group, err := h.MonitorGroups.SetMonitors(c.Request.Context(), id, ids)
	if err != nil {
		api.WriteServiceError(c, err)
		return
	}
	api.OK(c, group)
}

// cloneMonitorGroup copies a group, optionally with its monitors.
func (h *Container) cloneMonitorGroup(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	var payload struct {
		Name string `json:"name"`
		// Deep also clones every monitor of the group.
		Deep bool `json:"deep"`
		// CopyLinks keeps the notification channels of each cloned monitor.
		CopyLinks bool `json:"copy_links"`
	}
	// An empty body is valid: everything has a default.
	if c.Request.ContentLength > 0 {
		if !bindJSON(c, &payload) {
			return
		}
	}
	group, err := h.MonitorGroups.Clone(c.Request.Context(), id, services.MonitorGroupCloneOptions{
		Name:      payload.Name,
		Deep:      payload.Deep,
		CopyLinks: payload.CopyLinks,
	})
	if err != nil {
		api.WriteServiceError(c, err)
		return
	}
	// A deep clone created monitors: this node must start checking them now
	// instead of waiting for the periodic reconciliation.
	if payload.Deep {
		for _, monitorID := range group.MonitorIDs {
			h.scheduleUpsert(monitorID)
		}
	}
	api.Created(c, group)
}
