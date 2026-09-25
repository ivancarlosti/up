package handlers

import (
	"github.com/gin-gonic/gin"

	"github.com/ivancarlosti/up/internal/api"
	"github.com/ivancarlosti/up/internal/models"
	"github.com/ivancarlosti/up/internal/services"
)

// bulkCreateMonitors creates one monitor per pasted row (dry run supported).
func (h *Container) bulkCreateMonitors(c *gin.Context) {
	var payload struct {
		Text       string `json:"text"`
		TemplateID uint   `json:"template_id"`
		GroupIDs   []uint `json:"group_ids"`
		Active     *bool  `json:"active"`
		DryRun     bool   `json:"dry_run"`
	}
	if !bindJSON(c, &payload) {
		return
	}
	report, createdIDs, err := h.Monitors.BulkCreate(c.Request.Context(), services.BulkOptions{
		Text:       payload.Text,
		TemplateID: payload.TemplateID,
		GroupIDs:   payload.GroupIDs,
		Active:     payload.Active,
		DryRun:     payload.DryRun,
	}, h.MonitorTemplates)
	if err != nil {
		api.WriteServiceError(c, err)
		return
	}
	for _, id := range createdIDs {
		h.scheduleUpsert(id)
	}
	api.OK(c, report)
}

// listMonitorTemplates returns every template.
func (h *Container) listMonitorTemplates(c *gin.Context) {
	templates, err := h.MonitorTemplates.List(c.Request.Context())
	if err != nil {
		api.WriteServiceError(c, err)
		return
	}
	api.OK(c, templates)
}

// getMonitorTemplate returns a single template.
func (h *Container) getMonitorTemplate(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	template, err := h.MonitorTemplates.Get(c.Request.Context(), id)
	if err != nil {
		api.WriteServiceError(c, err)
		return
	}
	api.OK(c, template)
}

// createMonitorTemplate stores a new template.
func (h *Container) createMonitorTemplate(c *gin.Context) {
	var template models.MonitorTemplate
	if !bindJSON(c, &template) {
		return
	}
	if err := h.MonitorTemplates.Create(c.Request.Context(), &template); err != nil {
		api.WriteServiceError(c, err)
		return
	}
	api.Created(c, template)
}

// updateMonitorTemplate saves an existing template.
func (h *Container) updateMonitorTemplate(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	var template models.MonitorTemplate
	if !bindJSON(c, &template) {
		return
	}
	template.ID = id
	if err := h.MonitorTemplates.Update(c.Request.Context(), &template); err != nil {
		api.WriteServiceError(c, err)
		return
	}
	api.OK(c, template)
}

// deleteMonitorTemplate removes a template (the monitors stay).
func (h *Container) deleteMonitorTemplate(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	if err := h.MonitorTemplates.Delete(c.Request.Context(), id); err != nil {
		api.WriteServiceError(c, err)
		return
	}
	api.NoContent(c)
}

// applyMonitorTemplate applies a template to a set of monitors (bulk edit).
func (h *Container) applyMonitorTemplate(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	var payload struct {
		MonitorIDs []uint   `json:"monitor_ids"`
		Fields     []string `json:"fields"`
		DryRun     bool     `json:"dry_run"`
	}
	if !bindJSON(c, &payload) {
		return
	}
	results, err := h.MonitorTemplates.Apply(c.Request.Context(), services.ApplyOptions{
		TemplateID: id,
		MonitorIDs: payload.MonitorIDs,
		Fields:     payload.Fields,
		DryRun:     payload.DryRun,
	})
	if err != nil {
		api.WriteServiceError(c, err)
		return
	}
	// Every applied monitor must be reconfigured here and not only by the
	// periodic reconciliation.
	if !payload.DryRun {
		for _, result := range results {
			if result.Applied {
				h.scheduleUpsert(result.MonitorID)
			}
		}
	}
	api.OK(c, gin.H{"dry_run": payload.DryRun, "results": results})
}

// linkAllMonitorTemplate attaches the monitors of the template type (or of the
// selected groups) to the template and applies its defaults to them. dry_run
// returns the same counts without writing, which is what the confirmation
// dialog shows.
func (h *Container) linkAllMonitorTemplate(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	var payload struct {
		GroupIDs []uint `json:"group_ids"`
		DryRun   bool   `json:"dry_run"`
	}
	// A wet run may be sent with an empty body.
	if c.Request.ContentLength > 0 {
		if !bindJSON(c, &payload) {
			return
		}
	}
	result, err := h.MonitorTemplates.LinkAll(c.Request.Context(), services.LinkOptions{
		TemplateID: id,
		GroupIDs:   payload.GroupIDs,
		DryRun:     payload.DryRun,
	})
	if err != nil {
		api.WriteServiceError(c, err)
		return
	}
	if !payload.DryRun {
		// The linked monitors may have a new interval or probe configuration.
		h.scheduleReload()
	}
	api.OK(c, result)
}
