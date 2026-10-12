package handlers

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/ivancarlosti/up/internal/api"
	"github.com/ivancarlosti/up/internal/i18n"
	"github.com/ivancarlosti/up/internal/models"
)

// maxLighthouseHistoryLimit bounds the ?limit= of the history endpoint: a chart
// never needs more points than that, and the endpoint must not become a way to
// download the whole table of a fleet.
const maxLighthouseHistoryLimit = 1000

// lighthouseSettings returns the web performance job configuration, its
// schedule and what the two tables hold.
func (h *Container) lighthouseSettings(c *gin.Context) {
	if h.Lighthouse == nil {
		api.WriteError(c, http.StatusServiceUnavailable, i18n.CodeInternal, "the lighthouse job is not wired")
		return
	}
	ctx := c.Request.Context()
	settings := h.Lighthouse.Settings()
	stats, err := h.Lighthouse.Stats(ctx)
	if err != nil {
		api.WriteServiceError(c, err)
		return
	}
	api.OK(c, gin.H{
		"settings":          settings,
		"defaults":          models.DefaultLighthouseSettings(),
		"strategies":        models.LighthouseStrategies,
		"intervals":         models.LighthouseIntervals,
		"weekdays":          models.LighthouseWeekdays,
		"last_run_day":      h.Settings.LighthouseLastRunDay(),
		"next_run":          h.Lighthouse.NextRun(time.Now().UTC()),
		"retention_days":    h.Lighthouse.RetentionDays(),
		"retention_max":     models.MaxLighthouseRetentionDays,
		"retention_default": models.DefaultLighthouseRetentionDays,
		"stats":             stats,
	})
}

// updateLighthouseSettings stores the job configuration (and the retention).
func (h *Container) updateLighthouseSettings(c *gin.Context) {
	if h.Lighthouse == nil {
		api.WriteError(c, http.StatusServiceUnavailable, i18n.CodeInternal, "the lighthouse job is not wired")
		return
	}
	var payload struct {
		models.LighthouseSettings
		// RetentionDays is optional: the settings page always sends it, an API
		// client that only changes the clock does not have to.
		RetentionDays *int `json:"retention_days"`
	}
	if !bindJSON(c, &payload) {
		return
	}
	if problem := payload.LighthouseSettings.Validate(); problem != "" {
		api.WriteError(c, http.StatusBadRequest, i18n.CodeValidation, problem)
		return
	}
	ctx := c.Request.Context()
	if err := h.Settings.SetLighthouseSettings(ctx, payload.LighthouseSettings); err != nil {
		api.WriteServiceError(c, err)
		return
	}
	if payload.RetentionDays != nil {
		if err := h.Settings.SetLighthouseRetentionDays(ctx, *payload.RetentionDays); err != nil {
			api.WriteServiceError(c, err)
			return
		}
	}
	api.OK(c, h.Lighthouse.Settings())
}

// runLighthouseNow measures every deduplicated target without waiting for the
// daily schedule.
func (h *Container) runLighthouseNow(c *gin.Context) {
	if h.Lighthouse == nil {
		api.WriteError(c, http.StatusServiceUnavailable, i18n.CodeInternal, "the lighthouse job is not wired")
		return
	}
	if err := h.Lighthouse.RunNow(c.Request.Context()); err != nil {
		api.WriteServiceError(c, err)
		return
	}
	api.OK(c, gin.H{"ran": true, "at": time.Now().UTC()})
}

// lighthouseMonitors lists the monitors that measure their page, with their last
// observation: it is the body of the Web performance page.
func (h *Container) lighthouseMonitors(c *gin.Context) {
	if h.Lighthouse == nil {
		api.WriteError(c, http.StatusServiceUnavailable, i18n.CodeInternal, "the lighthouse job is not wired")
		return
	}
	ctx := c.Request.Context()
	monitors, err := h.Lighthouse.Monitors(ctx)
	if err != nil {
		api.WriteServiceError(c, err)
		return
	}
	stats, err := h.Lighthouse.Stats(ctx)
	if err != nil {
		api.WriteServiceError(c, err)
		return
	}
	api.OK(c, gin.H{
		"monitors": monitors,
		"count":    len(monitors),
		"stats":    stats,
		// interval_default is the cadence of a monitor that carries none (a row
		// written before the field existed): the page uses it to render the
		// effective cadence of such a monitor, which the backend already applies.
		"interval_default": h.Lighthouse.Settings().Schedule().Interval,
		"retention_days":   h.Lighthouse.RetentionDays(),
		"next_run":         h.Lighthouse.NextRun(time.Now().UTC()),
	})
}

