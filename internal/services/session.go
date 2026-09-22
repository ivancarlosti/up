package services

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/ivancarlosti/up/internal/config"
	"github.com/ivancarlosti/up/internal/utils"
)

// SessionCookieName is the dashboard session cookie.
const SessionCookieName = "up_session"

// SessionData is the authenticated identity stored inside the signed cookie.
//
// The cookie is stateless (HMAC signed with the shared session secret) so no
// session table is required and every cluster node validates the same cookie.
// Rotating the secret in the database invalidates every session at once.
type SessionData struct {
	Subject   string `json:"sub"`
	Email     string `json:"email"`
	Method    string `json:"method"` // account | keycloak
	IssuedAt  string `json:"iat"`
	ExpiresAt string `json:"exp"`
}

// SessionService creates, reads and clears the dashboard session cookie.
type SessionService struct {
	cfg      *config.Config
	settings *SettingService
	log      *slog.Logger
}

// NewSessionService builds the session service.
func NewSessionService(cfg *config.Config, settings *SettingService, log *slog.Logger) *SessionService {
	return &SessionService{cfg: cfg, settings: settings, log: log}
}

// Create writes the session cookie for the authenticated identity.
func (s *SessionService) Create(ctx context.Context, w http.ResponseWriter, data SessionData) error {
	secret, err := s.settings.SessionSecret(ctx)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	data.IssuedAt = utils.EncodeTime(now)
	data.ExpiresAt = utils.EncodeTime(now.Add(time.Duration(s.cfg.SessionTTLHours) * time.Hour))

	payload, err := json.Marshal(data)
	if err != nil {
		return err
	}
	value := utils.SignedValue(secret, base64.RawURLEncoding.EncodeToString(payload))

	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookieName,
		Value:    value,
		Path:     "/",
		HttpOnly: true,
		Secure:   s.cfg.SecureCookies(),
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(time.Duration(s.cfg.SessionTTLHours) * time.Hour / time.Second),
	})
	return nil
}

// Read validates the cookie and returns the session. The boolean is false when
// there is no cookie, when the signature is invalid or when it has expired.
func (s *SessionService) Read(ctx context.Context, r *http.Request) (*SessionData, bool) {
	cookie, err := r.Cookie(SessionCookieName)
	if err != nil || cookie.Value == "" {
		return nil, false
	}
	secret, err := s.settings.SessionSecret(ctx)
	if err != nil {
		return nil, false
	}
	payload, ok := utils.VerifySignedValue(secret, cookie.Value)
	if !ok {
		return nil, false
	}
	raw, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		return nil, false
	}
	data := SessionData{}
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil, false
	}
	expires, err := utils.DecodeTime(data.ExpiresAt)
	if err != nil || time.Now().UTC().After(expires) {
		return nil, false
	}
	return &data, true
}

// Clear removes the session cookie.
func (s *SessionService) Clear(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   s.cfg.SecureCookies(),
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
}

// SignState signs an OIDC state/nonce payload using the shared secret.
func (s *SessionService) SignState(ctx context.Context, payload string) (string, error) {
	secret, err := s.settings.SessionSecret(ctx)
	if err != nil {
		return "", err
	}
	return utils.SignedValue(secret, base64.RawURLEncoding.EncodeToString([]byte(payload))), nil
}

// VerifyState checks a previously signed state payload.
func (s *SessionService) VerifyState(ctx context.Context, value string) (string, bool) {
	secret, err := s.settings.SessionSecret(ctx)
	if err != nil {
		return "", false
	}
	payload, ok := utils.VerifySignedValue(secret, value)
	if !ok {
		return "", false
	}
	raw, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		return "", false
	}
	return string(raw), true
}

// AuthMethod exposes the configured authentication method.
func (s *SessionService) AuthMethod() config.AuthMethod { return s.cfg.AuthMethod }
