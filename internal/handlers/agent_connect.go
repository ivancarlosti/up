package handlers

import (
	"context"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"

	"github.com/ivancarlosti/up/internal/api"
	"github.com/ivancarlosti/up/internal/beszel"
	"github.com/ivancarlosti/up/internal/i18n"
)

// agentConnect is the endpoint the Beszel agent dials
// (docs/hardware-monitoring.md).
//
// The agent is the client: it opens a WebSocket, presents its token in X-Token and
// its version in X-Beszel, and then waits for the hub to authenticate itself with a
// signature over that token. Nothing about the connection is symmetric with the
// rest of the API, so it has its own handler, its own (absent) IP filter and its
// own upgrade.
//
// The upgrade happens BEFORE the credential is checked: an agent that answers a
// plain 401 shows the operator "unexpected status" and nothing else, while a
// WebSocket close with a reason ends up verbatim in its log ("invalid token"),
// which is what someone debugging a new machine actually needs.
func (h *Container) agentConnect(c *gin.Context) {
	if h.Systems == nil {
		c.String(http.StatusServiceUnavailable, "hardware monitoring is not enabled")
		return
	}
	token := c.GetHeader("X-Token")
	agentVersion := c.GetHeader("X-Beszel")

	upgrader := websocket.Upgrader{
		ReadBufferSize:  4096,
		WriteBufferSize: 4096,
		// The agent is not a browser: there is no Origin to check, and the token
		// is the only authenticator. A CheckOrigin that rejected it would lock out
		// every agent behind a proxy that forwards the dashboard Origin.
		CheckOrigin: func(*http.Request) bool { return true },
	}
	ws, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		// Upgrade already wrote the HTTP error.
		h.Log.Debug("agent websocket upgrade failed", "error", err, "remote", c.ClientIP())
		return
	}
	conn := beszel.NewConn(ws, h.Log)
	defer conn.Close("connection finished")

	connCtx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
	defer cancel()

	system, err := h.Systems.Authenticate(connCtx, token)
	if err != nil {
		h.Log.Warn("agent refused", "remote", c.ClientIP(), "reason", err)
		conn.Close("invalid token")
		return
	}
	if _, err := beszel.Supported(agentVersion); err != nil {
		h.Log.Warn("agent refused: unsupported version",
			"system", system.Name, "agent_version", agentVersion, "minimum", beszel.MinimumAgentVersion)
		conn.Close("agent " + agentVersion + " is older than " + beszel.MinimumAgentVersion)
		return
	}

	signer, err := h.Systems.Signer(connCtx)
	if err != nil {
		h.Log.Error("could not read the host key of the agents", "error", err)
		conn.Close("the server has no host key")
		return
	}
	message, err := conn.Fingerprint(connCtx, token, signer)
	if err != nil {
		h.Log.Warn("agent handshake failed", "system", system.Name, "error", err)
		conn.Close("handshake failed: " + err.Error())
		return
	}
	if err := h.Systems.BindFingerprint(connCtx, system, message.Fingerprint, message.Hostname, agentVersion); err != nil {
		h.Log.Warn("agent fingerprint refused", "system", system.Name, "error", err)
		conn.Close("fingerprint refused")
		return
	}
	// The identity may have changed with the bind above (the first connection of a
	// machine stores its hostname), so the session starts from a fresh row.
	bound, err := h.Systems.Get(connCtx, system.ID)
	if err != nil {
		conn.Close("the system disappeared")
		return
	}
	h.Systems.Attach(connCtx, &bound.System, conn)
	// Attach took ownership of the connection: this handler must not close it, so
	// the deferred Close is replaced by waiting for the session to end (which is
	// what keeps the request alive without pinning a goroutine per agent).
	<-conn.Done()
	api.NoContent(c)
}

// systemHostKey returns the KEY value an operator pastes into the agent.
func (h *Container) systemHostKey(c *gin.Context) {
	if h.Systems == nil {
		api.WriteError(c, http.StatusServiceUnavailable, i18n.CodeValidation, "hardware monitoring is not wired")
		return
	}
	info, err := h.Systems.HostKeyInfo(c.Request.Context())
	if err != nil {
		api.WriteServiceError(c, err)
		return
	}
	api.OK(c, info)
}
