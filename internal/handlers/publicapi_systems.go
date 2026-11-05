package handlers

import (
	"github.com/gin-gonic/gin"

	"github.com/ivancarlosti/up/internal/api"
	"github.com/ivancarlosti/up/internal/models"
	"github.com/ivancarlosti/up/internal/services"
)

// publicSystem returns a copy of a system with the fields that belong to the
// dashboard removed.
//
// The public API mirrors the decorated row the dashboard renders (status,
// uptime, owner node, the identity the agent reported), but three things stay
// inside the dashboard: the alert rules (operator tuning, latch included), the
// one time token and install line of a create, and the agent KEY. It is the
// same idea as redactMonitor (publicapi.go), applied to a payload that carries
// no credential of its own. See docs/public-api.md.
func publicSystem(view services.SystemView) services.SystemView {
	view.Alerts = nil
	view.Token = ""
	view.InstallCommand = ""
	view.AgentKey = ""
	return view
}

// apiListSystems lists the hardware monitored machines (read scope).
func (h *Container) apiListSystems(c *gin.Context) {
	if !h.requireSystems(c) {
		return
	}
	systems, err := h.Systems.List(c.Request.Context())
	if err != nil {
		api.WriteServiceError(c, err)
		return
	}
	for i := range systems {
		systems[i] = publicSystem(systems[i])
	}
	api.OK(c, systems)
}

// apiSystemsOverview returns the most recent sample of every machine: the
// numbers the cards of the listing render under each name.
//
// Like the cards, it reads this node's tables: in federated mode the samples of
// a machine another node owns are not here, and the documentation says so.
func (h *Container) apiSystemsOverview(c *gin.Context) {
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

// apiGetSystem returns one machine without its history (read scope).
func (h *Container) apiGetSystem(c *gin.Context) {
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
	api.OK(c, publicSystem(*system))
}

// systemHistory reads the detail payload of one machine, the single source of
// the four endpoints below (docs/public-api.md).
//
// History is cluster aware: in federated mode a machine another node owns is
// answered from that node over the peer API, so a client that is not on the
// owner never receives an empty chart that looks like a machine that stopped
// reporting. The requested window is clamped by the service to one of the five
// hardware windows (1, 6, 24, 168 or 720 hours).
func (h *Container) systemHistory(c *gin.Context) (*models.SystemHistory, bool) {
	if !h.requireSystems(c) {
		return nil, false
	}
	id, ok := pathID(c)
	if !ok {
		return nil, false
	}
	history, err := h.Systems.History(c.Request.Context(), id, queryInt(c, "hours", 24))
	if err != nil {
		api.WriteServiceError(c, err)
		return nil, false
	}
	return history, true
}

// apiSystemHistory answers the detail page of one machine: the row, the charts,
// the containers and the SMART report in one response.
func (h *Container) apiSystemHistory(c *gin.Context) {
	history, ok := h.systemHistory(c)
	if !ok {
		return
	}
	api.OK(c, history)
}

// apiSystemMetrics returns only the charts of one machine.
func (h *Container) apiSystemMetrics(c *gin.Context) {
	history, ok := h.systemHistory(c)
	if !ok {
		return
	}
	api.OK(c, history.Metrics)
}

// apiSystemContainers returns the containers of one machine.
func (h *Container) apiSystemContainers(c *gin.Context) {
	history, ok := h.systemHistory(c)
	if !ok {
		return
	}
	api.OK(c, history.Containers)
}

// apiSystemSmart returns the stored SMART report of one machine.
//
// `smart` is the raw disks array the agent reported (null until the first
// report), `smart_complete` whether every disk answered and `smart_at` when the
// report was collected.
func (h *Container) apiSystemSmart(c *gin.Context) {
	history, ok := h.systemHistory(c)
	if !ok {
		return
	}
	api.OK(c, gin.H{
		"smart":          history.Smart,
		"smart_complete": history.SmartOK,
		"smart_at":       history.SmartAt,
	})
}
