package handlers

import (
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/ivancarlosti/up/internal/api"
	"github.com/ivancarlosti/up/internal/models"
	"github.com/ivancarlosti/up/internal/services"
	"github.com/ivancarlosti/up/internal/version"
)

// processStartedAt is used to report how long this node has been running. A peer
// that just restarted is a peer whose view of the world is fresh, which is worth
// knowing when a sync looks odd.
var processStartedAt = time.Now().UTC()

// syncPing answers a signed peer request (GET /api/cluster/sync/ping).
//
// The middleware has already authenticated the caller, so the handler only
// reports who this node is and which protocol it speaks. The payload is
// deliberately small: the peer uses it for liveness, for the settle time and to
// detect a protocol or mode mismatch.
func (h *Container) syncPing(c *gin.Context) {
	api.OK(c, services.PingResponse{
		NodeID:          h.Cfg.NodeID,
		NodeName:        h.Cfg.NodeName,
		APIURL:          h.Cfg.AppURL,
		Version:         version.Readable(),
		ProtocolVersion: models.ProtocolVersion,
		Mode:            h.Cfg.ClusterMode,
		ClusterEnabled:  h.Cfg.ClusterEnabled,
		UptimeSeconds:   int64(time.Since(processStartedAt).Seconds()),
		ServerTime:      time.Now().UTC(),
	})
}

// syncVotes answers a signed peer request (GET /api/cluster/sync/votes) with THIS
// node's own verdicts per monitor.
//
// It carries only what this node measured: the receiver merges it with its own
// heartbeats and with the other peers' verdicts, which is what keeps the failure
// strategies meaningful without a shared heartbeats table.
func (h *Container) syncVotes(c *gin.Context) {
	response, err := h.Sync.ServeVotes(c.Request.Context())
	if err != nil {
		api.WriteServiceError(c, err)
		return
	}
	api.OK(c, response)
}

// syncManifest answers a signed peer request with this node's entity checksums.
func (h *Container) syncManifest(c *gin.Context) {
	response, err := h.Sync.ServeManifest(c.Request.Context())
	if err != nil {
		api.WriteServiceError(c, err)
		return
	}
	api.OK(c, response)
}

// syncSnapshot answers a signed peer request with one page of an entity.
func (h *Container) syncSnapshot(c *gin.Context) {
	page, _ := strconv.Atoi(strings.TrimSpace(c.Query("page")))
	size, _ := strconv.Atoi(strings.TrimSpace(c.Query("size")))

	response, err := h.Sync.ServeSnapshot(c.Request.Context(), strings.TrimSpace(c.Query("entity")), page, size)
	if err != nil {
		api.WriteServiceError(c, err)
		return
	}
	api.OK(c, response)
}

// syncChanges answers a signed peer request: the outbox rows the caller has not
// seen yet (GET /api/cluster/sync/changes?since=&limit=).
func (h *Container) syncChanges(c *gin.Context) {
	since, _ := strconv.ParseInt(strings.TrimSpace(c.Query("since")), 10, 64)
	limit, _ := strconv.Atoi(strings.TrimSpace(c.Query("limit")))

	response, err := h.Sync.ServeChanges(c.Request.Context(), since, limit)
	if err != nil {
		api.WriteServiceError(c, err)
		return
	}
	api.OK(c, response)
}

// syncStatus is the local admin view of the peer table
// (GET /api/cluster/sync/status, session authenticated).
//
// It exists because in federated mode the dashboard shows a per-node view: a
// stalled peer or a rejected signature is otherwise invisible, and the mode
// becomes undebuggable.
func (h *Container) syncStatus(c *gin.Context) {
	report, err := h.Cluster.PeerStatusReport(c.Request.Context())
	if err != nil {
		api.WriteServiceError(c, err)
		return
	}
	api.OK(c, report)
}
