package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/ivancarlosti/up/internal/api"
	"github.com/ivancarlosti/up/internal/config"
	"github.com/ivancarlosti/up/internal/i18n"
	"github.com/ivancarlosti/up/internal/middleware"
	"github.com/ivancarlosti/up/internal/models"
	"github.com/ivancarlosti/up/internal/services"
)

// session reports whether the caller is authenticated and how login works.
//
// The captcha keys come from the shared settings table (Admin > Settings), not
// from the environment: the SPA reads this payload on boot and only gets the
// public site key, never the secret.
func (h *Container) session(c *gin.Context) {
	identity, ok := h.Sessions.CurrentIdentity(c.Request.Context(), c.Request)
	captcha := h.Settings.Recaptcha()
	payload := gin.H{
		"authenticated":       ok,
		"auth_method":         string(h.Cfg.AuthMethod),
		"login_enabled":       h.Cfg.AuthMethod == config.AuthMethodAccount,
		"oidc_enabled":        h.Cfg.AuthMethod == config.AuthMethodKeycloak,
		"recaptcha_enabled":   captcha.Enabled(),
		"recaptcha_client_id": captcha.ClientID,
		"instance_url":        h.Cfg.AppURL,
	}
	if ok {
		payload["identity"] = identity
	}
	if !ok {
		c.JSON(http.StatusOK, payload)
		return
	}
	c.JSON(http.StatusOK, payload)
}

// login validates the account credentials and opens a session.
func (h *Container) login(c *gin.Context) {
	if h.Cfg.AuthMethod != config.AuthMethodAccount {
		api.WriteError(c, http.StatusForbidden, i18n.CodeAuthMethodDisabled,
			"the account login is disabled on this instance (AUTH_METHOD="+string(h.Cfg.AuthMethod)+")")
		return
	}
	var payload struct {
		Email          string `json:"email"`
		Password       string `json:"password"`
		RecaptchaToken string `json:"recaptcha_token"`
	}
	if !bindJSON(c, &payload) {
		return
	}
	if payload.Email == "" || payload.Password == "" {
		api.BadRequest(c, "email and password are required")
		return
	}

	if captcha := h.Settings.Recaptcha(); captcha.Enabled() {
		if strings.TrimSpace(payload.RecaptchaToken) == "" {
			api.WriteError(c, http.StatusForbidden, i18n.CodeAuthCaptchaMissing,
				"the captcha token is required")
			return
		}
		if err := h.verifyRecaptcha(c.Request.Context(), payload.RecaptchaToken,
			models.RecaptchaActionLogin, middleware.ClientIP(c)); err != nil {
			api.WriteError(c, http.StatusForbidden, i18n.CodeAuthCaptchaFailed, err.Error())
			return
		}
	}

	if !h.Sessions.AccountAllowed(payload.Email, payload.Password) {
		h.Log.Warn("failed login attempt", "email", payload.Email, "ip", middleware.ClientIP(c))
		api.WriteError(c, http.StatusUnauthorized, i18n.CodeAuthInvalid, "invalid credentials")
		return
	}

	if err := h.Sessions.Create(c.Request.Context(), c.Writer, services.SessionData{
		Subject: payload.Email,
		Email:   payload.Email,
		Method:  string(config.AuthMethodAccount),
	}); err != nil {
		api.WriteServiceError(c, err)
		return
	}
	h.Log.Info("login succeeded", "email", payload.Email, "ip", middleware.ClientIP(c))
	api.OK(c, gin.H{
		"authenticated": true,
		"identity": services.Identity{
			Email:    payload.Email,
			Method:   string(config.AuthMethodAccount),
			AuthMode: string(config.AuthMethodAccount),
		},
	})
}

// logout clears the session cookie.
func (h *Container) logout(c *gin.Context) {
	h.Sessions.Clear(c.Writer)
	api.NoContent(c)
}

// recaptchaVerdict explains why a Site Verify answer must be rejected, and
// returns "" when the answer is acceptable.
//
// reCAPTCHA v3 answers every execute with a score instead of a challenge, so
// `success` alone is not enough: the token also carries the action it was minted
// for (a token harvested from the public subscription form must not unlock the
// login) and the score the operator tuned in Admin > Settings. A token with no
// score comes from a v2 key pair, where the rendered challenge is the gate: the
// threshold does not apply to it.
func recaptchaVerdict(success bool, score float64, action, expectedAction string,
	errorCodes []string, minScore float64) string {
	if !success {
		return "the captcha validation failed: " + strings.Join(errorCodes, ", ")
	}
	if action != "" && action != expectedAction {
		return "the captcha token was minted for the action " + action +
			", the endpoint expects " + expectedAction
	}
	if score > 0 && score < minScore {
		return fmt.Sprintf("the captcha score %.2f is below the minimum %.2f", score, minScore)
	}
	return ""
}

// verifyRecaptcha validates a Google reCAPTCHA token with the Site Verify
// endpoint.
//
// The secret, the score threshold and the expected action are read from the
// shared settings on every call, so a pair rotated in Admin > Settings applies
// to the very next attempt, without a restart.
func (h *Container) verifyRecaptcha(ctx context.Context, token, expectedAction, clientIP string) error {
	captcha := h.Settings.Recaptcha()
	if captcha.ClientSecret == "" {
		// The captcha was switched off between the gate of the caller and this
		// call: fail closed instead of posting an empty secret to Google.
		return errors.New("the reCAPTCHA secret is not configured")
	}
	form := url.Values{}
	form.Set("secret", captcha.ClientSecret)
	form.Set("response", token)
	if clientIP != "" {
		form.Set("remoteip", clientIP)
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"https://www.google.com/recaptcha/api/siteverify", strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return err
	}
	defer func() { _ = response.Body.Close() }()

	var result struct {
		Success    bool     `json:"success"`
		Score      float64  `json:"score"`
		Action     string   `json:"action"`
		ErrorCodes []string `json:"error-codes"`
	}
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		return err
	}
	// The score is logged either way: it is the only way to tell whether the
	// threshold tuned in Admin > Settings matches the traffic of this instance.
	if reason := recaptchaVerdict(result.Success, result.Score, result.Action,
		expectedAction, result.ErrorCodes, captcha.MinScore); reason != "" {
		h.Log.Warn("captcha rejected", "action", result.Action, "score", result.Score,
			"minimum", captcha.MinScore, "reason", reason)
		return &services.APIError{
			Status:  http.StatusForbidden,
			Code:    i18n.CodeAuthCaptchaFailed,
			Message: reason,
		}
	}
	h.Log.Debug("captcha accepted", "action", result.Action, "score", result.Score)
	return nil
}
