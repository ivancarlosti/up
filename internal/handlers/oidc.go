package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/gin-gonic/gin"
	"golang.org/x/oauth2"

	"github.com/ivancarlosti/up/internal/api"
	"github.com/ivancarlosti/up/internal/config"
	"github.com/ivancarlosti/up/internal/i18n"
	"github.com/ivancarlosti/up/internal/middleware"
	"github.com/ivancarlosti/up/internal/utils"
)

// oidcStateCookie holds the signed state/nonce/PKCE verifier during the flow.
const oidcStateCookie = "up_oidc_state"

// oidcIDTokenCookie keeps the raw ID token of the last successful token exchange
// for a short while. oidcLogout sends it back as `id_token_hint`, which is what
// lets the provider drop its own session without showing its confirmation page.
const oidcIDTokenCookie = "up_oidc_id_token"

// oidcIDTokenCookieTTL is how long the ID token stays available (seconds).
const oidcIDTokenCookieTTL = 900

// oidcProvider wraps the Keycloak/OIDC discovery document, the token verifier
// and the OAuth2 client.
type OIDCProvider struct {
	cfg      *config.Config
	log      *slog.Logger
	provider *oidc.Provider
	verifier *oidc.IDTokenVerifier
	oauth    *oauth2.Config
	// logoutURL is the provider end_session_endpoint, empty when the provider
	// does not advertise one (then only the local session is cleared).
	logoutURL string
}

// NewOIDCProvider discovers the provider endpoints. It returns nil (without an
// error) when AUTH_METHOD is not keycloak.
func NewOIDCProvider(ctx context.Context, cfg *config.Config, log *slog.Logger) (*OIDCProvider, error) {
	if cfg.AuthMethod != config.AuthMethodKeycloak {
		return nil, nil
	}
	provider, err := oidc.NewProvider(ctx, cfg.KeycloakIssuer)
	if err != nil {
		return nil, fmt.Errorf("discovering the OIDC provider at %s: %w", cfg.KeycloakIssuer, err)
	}
	// The end_session_endpoint is not part of the oauth2 endpoint set, so it is
	// read from the raw discovery document (it is optional in the spec).
	var discovery struct {
		EndSessionEndpoint string `json:"end_session_endpoint"`
	}
	if err := provider.Claims(&discovery); err != nil {
		return nil, fmt.Errorf("reading the OIDC discovery document at %s: %w", cfg.KeycloakIssuer, err)
	}
	if discovery.EndSessionEndpoint == "" {
		log.Warn("the OIDC provider advertises no end_session_endpoint; signing out will only clear the local session",
			"issuer", cfg.KeycloakIssuer)
	}
	log.Info("OIDC provider discovered",
		"issuer", cfg.KeycloakIssuer,
		"redirect_uri", cfg.KeycloakRedirectURI,
		"post_logout_redirect_uri", cfg.KeycloakPostLogoutRedirectURI,
		"accounts", cfg.KeycloakAccounts)
	return &OIDCProvider{
		cfg:       cfg,
		log:       log,
		provider:  provider,
		verifier:  provider.Verifier(&oidc.Config{ClientID: cfg.KeycloakClientID}),
		logoutURL: discovery.EndSessionEndpoint,
		oauth: &oauth2.Config{
			ClientID:     cfg.KeycloakClientID,
			ClientSecret: cfg.KeycloakClientSecret,
			Endpoint:     provider.Endpoint(),
			RedirectURL:  cfg.KeycloakRedirectURI,
			Scopes:       []string{oidc.ScopeOpenID, "profile", "email"},
		},
	}, nil
}

// statePayload travels inside the signed state cookie.
type statePayload struct {
	State    string `json:"state"`
	Nonce    string `json:"nonce"`
	Verifier string `json:"verifier"`
	Redirect string `json:"redirect"`
}