// runLighthouseMonitor measures a single monitor now ("check now" for one page).
func (h *Container) runLighthouseMonitor(c *gin.Context) {
	if h.Lighthouse == nil {
		api.WriteError(c, http.StatusServiceUnavailable, i18n.CodeInternal, "the lighthouse job is not wired")
		return
	}
	id, ok := pathID(c)
	if !ok {
		return
	}
	ctx := c.Request.Context()
	if err := h.Lighthouse.Refresh(ctx, id); err != nil {
		api.WriteServiceError(c, err)
		return
	}
	stored, err := h.Lighthouse.Get(ctx, id)
	if err != nil {
		api.WriteServiceError(c, err)
		return
	}
	if stored == nil {
		api.OK(c, gin.H{"ran": true, "at": time.Now().UTC()})
		return
	}
	api.OK(c, gin.H{"ran": true, "at": time.Now().UTC(), "result": stored.Info()})
}

// lighthouseMonitorHistory returns the runs of one monitor (the chart data).
// An optional ?limit=N keeps only the N newest runs, which is what the chart asks
// for: an hourly monitor collects a day of points in a day, so the dialog shows
// the last few hundred of them instead of every observation ever stored.
func (h *Container) lighthouseMonitorHistory(c *gin.Context) {
	if h.Lighthouse == nil {
		api.WriteError(c, http.StatusServiceUnavailable, i18n.CodeInternal, "the lighthouse job is not wired")
		return
	}
	id, ok := pathID(c)
	if !ok {
		return
	}
	runs, err := h.Lighthouse.RunHistory(c.Request.Context(), id, lighthouseHistoryLimit(c))
	if err != nil {
		api.WriteServiceError(c, err)
		return
	}
	api.OK(c, gin.H{"monitor_id": id, "runs": runs, "count": len(runs)})
}

// lighthouseHistoryLimit reads the optional ?limit= of the history endpoint.
//
// A missing, unreadable or negative value means "no limit" (the documented
// default of the endpoint), a value above maxLighthouseHistoryLimit is clamped:
// the chart is the only client and it never needs more.
func lighthouseHistoryLimit(c *gin.Context) int {
	raw := strings.TrimSpace(c.Query("limit"))
	if raw == "" {
		return 0
	}
	limit, err := strconv.Atoi(raw)
	if err != nil || limit < 0 {
		return 0
	}
	if limit > maxLighthouseHistoryLimit {
		return maxLighthouseHistoryLimit
	}
	return limit
}

// purgeLighthouse deletes the run history older than the retention (or older
// than the days in the body, which lets the operator purge deeper without
// changing the policy).
func (h *Container) purgeLighthouse(c *gin.Context) {
	if h.Lighthouse == nil {
		api.WriteError(c, http.StatusServiceUnavailable, i18n.CodeInternal, "the lighthouse job is not wired")
		return
	}
	days := h.Lighthouse.RetentionDays()
	var payload struct {
		Days *int `json:"days"`
	}
	// An empty body is valid: it applies the configured retention.
	if c.Request.ContentLength > 0 {
		if !bindJSON(c, &payload) {
			return
		}
	}
	if payload.Days != nil {
		days = models.NormalizeLighthouseRetentionDays(*payload.Days)
	}
	if days <= 0 {
		api.WriteError(c, http.StatusBadRequest, i18n.CodeValidation,
			"retention_days must be greater than zero to purge")
		return
	}
	before := time.Now().UTC().AddDate(0, 0, -days)
	deleted, err := h.Lighthouse.PurgeBefore(c.Request.Context(), before)
	if err != nil {
		api.WriteServiceError(c, err)
		return
	}
	h.Log.Info("lighthouse history purged", "days", days, "deleted", deleted, "before", before)
	api.OK(c, gin.H{"deleted": deleted, "days": days, "before": before})
}
