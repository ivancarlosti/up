package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/ivancarlosti/up/internal/api"
	"github.com/ivancarlosti/up/internal/config"
	"github.com/ivancarlosti/up/internal/i18n"
	"github.com/ivancarlosti/up/internal/middleware"
	"github.com/ivancarlosti/up/internal/services"
)

// session reports whether the caller is authenticated and how login works.
func (h *Container) session(c *gin.Context) {
	identity, ok := h.Sessions.CurrentIdentity(c.Request.Context(), c.Request)
	payload := gin.H{
		"authenticated":       ok,
		"auth_method":         string(h.Cfg.AuthMethod),
		"login_enabled":       h.Cfg.AuthMethod == config.AuthMethodAccount,
		"oidc_enabled":        h.Cfg.AuthMethod == config.AuthMethodKeycloak,
		"recaptcha_enabled":   h.Cfg.RecaptchaEnabled,
		"recaptcha_client_id": h.Cfg.RecaptchaClientID,
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

	if h.Cfg.RecaptchaEnabled {
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

// verifyRecaptcha validates a Google reCAPTCHA token (v2 checkbox or v3).
func (h *Container) verifyRecaptcha(ctx context.Context, token, clientIP string) error {
	form := url.Values{}
	form.Set("secret", h.Cfg.RecaptchaClientSecret)
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
	if !result.Success {
		return &services.APIError{
			Status:  http.StatusForbidden,
			Code:    i18n.CodeAuthCaptchaFailed,
			Message: "the captcha validation failed: " + strings.Join(result.ErrorCodes, ", "),
		}
	}
	// reCAPTCHA v3 returns a score between 0 and 1: require at least 0.5.
	if result.Score > 0 && result.Score < 0.5 {
		return &services.APIError{
			Status:  http.StatusForbidden,
			Code:    i18n.CodeAuthCaptchaFailed,
			Message: "the captcha score is too low",
		}
	}
	return nil
}
