package handlers

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/ivancarlosti/up/internal/api"
	"github.com/ivancarlosti/up/internal/i18n"
	"github.com/ivancarlosti/up/internal/services"
)

// requireSystems answers 503 when the hardware monitoring service is not wired.
func (h *Container) requireSystems(c *gin.Context) bool {
	if h.Systems != nil {
		return true
	}
	api.WriteError(c, http.StatusServiceUnavailable, i18n.CodeValidation, "hardware monitoring is not enabled")
	return false
}

// listSystems returns every hardware monitored machine.
func (h *Container) listSystems(c *gin.Context) {
	if !h.requireSystems(c) {
		return
	}
	systems, err := h.Systems.List(c.Request.Context())
	if err != nil {
		api.WriteServiceError(c, err)
		return
	}
	api.OK(c, systems)
}

// getSystemHistory is the detail page: the row, the charts, the containers and
// the SMART report in one answer.
func (h *Container) getSystemHistory(c *gin.Context) {
	if !h.requireSystems(c) {
		return
	}
	id, ok := pathID(c)
	if !ok {
		return
	}
	history, err := h.Systems.History(c.Request.Context(), id, queryInt(c, "hours", 24))
	if err != nil {
		api.WriteServiceError(c, err)
		return
	}
	api.OK(c, history)
}

// systemsOverview returns the most recent sample of every machine: what the cards
// of the listing render under each name.
func (h *Container) systemsOverview(c *gin.Context) {
	if !h.requireSystems(c) {
		return
	}
	rows, err := h.Systems.Latest(c.Request.Context())
	if err != nil {
		api.WriteServiceError(c, err)
		return
	}
	api.OK(c, rows)
}

// getSystem returns one system without its history (the edit dialog).
func (h *Container) getSystem(c *gin.Context) {
	if !h.requireSystems(c) {
		return
	}
	id, ok := pathID(c)
	if !ok {
		return
	}
	system, err := h.Systems.Get(c.Request.Context(), id)
	if err != nil {
		api.WriteServiceError(c, err)
		return
	}
	api.OK(c, system)
}

// createSystem stores a new machine and answers with its one time token.
func (h *Container) createSystem(c *gin.Context) {
	if !h.requireSystems(c) {
		return
	}
	var payload services.SystemInput
	if !bindJSON(c, &payload) {
		return
	}
	system, err := h.Systems.Create(c.Request.Context(), payload)
	if err != nil {
		api.WriteServiceError(c, err)
		return
	}
	api.Created(c, system)
}

// updateSystem saves the editable fields of a machine.
func (h *Container) updateSystem(c *gin.Context) {
	if !h.requireSystems(c) {
		return
	}
	id, ok := pathID(c)
	if !ok {
		return
	}
	var payload services.SystemInput
	if !bindJSON(c, &payload) {
		return
	}
	system, err := h.Systems.Update(c.Request.Context(), id, payload)
	if err != nil {
		api.WriteServiceError(c, err)
		return
	}
	api.OK(c, system)
}

// deleteSystem removes a machine and its history.
func (h *Container) deleteSystem(c *gin.Context) {
	if !h.requireSystems(c) {
		return
	}
	id, ok := pathID(c)
	if !ok {
		return
	}
	if err := h.Systems.Delete(c.Request.Context(), id); err != nil {
		api.WriteServiceError(c, err)
		return
	}
	api.NoContent(c)
}

// rotateSystemToken replaces the agent token and answers with the new one.
func (h *Container) rotateSystemToken(c *gin.Context) {
	if !h.requireSystems(c) {
		return
	}
	id, ok := pathID(c)
	if !ok {
		return
	}
	system, err := h.Systems.RotateToken(c.Request.Context(), id)
	if err != nil {
		api.WriteServiceError(c, err)
		return
	}
	api.OK(c, system)
}

// systemMetrics returns the downsampled charts of one machine.
func (h *Container) systemMetrics(c *gin.Context) {
	if !h.requireSystems(c) {
		return
	}
	id, ok := pathID(c)
	if !ok {
		return
	}
	points, err := h.Systems.Series(c.Request.Context(), id, queryInt(c, "hours", 24))
	if err != nil {
		api.WriteServiceError(c, err)
		return
	}
	api.OK(c, points)
}

// systemContainers returns the containers of one machine.
func (h *Container) systemContainers(c *gin.Context) {
	if !h.requireSystems(c) {
		return
	}
	id, ok := pathID(c)
	if !ok {
		return
	}
	rows, err := h.Systems.Containers(c.Request.Context(), id)
	if err != nil {
		api.WriteServiceError(c, err)
		return
	}
	api.OK(c, rows)
}

