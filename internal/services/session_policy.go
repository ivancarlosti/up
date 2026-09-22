package services

import (
	"context"
	"net/http"
	"strings"

	"github.com/ivancarlosti/up/internal/config"
	"github.com/ivancarlosti/up/internal/utils"
)

// AccountAllowed checks a login attempt against ACCOUNT_LOGIN/ACCOUNT_PASSWORD.
// The comparisons are constant time to avoid leaking the credentials through
// response timing.
func (s *SessionService) AccountAllowed(email, password string) bool {
	if s.cfg.AuthMethod != config.AuthMethodAccount {
		return false
	}
	emailOK := utils.SecureCompare(strings.ToLower(strings.TrimSpace(email)),
		strings.ToLower(strings.TrimSpace(s.cfg.AccountLogin)))
	passwordOK := utils.SecureCompare(password, s.cfg.AccountPassword)
	return emailOK && passwordOK
}

// EmailAllowedByKeycloakAccounts applies the KEYCLOAK_ACCOUNTS allow list.
//
// Accepted entries: exact addresses (you@example.com) and domains written as
// "@empresa.com", "empresa.com" or "domain2.com". Everything else is rejected
// with 403, which makes it possible to reuse a public OIDC provider (social
// login) while keeping the dashboard private.
func (s *SessionService) EmailAllowedByKeycloakAccounts(email string) bool {
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" {
		return false
	}
	parts := strings.SplitN(email, "@", 2)
	domain := ""
	if len(parts) == 2 {
		domain = parts[1]
	}
	for _, entry := range s.cfg.KeycloakAccounts {
		if strings.Contains(entry, "@") {
			if strings.EqualFold(entry, email) {
				return true
			}
			continue
		}
		if domain != "" && (domain == entry || strings.HasSuffix(domain, "."+entry)) {
			return true
		}
	}
	return false
}

// Identity describes the authenticated user for the frontend.
type Identity struct {
	Email     string `json:"email"`
	Method    string `json:"method"`
	AuthMode  string `json:"auth_method"`
	ExpiresAt string `json:"expires_at,omitempty"`
}

// AuthMethodName returns the configured authentication method as a string.
func (s *SessionService) AuthMethodName() string { return string(s.cfg.AuthMethod) }

// CurrentIdentity returns the identity of the request. With AUTH_METHOD=none
// every request is treated as the built-in anonymous administrator.
func (s *SessionService) CurrentIdentity(ctx context.Context, r *http.Request) (*Identity, bool) {
	if s.cfg.AuthMethod == config.AuthMethodNone {
		return &Identity{
			Email:    "anonymous",
			Method:   string(config.AuthMethodNone),
			AuthMode: string(config.AuthMethodNone),
		}, true
	}
	data, ok := s.Read(ctx, r)
	if !ok {
		return nil, false
	}
	return &Identity{
		Email:     data.Email,
		Method:    data.Method,
		AuthMode:  string(s.cfg.AuthMethod),
		ExpiresAt: data.ExpiresAt,
	}, true
}

// SupportedLocales lists the languages shipped with the frontend.
func SupportedLocales() []string { return config.SupportedLocales }
