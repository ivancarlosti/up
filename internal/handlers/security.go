package handlers

import (
	"time"

	"github.com/gin-gonic/gin"

	"github.com/ivancarlosti/up/internal/api"
	"github.com/ivancarlosti/up/internal/middleware"
	"github.com/ivancarlosti/up/internal/models"
)

// ---------------------------------------------------------------------------
// API tokens
// ---------------------------------------------------------------------------

// listTokens returns every API token (without the secrets).
func (h *Container) listTokens(c *gin.Context) {
	tokens, err := h.Tokens.List(c.Request.Context())
	if err != nil {
		api.WriteServiceError(c, err)
		return
	}
	api.OK(c, tokens)
}

// createToken generates a token; the plain value is returned only once.
func (h *Container) createToken(c *gin.Context) {
	var payload struct {
		Name      string   `json:"name"`
		Scopes    []string `json:"scopes"`
		ExpiresAt string   `json:"expires_at"`
		ExpiresIn int      `json:"expires_in_days"`
	}
	if !bindJSON(c, &payload) {
		return
	}
	var expiresAt *time.Time
	if payload.ExpiresAt != "" {
		parsed, err := time.Parse(time.RFC3339, payload.ExpiresAt)
		if err != nil {
			api.BadRequest(c, "expires_at must be an RFC3339 timestamp")
			return
		}
		expiresAt = &parsed
	} else if payload.ExpiresIn > 0 {
		value := time.Now().UTC().AddDate(0, 0, payload.ExpiresIn)
		expiresAt = &value
	}
	token, err := h.Tokens.Create(c.Request.Context(), payload.Name, payload.Scopes, expiresAt)
	if err != nil {
		api.WriteServiceError(c, err)
		return
	}
	api.Created(c, token)
}

// updateToken changes the metadata of a token.
func (h *Container) updateToken(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	var payload struct {
		Name      string   `json:"name"`
		Scopes    []string `json:"scopes"`
		ExpiresAt string   `json:"expires_at"`
	}
	if !bindJSON(c, &payload) {
		return
	}
	var expiresAt *time.Time
	if payload.ExpiresAt != "" {
		parsed, err := time.Parse(time.RFC3339, payload.ExpiresAt)
		if err != nil {
			api.BadRequest(c, "expires_at must be an RFC3339 timestamp")
			return
		}
		expiresAt = &parsed
	}
	token, err := h.Tokens.Update(c.Request.Context(), id, payload.Name, payload.Scopes, expiresAt)
	if err != nil {
		api.WriteServiceError(c, err)
		return
	}
	api.OK(c, token)
}

// revokeToken invalidates a token.
func (h *Container) revokeToken(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	if err := h.Tokens.Revoke(c.Request.Context(), id); err != nil {
		api.WriteServiceError(c, err)
		return
	}
	api.NoContent(c)
}

// deleteToken removes a token permanently.
func (h *Container) deleteToken(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	if err := h.Tokens.Delete(c.Request.Context(), id); err != nil {
		api.WriteServiceError(c, err)
		return
	}
	api.NoContent(c)
}

// ---------------------------------------------------------------------------
// IP rules
// ---------------------------------------------------------------------------

// listIPRules returns the allow/deny list.
func (h *Container) listIPRules(c *gin.Context) {
	rules, err := h.IPRules.List(c.Request.Context())
	if err != nil {
		api.WriteServiceError(c, err)
		return
	}
	api.OK(c, gin.H{
		"rules":       rules,
		"client_ip":   middleware.ClientIP(c),
		"bypassed":    h.Cfg.SecurityBypassIPRules,
		"scopes":      models.AllIPRuleScopes(),
		"sample_cidr": "203.0.113.0/24",
	})
}

// createIPRule stores a new rule.
func (h *Container) createIPRule(c *gin.Context) {
	var rule models.IPRule
	if !bindJSON(c, &rule) {
		return
	}
	if err := h.IPRules.Create(c.Request.Context(), &rule); err != nil {
		api.WriteServiceError(c, err)
		return
	}
	api.Created(c, rule)
}

// updateIPRule saves an existing rule.
func (h *Container) updateIPRule(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	var rule models.IPRule
	if !bindJSON(c, &rule) {
		return
	}
	rule.ID = id
	if err := h.IPRules.Update(c.Request.Context(), &rule); err != nil {
		api.WriteServiceError(c, err)
		return
	}
	api.OK(c, rule)
}

// deleteIPRule removes a rule.
func (h *Container) deleteIPRule(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	if err := h.IPRules.Delete(c.Request.Context(), id); err != nil {
		api.WriteServiceError(c, err)
		return
	}
	api.NoContent(c)
}