// containerMetrics returns the history of one container of one machine.
func (h *Container) containerMetrics(c *gin.Context) {
	if !h.requireSystems(c) {
		return
	}
	id, ok := pathID(c)
	if !ok {
		return
	}
	name := c.Param("name")
	points, err := h.Systems.ContainerSeries(c.Request.Context(), id, name, queryInt(c, "hours", 24))
	if err != nil {
		api.WriteServiceError(c, err)
		return
	}
	api.OK(c, points)
}

// allContainers returns every container of every machine (the "all containers"
// tab).
func (h *Container) allContainers(c *gin.Context) {
	if !h.requireSystems(c) {
		return
	}
	rows, err := h.Systems.AllContainers(c.Request.Context())
	if err != nil {
		api.WriteServiceError(c, err)
		return
	}
	api.OK(c, rows)
}

// allSmart returns the stored SMART reports (the S.M.A.R.T. tab).
func (h *Container) allSmart(c *gin.Context) {
	if !h.requireSystems(c) {
		return
	}
	reports, err := h.Systems.Smart(c.Request.Context(), 0)
	if err != nil {
		api.WriteServiceError(c, err)
		return
	}
	api.OK(c, reports)
}

// systemSmart returns the SMART report of one machine.
func (h *Container) systemSmart(c *gin.Context) {
	if !h.requireSystems(c) {
		return
	}
	id, ok := pathID(c)
	if !ok {
		return
	}
	reports, err := h.Systems.Smart(c.Request.Context(), id)
	if err != nil {
		api.WriteServiceError(c, err)
		return
	}
	api.OK(c, reports)
}

// requireSystemAlerts answers 503 when the alert service is not wired.
func (h *Container) requireSystemAlerts(c *gin.Context) bool {
	if h.Systems != nil && h.Systems.Alerts() != nil {
		return true
	}
	api.WriteError(c, http.StatusServiceUnavailable, i18n.CodeValidation, "hardware alerts are not enabled")
	return false
}

// getSystemAlerts returns the alert rules of one machine and the channels they
// notify.
func (h *Container) getSystemAlerts(c *gin.Context) {
	if !h.requireSystemAlerts(c) {
		return
	}
	id, ok := pathID(c)
	if !ok {
		return
	}
	view, err := h.Systems.Alerts().View(c.Request.Context(), id)
	if err != nil {
		api.WriteServiceError(c, err)
		return
	}
	api.OK(c, view)
}

// updateSystemAlerts saves the rules and the channel links of one machine.
func (h *Container) updateSystemAlerts(c *gin.Context) {
	if !h.requireSystemAlerts(c) {
		return
	}
	id, ok := pathID(c)
	if !ok {
		return
	}
	var payload services.SystemAlertsInput
	if !bindJSON(c, &payload) {
		return
	}
	view, err := h.Systems.Alerts().Save(c.Request.Context(), id, payload)
	if err != nil {
		api.WriteServiceError(c, err)
		return
	}
	api.OK(c, view)
}

// resetSystemAlerts restores the global default rules on one machine.
//
// The channel links are untouched (a reset changes how the machine is watched,
// not who is told) and the answer is the whole view, so the dialog can redraw
// itself without a second round trip.
func (h *Container) resetSystemAlerts(c *gin.Context) {
	if !h.requireSystemAlerts(c) {
		return
	}
	id, ok := pathID(c)
	if !ok {
		return
	}
	view, err := h.Systems.Alerts().ResetToDefaults(c.Request.Context(), id)
	if err != nil {
		api.WriteServiceError(c, err)
		return
	}
	api.OK(c, view)
}

// copySystemAlerts copies the rules of another machine onto one machine.
func (h *Container) copySystemAlerts(c *gin.Context) {
	if !h.requireSystemAlerts(c) {
		return
	}
	id, ok := pathID(c)
	if !ok {
		return
	}
	var payload struct {
		SourceID uint `json:"source_id"`
	}
	if !bindJSON(c, &payload) {
		return
	}
	view, err := h.Systems.Alerts().CopyFrom(c.Request.Context(), id, payload.SourceID)
	if err != nil {
		api.WriteServiceError(c, err)
		return
	}
	api.OK(c, view)
}

// refreshSystemSmart asks the owner of an agent for a fresh SMART report.
func (h *Container) refreshSystemSmart(c *gin.Context) {
	if !h.requireSystems(c) {
		return
	}
	id, ok := pathID(c)
	if !ok {
		return
	}
	if err := h.Systems.RefreshSmartNow(c.Request.Context(), id); err != nil {
		api.WriteServiceError(c, err)
		return
	}
	reports, err := h.Systems.Smart(c.Request.Context(), id)
	if err != nil {
		api.WriteServiceError(c, err)
		return
	}
	api.OK(c, reports)
}
