package handlers

import (
	"github.com/gin-gonic/gin"

	"github.com/ivancarlosti/up/internal/api"
	"github.com/ivancarlosti/up/internal/models"
)

// listStatusPages returns every status page.
func (h *Container) listStatusPages(c *gin.Context) {
	pages, err := h.StatusPages.List(c.Request.Context())
	if err != nil {
		api.WriteServiceError(c, err)
		return
	}
	api.OK(c, pages)
}

// getStatusPage returns a single status page.
func (h *Container) getStatusPage(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	page, err := h.StatusPages.Get(c.Request.Context(), id)
	if err != nil {
		api.WriteServiceError(c, err)
		return
	}
	items, err := h.StatusPages.Items(c.Request.Context(), id)
	if err != nil {
		api.WriteServiceError(c, err)
		return
	}
	groups, err := h.StatusPages.Groups(c.Request.Context(), id)
	if err != nil {
		api.WriteServiceError(c, err)
		return
	}
	api.OK(c, gin.H{"page": page, "monitors": items, "groups": groups})
}

// statusPageGroups returns the groups included in a status page, in page order.
func (h *Container) statusPageGroups(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	groups, err := h.StatusPages.Groups(c.Request.Context(), id)
	if err != nil {
		api.WriteServiceError(c, err)
		return
	}
	api.OK(c, groups)
}

// setStatusPageGroups replaces the monitor groups included in a status page.
func (h *Container) setStatusPageGroups(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	var payload struct {
		Groups []models.StatusPageGroupLink `json:"groups"`
	}
	if !bindJSON(c, &payload) {
		return
	}
	if err := h.StatusPages.SetGroups(c.Request.Context(), id, payload.Groups); err != nil {
		api.WriteServiceError(c, err)
		return
	}
	groups, err := h.StatusPages.Groups(c.Request.Context(), id)
	if err != nil {
		api.WriteServiceError(c, err)
		return
	}
	api.OK(c, groups)
}

// createStatusPage stores a new status page.
func (h *Container) createStatusPage(c *gin.Context) {
	var payload struct {
		models.StatusPage
		MonitorIDs []uint `json:"monitor_ids"`
	}
	if !bindJSON(c, &payload) {
		return
	}
	if err := h.StatusPages.Create(c.Request.Context(), &payload.StatusPage); err != nil {
		api.WriteServiceError(c, err)
		return
	}
	if len(payload.MonitorIDs) > 0 {
		if err := h.StatusPages.SetMonitors(c.Request.Context(), payload.StatusPage.ID, payload.MonitorIDs); err != nil {
			api.WriteServiceError(c, err)
			return
		}
	}
	api.Created(c, payload.StatusPage)
}

// updateStatusPage saves an existing status page.
func (h *Container) updateStatusPage(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	var payload models.StatusPage
	if !bindJSON(c, &payload) {
		return
	}
	payload.ID = id
	if err := h.StatusPages.Update(c.Request.Context(), &payload); err != nil {
		api.WriteServiceError(c, err)
		return
	}
	api.OK(c, payload)
}

// deleteStatusPage removes a status page.
func (h *Container) deleteStatusPage(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	if err := h.StatusPages.Delete(c.Request.Context(), id); err != nil {
		api.WriteServiceError(c, err)
		return
	}
	api.NoContent(c)
}

// statusPageMonitors lists the monitor selection.
func (h *Container) statusPageMonitors(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	items, err := h.StatusPages.Items(c.Request.Context(), id)
	if err != nil {
		api.WriteServiceError(c, err)
		return
	}
	api.OK(c, items)
}

// setStatusPageMonitors replaces the monitor selection (order matters).
func (h *Container) setStatusPageMonitors(c *gin.Context) {
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
	if err := h.StatusPages.SetMonitors(c.Request.Context(), id, payload.MonitorIDs); err != nil {
		api.WriteServiceError(c, err)
		return
	}
	items, err := h.StatusPages.Items(c.Request.Context(), id)
	if err != nil {
		api.WriteServiceError(c, err)
		return
	}
	api.OK(c, items)
}
