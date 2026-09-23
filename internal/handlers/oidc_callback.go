package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"golang.org/x/oauth2"

	"github.com/ivancarlosti/up/internal/config"
	"github.com/ivancarlosti/up/internal/i18n"
	"github.com/ivancarlosti/up/internal/middleware"
	"github.com/ivancarlosti/up/internal/services"
)

// oidcCallback exchanges the authorization code, verifies the ID token and
// applies the KEYCLOAK_ACCOUNTS allow list.
func (h *Container) oidcCallback(c *gin.Context) {
	if h.OIDC == nil {
		h.renderOIDCError(c, http.StatusForbidden, i18n.CodeAuthMethodDisabled,
			"OIDC login is only available when AUTH_METHOD=keycloak")
		return
	}
	ctx := c.Request.Context()

	cookie, err := c.Cookie(oidcStateCookie)
	if err != nil || cookie == "" {
		h.renderOIDCError(c, http.StatusBadRequest, i18n.CodeAuthStateInvalid,
			"the OIDC state cookie is missing; start the login again")
		return
	}
	raw, ok := h.Sessions.VerifyState(ctx, cookie)
	if !ok {
		h.renderOIDCError(c, http.StatusBadRequest, i18n.CodeAuthStateInvalid,
			"the OIDC state is invalid or expired; start the login again")
		return
	}
	var state statePayload
	if err := json.Unmarshal([]byte(raw), &state); err != nil {
		h.renderOIDCError(c, http.StatusBadRequest, i18n.CodeAuthStateInvalid, "malformed OIDC state")
		return
	}
	// The cookie can only be used once.
	h.clearOIDCState(c)

	if returned := c.Query("state"); returned == "" || returned != state.State {
		h.renderOIDCError(c, http.StatusBadRequest, i18n.CodeAuthStateInvalid, "the OIDC state does not match")
		return
	}
	if providerError := c.Query("error"); providerError != "" {
		h.renderOIDCError(c, http.StatusForbidden, i18n.CodeAuthProviderError,
			providerError+": "+c.Query("error_description"))
		return
	}
	code := c.Query("code")
	if code == "" {
		h.renderOIDCError(c, http.StatusBadRequest, i18n.CodeAuthProviderError, "the authorization code is missing")
		return
	}

	token, err := h.OIDC.oauth.Exchange(ctx, code, oauth2.VerifierOption(state.Verifier))
	if err != nil {
		h.renderOIDCError(c, http.StatusForbidden, i18n.CodeAuthProviderError,
			"the token exchange failed: "+err.Error())
		return
	}
	rawIDToken, ok := token.Extra("id_token").(string)
	if !ok || rawIDToken == "" {
		h.renderOIDCError(c, http.StatusForbidden, i18n.CodeAuthProviderError, "the provider returned no id_token")
		return
	}
	idToken, err := h.OIDC.verifier.Verify(ctx, rawIDToken)
	if err != nil {
		h.renderOIDCError(c, http.StatusForbidden, i18n.CodeAuthProviderError,
			"the id_token is invalid: "+err.Error())
		return
	}
	if idToken.Nonce != state.Nonce {
		h.renderOIDCError(c, http.StatusForbidden, i18n.CodeAuthStateInvalid, "the id_token nonce does not match")
		return
	}
	// Kept for a short while so the logout offered below (and the "Sign out" of
	// the dashboard) can send it back as `id_token_hint` and make the provider
	// drop its session without asking for a confirmation.
	h.storeIDToken(c, rawIDToken)

	claims := struct {
		Email             string `json:"email"`
		PreferredUsername string `json:"preferred_username"`
		Upn               string `json:"upn"`
		Subject           string `json:"sub"`
	}{}
	if err := idToken.Claims(&claims); err != nil {
		h.renderOIDCError(c, http.StatusForbidden, i18n.CodeAuthProviderError, "could not read the id_token claims")
		return
	}

	email := firstNonEmpty(claims.Email, claims.PreferredUsername, claims.Upn)
	if !h.Sessions.EmailAllowedByKeycloakAccounts(email) {
		h.Log.Warn("OIDC login rejected by the allow list", "email", email, "ip", middleware.ClientIP(c))
		h.renderOIDCError(c, http.StatusForbidden, i18n.CodeAuthDomainNotAllowed,
			"the account "+email+" is not allowed to access this instance")
		return
	}

	if err := h.Sessions.Create(ctx, c.Writer, services.SessionData{
		Subject: claims.Subject,
		Email:   email,
		Method:  string(config.AuthMethodKeycloak),
	}); err != nil {
		h.renderOIDCError(c, http.StatusInternalServerError, i18n.CodeInternal, err.Error())
		return
	}
	h.Log.Info("OIDC login succeeded", "email", email, "ip", middleware.ClientIP(c))
	c.Redirect(http.StatusFound, h.Cfg.AppURL+ensureLeadingSlash(state.Redirect))
}

// renderOIDCError returns a small HTML page (the browser is involved, so a JSON
// body would be confusing) containing the stable error code that the frontend
// translates when the user comes back to the dashboard.
//
// The page offers the way out of the provider session: after a rejected login
// the browser still holds the provider cookie, so signing in again would sign
// the very same account in. The logout handoff is the action that reliably
// clears that cookie; the forced re-login (prompt=login) is intentionally not
// offered here.
func (h *Container) renderOIDCError(c *gin.Context, status int, code, message string) {
	c.Header("Content-Type", "text/html; charset=utf-8")
	c.Status(status)
	signOut := "Sign out"
	if h.Cfg.KeycloakRealm != "" {
		signOut += " of " + h.Cfg.KeycloakRealm
	}
	fmt.Fprintf(c.Writer, `<!doctype html><html lang="en"><head><meta charset="utf-8">
<title>Up - authentication failed</title>
<style>body{font-family:system-ui,sans-serif;background:#0b0f19;color:#e5e7eb;display:flex;
min-height:100vh;align-items:center;justify-content:center;margin:0}main{max-width:38rem;padding:2rem}
code{background:#111827;padding:.15rem .4rem;border-radius:.25rem}</style></head>
<body><main><h1>Authentication failed</h1><p>%s</p><p>Error code: <code>%s</code></p>
<p>The browser is still signed in at the identity provider. Sign out below and sign in again to use another account.</p>
<p><a style="color:#60a5fa" href="%s/api/auth/oidc/logout?redirect=/login">%s</a></p></main></body></html>`,
		htmlEscape(message), htmlEscape(code), h.Cfg.AppURL, htmlEscape(signOut))
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func ensureLeadingSlash(value string) string {
	if strings.HasPrefix(value, "/") {
		return value
	}
	return "/" + value
}

func htmlEscape(value string) string {
	replacer := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;")
	return replacer.Replace(value)
}