// oidcLogin starts the Authorization Code flow with PKCE.
func (h *Container) oidcLogin(c *gin.Context) {
	if h.OIDC == nil {
		api.WriteError(c, http.StatusForbidden, i18n.CodeAuthMethodDisabled,
			"OIDC login is only available when AUTH_METHOD=keycloak")
		return
	}
	state := utils.MustRandomHex(16)
	nonce := utils.MustRandomHex(16)
	verifier := oauth2.GenerateVerifier()

	payload, err := json.Marshal(statePayload{
		State:    state,
		Nonce:    nonce,
		Verifier: verifier,
		Redirect: c.DefaultQuery("redirect", "/"),
	})
	if err != nil {
		api.WriteServiceError(c, err)
		return
	}
	signed, err := h.Sessions.SignState(c.Request.Context(), string(payload))
	if err != nil {
		api.WriteServiceError(c, err)
		return
	}
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     oidcStateCookie,
		Value:    signed,
		Path:     "/",
		HttpOnly: true,
		Secure:   h.Cfg.SecureCookies(),
		SameSite: http.SameSiteLaxMode,
		MaxAge:   600,
	})

	authURL := h.OIDC.oauth.AuthCodeURL(state, loginOptions(c.Query("prompt"), nonce, verifier)...)
	c.Redirect(http.StatusFound, authURL)
}

// loginOptions builds the authorization request options, including an optional
// prompt=login. That value is what lets the user pick another account after a
// rejected login: without it the provider silently reuses the browser session.
func loginOptions(prompt, nonce, verifier string) []oauth2.AuthCodeOption {
	options := []oauth2.AuthCodeOption{
		oidc.Nonce(nonce),
		oauth2.S256ChallengeOption(verifier),
		oauth2.AccessTypeOnline,
	}
	if value := allowedPrompt(prompt); value != "" {
		options = append(options, oauth2.SetAuthURLParam("prompt", value))
	}
	return options
}

// allowedPrompt keeps only the prompt values the dashboard uses. The value ends
// up in a provider URL, so it is never forwarded verbatim.
func allowedPrompt(value string) string {
	if strings.TrimSpace(value) == "login" {
		return "login"
	}
	return ""
}

// clearOIDCState removes the single use state cookie.
func (h *Container) clearOIDCState(c *gin.Context) {
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     oidcStateCookie,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   h.Cfg.SecureCookies(),
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
}

// storeIDToken keeps the raw ID token of a just verified callback so a later
// logout can send it back as `id_token_hint`.
func (h *Container) storeIDToken(c *gin.Context, raw string) {
	if raw == "" {
		return
	}
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     oidcIDTokenCookie,
		Value:    raw,
		Path:     "/",
		HttpOnly: true,
		Secure:   h.Cfg.SecureCookies(),
		SameSite: http.SameSiteLaxMode,
		MaxAge:   oidcIDTokenCookieTTL,
	})
}

// idToken returns the ID token stored by a recent callback ("" when absent).
func (h *Container) idToken(c *gin.Context) string {
	value, err := c.Cookie(oidcIDTokenCookie)
	if err != nil {
		return ""
	}
	return value
}

// clearIDToken removes the ID token cookie.
func (h *Container) clearIDToken(c *gin.Context) {
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     oidcIDTokenCookie,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   h.Cfg.SecureCookies(),
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
}

// oidcLogout performs an RP-initiated logout: the local cookies are always
// cleared and the browser is then handed over to the provider so its own
// session is dropped too. That session is what would otherwise sign the same
// (possibly rejected) account back in silently, leaving no way to switch.
func (h *Container) oidcLogout(c *gin.Context) {
	redirect := ensureLeadingSlash(c.DefaultQuery("redirect", "/login"))

	h.Sessions.Clear(c.Writer)
	h.clearOIDCState(c)
	h.clearIDToken(c)

	if h.OIDC == nil || h.OIDC.logoutURL == "" {
		// No provider side logout available: the local session is gone, which
		// is all this endpoint promises in that case.
		c.Redirect(http.StatusFound, h.Cfg.AppURL+redirect)
		return
	}

	target, err := url.Parse(h.OIDC.logoutURL)
	if err != nil {
		h.Log.Error("invalid end_session_endpoint", "url", h.OIDC.logoutURL, "error", err)
		c.Redirect(http.StatusFound, h.Cfg.AppURL+redirect)
		return
	}
	query := target.Query()
	query.Set("client_id", h.Cfg.KeycloakClientID)
	query.Set("post_logout_redirect_uri", h.Cfg.KeycloakPostLogoutRedirectURI)
	if raw := h.idToken(c); raw != "" {
		// Without the hint the provider cannot tell which session belongs to
		// this client and shows its confirmation page first.
		query.Set("id_token_hint", raw)
	}
	target.RawQuery = query.Encode()

	h.Log.Info("OIDC logout started", "ip", middleware.ClientIP(c))
	c.Redirect(http.StatusFound, target.String())
}
