package handlers

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/ivancarlosti/up/internal/api"
	"github.com/ivancarlosti/up/internal/models"
	"github.com/ivancarlosti/up/internal/services"
)

// clusterStatus returns the complete cluster status (nodes, settings, key).
func (h *Container) clusterStatus(c *gin.Context) {
	status, err := h.Cluster.Status(c.Request.Context())
	if err != nil {
		api.WriteServiceError(c, err)
		return
	}
	api.OK(c, status)
}

// clusterNodes lists the registered nodes.
func (h *Container) clusterNodes(c *gin.Context) {
	nodes, err := h.Cluster.Nodes(c.Request.Context())
	if err != nil {
		api.WriteServiceError(c, err)
		return
	}
	api.OK(c, nodes)
}

// clusterJoin handles both flavors of the join endpoint (see docs/clustering.md):
//
//	primary_url present -> the dashboard of the joining node asks us to join a
//	                       primary: this requires an authenticated session
//	                       because it performs a server side HTTP call (SSRF guard);
//	primary_url absent  -> a node registers itself using the cluster private key.
func (h *Container) clusterJoin(c *gin.Context) {
	var payload services.JoinRequest
	if !bindJSON(c, &payload) {
		return
	}

	if payload.PrimaryURL != "" {
		if _, ok := h.Sessions.CurrentIdentity(c.Request.Context(), c.Request); !ok {
			api.Unauthorized(c, "authentication required to join a cluster")
			return
		}
		response, err := h.Cluster.Join(c.Request.Context(), payload)
		if err != nil {
			api.WriteServiceError(c, err)
			return
		}
		api.OK(c, response)
		return
	}

	// Node to node registration: the private key is validated inside.
	response, err := h.Cluster.RegisterNode(c.Request.Context(), payload)
	if err != nil {
		api.WriteServiceError(c, err)
		return
	}
	api.OK(c, response)
}

// clusterLeave removes a node (defaults to this node).
func (h *Container) clusterLeave(c *gin.Context) {
	var payload struct {
		NodeID string `json:"node_id"`
	}
	_ = c.ShouldBindJSON(&payload)
	status, err := h.Cluster.Leave(c.Request.Context(), payload.NodeID)
	if err != nil {
		api.WriteServiceError(c, err)
		return
	}
	api.OK(c, status)
}

// clusterGetSettings returns the behaviour rules.
func (h *Container) clusterGetSettings(c *gin.Context) {
	settings, err := h.Cluster.Settings(c.Request.Context())
	if err != nil {
		api.WriteServiceError(c, err)
		return
	}
	api.OK(c, settings)
}

// clusterUpdateSettings stores the behaviour rules.
func (h *Container) clusterUpdateSettings(c *gin.Context) {
	var payload models.ClusterSettings
	if !bindJSON(c, &payload) {
		return
	}
	settings, err := h.Cluster.UpdateSettings(c.Request.Context(), &payload)
	if err != nil {
		api.WriteServiceError(c, err)
		return
	}
	api.OK(c, settings)
}

// clusterPrivateKey reveals the shared key (admin only).
func (h *Container) clusterPrivateKey(c *gin.Context) {
	key, err := h.Cluster.PrivateKey(c.Request.Context())
	if err != nil {
		api.WriteServiceError(c, err)
		return
	}
	api.OK(c, gin.H{"private_key": key, "node_id": h.Cfg.NodeID, "node_name": h.Cfg.NodeName})
}

// clusterRegenerateKey rotates the shared key.
func (h *Container) clusterRegenerateKey(c *gin.Context) {
	key, err := h.Cluster.RegeneratePrivateKey(c.Request.Context())
	if err != nil {
		api.WriteServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"private_key": key,
		"message":     "cluster private key regenerated; every other node must join again",
	})
}

// clusterHeartbeat refreshes the liveness of this node and returns the nodes
// considered online.
func (h *Container) clusterHeartbeat(c *gin.Context) {
	ctx := c.Request.Context()
	if err := h.Cluster.Ping(ctx); err != nil {
		api.WriteServiceError(c, err)
		return
	}
	online, offline, err := h.Cluster.OnlineNodes(ctx)
	if err != nil {
		api.WriteServiceError(c, err)
		return
	}
	api.OK(c, gin.H{"online": online, "offline": offline})
}
