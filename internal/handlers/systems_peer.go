package handlers

import (
	"github.com/gin-gonic/gin"

	"github.com/ivancarlosti/up/internal/api"
)

// syncSystemHistory answers GET /api/cluster/sync/systems/history: the samples
// of one system this node owns, for a peer that does not own it.
//
// It is a signed peer route like the rest of /api/cluster/sync, and it answers
// only for a system this node actually has: a peer that asks the wrong node gets
// a clear 404 instead of an empty chart that looks like a machine that stopped
// reporting.
func (h *Container) syncSystemHistory(c *gin.Context) {
	if h.Systems == nil {
		api.NotFound(c, "ERR_SYSTEM_NOT_FOUND", "hardware monitoring is not enabled on this node")
		return
	}
	uuid := queryString(c, "uuid")
	if uuid == "" {
		api.BadRequest(c, "uuid is required")
		return
	}
	response, err := h.Systems.ServeHistory(c.Request.Context(), uuid, queryInt(c, "hours", 24))
	if err != nil {
		api.WriteServiceError(c, err)
		return
	}
	api.OK(c, response)
}
