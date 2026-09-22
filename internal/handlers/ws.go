package handlers

import (
	"github.com/gin-gonic/gin"
)

// websocket upgrades the request to a WebSocket connection. The route lives
// inside the authenticated group, so AUTH_METHOD=none keeps it open while the
// other modes require a valid session cookie.
func (h *Container) websocket(c *gin.Context) {
	if h.Hub == nil {
		c.JSON(503, gin.H{"code": "ERR_INTERNAL", "message": "real time updates are disabled"})
		return
	}
	h.Hub.Handle(c.Writer, c.Request)
}
