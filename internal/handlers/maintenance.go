package handlers

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/ivancarlosti/up/internal/api"
	"github.com/ivancarlosti/up/internal/i18n"
	"github.com/ivancarlosti/up/internal/models"
)

// heartbeatRetentionPreview describes the history table and what the current
// retention would delete. It is what Admin > Settings shows before the operator
// confirms a purge: the number of rows, their age range and the row count that
// falls on the wrong side of the cut-off.
func (h *Container) heartbeatRetentionPreview(c *gin.Context) {
	ctx := c.Request.Context()
	days := h.Settings.HeartbeatRetentionDays()
	stats, err := h.Stats.HeartbeatStats(ctx)
	if err != nil {
		api.WriteServiceError(c, err)
		return
	}
	out := gin.H{
		"retention_days": days,
		"default_days":   models.DefaultHeartbeatRetentionDays,
		"max_days":       models.MaxHeartbeatRetentionDays,
		"total":          stats.Total,
		"oldest":         stats.Oldest,
		"newest":         stats.Newest,
		"would_delete":   int64(0),
	}
	if days > 0 {
		cutoff := time.Now().UTC().AddDate(0, 0, -days)
		count, err := h.Stats.CountOlderThan(ctx, cutoff)
		if err != nil {
			api.WriteServiceError(c, err)
			return
		}
		out["cutoff"] = cutoff
		out["would_delete"] = count
	}
	api.OK(c, out)
}

// purgeHeartbeats deletes the history older than the retention. The body may
// carry a different number of days ({"days": 90}), which lets the operator
// purge deeper than the configured policy without changing it.
func (h *Container) purgeHeartbeats(c *gin.Context) {
	ctx := c.Request.Context()
	days := h.Settings.HeartbeatRetentionDays()
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
		days = models.NormalizeHeartbeatRetentionDays(*payload.Days)
	}
	if days <= 0 {
		api.WriteError(c, http.StatusBadRequest, i18n.CodeValidation,
			"retention_days must be greater than zero to purge")
		return
	}
	cutoff := time.Now().UTC().AddDate(0, 0, -days)
	deleted, err := h.Stats.Purge(ctx, cutoff)
	if err != nil {
		api.WriteServiceError(c, err)
		return
	}
	h.Log.Info("heartbeat history purged", "days", days, "deleted", deleted, "before", cutoff)
	api.OK(c, gin.H{"deleted": deleted, "days": days, "before": cutoff})
}
