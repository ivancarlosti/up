package services

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"gorm.io/gorm"

	"github.com/ivancarlosti/up/internal/i18n"
	"github.com/ivancarlosti/up/internal/models"
	"github.com/ivancarlosti/up/internal/utils"
)

// TokenService manages the bearer tokens of the public REST API (/api/v1).
//
// Only the SHA-256 hash of a token is persisted; the plain value is returned
// exactly once, when the token is created.
type TokenService struct {
	db  *gorm.DB
	log *slog.Logger
}

// NewTokenService builds the token service.
func NewTokenService(db *gorm.DB, log *slog.Logger) *TokenService {
	return &TokenService{db: db, log: log}
}

// List returns every token (without the secrets).
func (s *TokenService) List(ctx context.Context) ([]*models.APIToken, error) {
	var tokens []*models.APIToken
	if err := s.db.WithContext(ctx).Order("created_at DESC").Find(&tokens).Error; err != nil {
		return nil, ErrInternal(fmt.Errorf("listing api tokens: %w", err))
	}
	for _, token := range tokens {
		token.ScopeList = models.SplitList(token.Scopes)
	}
	return tokens, nil
}

// Create generates a new token and returns it together with the plain secret.
func (s *TokenService) Create(ctx context.Context, name string, scopes []string, expiresAt *time.Time) (*models.APIToken, error) {
	name = trimmed(name, 150)
	if name == "" {
		return nil, ErrBadRequest(i18n.CodeValidation, "name is required")
	}
	scopes = normalizeScopes(scopes)
	if len(scopes) == 0 {
		return nil, ErrBadRequest(i18n.CodeValidation, "at least one scope (read or write) is required")
	}

	plain, prefix, hash, err := utils.NewAPIToken()
	if err != nil {
		return nil, ErrInternal(err)
	}
	token := &models.APIToken{
		Name:      name,
		Prefix:    prefix,
		TokenHash: hash,
		Scopes:    models.JoinList(scopes),
		ExpiresAt: expiresAt,
		CreatedAt: time.Now().UTC(),
	}
	if err := s.db.WithContext(ctx).Create(token).Error; err != nil {
		return nil, ErrInternal(fmt.Errorf("creating api token: %w", err))
	}
	token.PlainToken = plain
	token.ScopeList = scopes
	s.log.Info("api token created", "id", token.ID, "name", token.Name, "scopes", token.Scopes)
	return token, nil
}

// Update changes the name, scopes and expiry of a token (never the secret).
func (s *TokenService) Update(ctx context.Context, id uint, name string, scopes []string, expiresAt *time.Time) (*models.APIToken, error) {
	updates := map[string]any{"expires_at": expiresAt}
	if trimmed(name, 150) != "" {
		updates["name"] = trimmed(name, 150)
	}
	if len(scopes) > 0 {
		normalized := normalizeScopes(scopes)
		if len(normalized) == 0 {
			return nil, ErrBadRequest(i18n.CodeValidation, "scopes must contain read and/or write")
		}
		updates["scopes"] = models.JoinList(normalized)
	}

	result := s.db.WithContext(ctx).Model(&models.APIToken{}).Where("id = ?", id).Updates(updates)
	if result.Error != nil {
		return nil, ErrInternal(result.Error)
	}
	if result.RowsAffected == 0 {
		return nil, ErrNotFound(i18n.CodeNotFound, fmt.Sprintf("api token %d does not exist", id))
	}
	return s.get(ctx, id)
}

// Revoke invalidates a token without deleting its history.
func (s *TokenService) Revoke(ctx context.Context, id uint) error {
	result := s.db.WithContext(ctx).Model(&models.APIToken{}).Where("id = ?", id).
		Updates(map[string]any{"revoked_at": time.Now().UTC()})
	if result.Error != nil {
		return ErrInternal(result.Error)
	}
	if result.RowsAffected == 0 {
		return ErrNotFound(i18n.CodeNotFound, fmt.Sprintf("api token %d does not exist", id))
	}
	s.log.Warn("api token revoked", "id", id)
	return nil
}

// Delete removes a token permanently.
func (s *TokenService) Delete(ctx context.Context, id uint) error {
	result := s.db.WithContext(ctx).Delete(&models.APIToken{}, id)
	if result.Error != nil {
		return ErrInternal(result.Error)
	}
	if result.RowsAffected == 0 {
		return ErrNotFound(i18n.CodeNotFound, fmt.Sprintf("api token %d does not exist", id))
	}
	s.log.Warn("api token deleted", "id", id)
	return nil
}

// normalizeScopes keeps only the known scopes.
func normalizeScopes(scopes []string) []string {
	allowed := map[string]bool{}
	for _, scope := range models.AllTokenScopes() {
		allowed[string(scope)] = true
	}
	out := make([]string, 0, len(scopes))
	for _, scope := range scopes {
		scope = trimmed(scope, 20)
		if allowed[scope] {
			out = append(out, scope)
		}
	}
	return out
}
