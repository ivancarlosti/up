package services

import (
	"context"
	"fmt"
	"time"

	"gorm.io/gorm"

	"github.com/ivancarlosti/up/internal/i18n"
	"github.com/ivancarlosti/up/internal/models"
	"github.com/ivancarlosti/up/internal/utils"
)

// Authenticate validates a presented bearer token and updates its usage
// metadata. The optional scope argument is verified against the token scopes.
func (s *TokenService) Authenticate(ctx context.Context, plain, clientIP string, required models.TokenScope) (*models.APIToken, error) {
	plain = trimmed(plain, 300)
	if plain == "" {
		return nil, ErrForbidden(i18n.CodeTokenRequired, "an API token is required (Authorization: Bearer <token>)")
	}
	prefix := utils.APITokenPrefix(plain)
	if prefix == "" {
		return nil, ErrForbidden(i18n.CodeTokenInvalid, "malformed API token")
	}

	var token models.APIToken
	err := s.db.WithContext(ctx).Where("prefix = ?", prefix).First(&token).Error
	if err == gorm.ErrRecordNotFound {
		return nil, ErrForbidden(i18n.CodeTokenInvalid, "unknown API token")
	}
	if err != nil {
		return nil, ErrInternal(err)
	}
	if !utils.SecureCompare(token.TokenHash, utils.SHA256Hex(plain)) {
		return nil, ErrForbidden(i18n.CodeTokenInvalid, "invalid API token")
	}
	if token.Expired(time.Now().UTC()) {
		return nil, ErrForbidden(i18n.CodeTokenExpired, "this API token is expired or revoked")
	}
	if required != "" && !token.HasScope(required) {
		return nil, ErrForbidden(i18n.CodeTokenScope,
			fmt.Sprintf("this API token does not grant the %q scope", required))
	}

	now := time.Now().UTC()
	if err := s.db.WithContext(ctx).Model(&models.APIToken{}).Where("id = ?", token.ID).
		Updates(map[string]any{"last_used_at": now, "last_used_ip": clientIP}).Error; err != nil {
		s.log.Debug("could not update the token usage metadata", "error", err)
	}
	token.ScopeList = models.SplitList(token.Scopes)
	return &token, nil
}

// get loads a token by id (without the secret).
func (s *TokenService) get(ctx context.Context, id uint) (*models.APIToken, error) {
	var token models.APIToken
	if err := s.db.WithContext(ctx).First(&token, id).Error; err != nil {
		return nil, ErrNotFound(i18n.CodeNotFound, fmt.Sprintf("api token %d does not exist", id))
	}
	token.ScopeList = models.SplitList(token.Scopes)
	return &token, nil
}
