package handlers

import (
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/ivancarlosti/up/internal/api"
	"github.com/ivancarlosti/up/internal/i18n"
	"github.com/ivancarlosti/up/internal/middleware"
)

// subscribeStatusPage starts the double opt-in of an address. The answer is
// intentionally the same for a new, a pending and an existing subscription.
func (h *Container) subscribeStatusPage(c *gin.Context) {
	var payload struct {
		Email          string `json:"email"`
		Locale         string `json:"locale"`
		RecaptchaToken string `json:"recaptcha_token"`
	}
	if !bindJSON(c, &payload) {
		return
	}
	if h.Settings.RecaptchaEnabled() {
		if strings.TrimSpace(payload.RecaptchaToken) == "" {
			api.WriteError(c, http.StatusForbidden, i18n.CodeAuthCaptchaMissing,
				"the captcha token is required")
			return
		}
		if err := h.verifyRecaptcha(c.Request.Context(), payload.RecaptchaToken, middleware.ClientIP(c)); err != nil {
			api.WriteError(c, http.StatusForbidden, i18n.CodeAuthCaptchaFailed, err.Error())
			return
		}
	}
	if err := h.StatusPageSubscriptions.Subscribe(c.Request.Context(), c.Param("slug"), payload.Email, payload.Locale); err != nil {
		api.WriteServiceError(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	api.OK(c, gin.H{"ok": true})
}

// confirmStatusPageSubscription consumes the confirmation link. The browser is
// sent back to the SPA with an outcome, so the token never reaches the frontend.
func (h *Container) confirmStatusPageSubscription(c *gin.Context) {
	slug := c.Param("slug")
	if err := h.StatusPageSubscriptions.Confirm(c.Request.Context(), slug, c.Query("token")); err != nil {
		h.Log.Warn("status page subscription confirmation rejected",
			"slug", slug, "ip", middleware.ClientIP(c), "error", err)
		redirectSubscriptionOutcome(c, slug, "invalid")
		return
	}
	redirectSubscriptionOutcome(c, slug, "confirmed")
}

// unsubscribeStatusPageSubscription consumes the unsubscribe link.
func (h *Container) unsubscribeStatusPageSubscription(c *gin.Context) {
	slug := c.Param("slug")
	if err := h.StatusPageSubscriptions.Unsubscribe(c.Request.Context(), slug, c.Query("token")); err != nil {
		h.Log.Warn("status page unsubscribe rejected",
			"slug", slug, "ip", middleware.ClientIP(c), "error", err)
		redirectSubscriptionOutcome(c, slug, "invalid")
		return
	}
	redirectSubscriptionOutcome(c, slug, "unsubscribed")
}

// redirectSubscriptionOutcome returns to the public page with a short status.
// A relative location works behind a reverse proxy and under APP_URL alike.
func redirectSubscriptionOutcome(c *gin.Context, slug, outcome string) {
	c.Header("Cache-Control", "no-store")
	c.Redirect(http.StatusFound,
		"/status/"+url.PathEscape(slug)+"?subscription="+url.QueryEscape(outcome))
}

// listStatusPageSubscribers returns one page of the admin subscriber listing.
func (h *Container) listStatusPageSubscribers(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	if _, err := h.StatusPages.Get(c.Request.Context(), id); err != nil {
		api.WriteServiceError(c, err)
		return
	}
	result, err := h.StatusPageSubscriptions.List(c.Request.Context(), id,
		queryString(c, "q"), queryInt(c, "page", 1), queryInt(c, "size", 25))
	if err != nil {
		api.WriteServiceError(c, err)
		return
	}
	api.OK(c, result)
}

// deleteStatusPageSubscriber removes one subscriber (the explicit admin action).
func (h *Container) deleteStatusPageSubscriber(c *gin.Context) {
	pageID, ok := pathID(c)
	if !ok {
		return
	}
	subscriberID, ok := pathParamID(c, "subscriber_id")
	if !ok {
		return
	}
	if err := h.StatusPageSubscriptions.Delete(c.Request.Context(), pageID, subscriberID); err != nil {
		api.WriteServiceError(c, err)
		return
	}
	api.NoContent(c)
}

// pathParamID parses a numeric path parameter other than :id.
func pathParamID(c *gin.Context, param string) (uint, bool) {
	raw := c.Param(param)
	value, err := strconv.ParseUint(raw, 10, 64)
	if err != nil || value == 0 || value > math.MaxUint32 {
		api.WriteError(c, http.StatusBadRequest, i18n.CodeValidation, "invalid "+param+" "+raw)
		return 0, false
	}
	return uint(value), true
}
