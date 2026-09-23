// Package middleware provides the Gin middlewares of Up: request logging,
// CORS, security headers, rate limiting, IP rules and authentication.
package middleware

import (
	"strings"

	"github.com/gin-gonic/gin"
)

// Context keys used to expose request scoped values to the handlers.
const (
	CtxClientIP  = "up_client_ip"
	CtxIdentity  = "up_identity"
	CtxToken     = "up_api_token"
	CtxRequestID = "up_request_id"
	// CtxPeerNode is the authenticated NODE_ID of an inbound node to node
	// request (set by RequirePeerKey).
	CtxPeerNode = "up_peer_node"
)

// ClientIP returns the resolved client address of the request.
func ClientIP(c *gin.Context) string {
	if value, ok := c.Get(CtxClientIP); ok {
		if ip, ok := value.(string); ok {
			return ip
		}
	}
	return c.ClientIP()
}

// PeerNode returns the authenticated peer identity of an inbound node to node
// request, or "" when the route is not protected by RequirePeerKey.
func PeerNode(c *gin.Context) string {
	if value, ok := c.Get(CtxPeerNode); ok {
		if node, ok := value.(string); ok {
			return node
		}
	}
	return ""
}

// bearerToken extracts the token from the Authorization header.
func bearerToken(c *gin.Context) string {
	header := c.GetHeader("Authorization")
	if header == "" {
		return ""
	}
	parts := strings.SplitN(header, " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return ""
	}
	return strings.TrimSpace(parts[1])
}
