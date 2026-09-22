package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/gin-gonic/gin"
	"golang.org/x/oauth2"

	"github.com/ivancarlosti/up/internal/api"
	"github.com/ivancarlosti/up/internal/config"
	"github.com/ivancarlosti/up/internal/i18n"
	"github.com/ivancarlosti/up/internal/utils"
)

// oidcStateCookie holds the signed state/nonce/PKCE verifier during the flow.
const oidcStateCookie = "up_oidc_state"

// oidcProvider wraps the Keycloak/OIDC discovery document, the token verifier
// and the OAuth2 client.
type OIDCProvider struct {
	cfg      *config.Config
	log      *slog.Logger
	provider *oidc.Provider
	verifier *oidc.IDTokenVerifier
	oauth    *oauth2.Config
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
	log.Info("OIDC provider discovered",
		"issuer", cfg.KeycloakIssuer,
		"redirect_uri", cfg.KeycloakRedirectURI,
		"accounts", cfg.KeycloakAccounts)
	return &OIDCProvider{
		cfg:      cfg,
		log:      log,
		provider: provider,
		verifier: provider.Verifier(&oidc.Config{ClientID: cfg.KeycloakClientID}),
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

	authURL := h.OIDC.oauth.AuthCodeURL(state,
		oidc.Nonce(nonce),
		oauth2.S256ChallengeOption(verifier),
		oauth2.AccessTypeOnline,
	)
	c.Redirect(http.StatusFound, authURL)
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
