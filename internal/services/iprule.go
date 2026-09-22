package services

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"gorm.io/gorm"

	"github.com/ivancarlosti/up/internal/config"
	"github.com/ivancarlosti/up/internal/i18n"
	"github.com/ivancarlosti/up/internal/models"
)

// ipRuleCacheTTL is how long the rules are cached in memory. They change
// rarely but are evaluated on every single request.
const ipRuleCacheTTL = 10 * time.Second

// IPRuleService implements the allow/deny list protecting the application.
//
// Behaviour (documented in docs/security.md):
//
//   - no rule at all         -> everything is allowed (the out of the box state);
//   - a matching "deny" rule -> the request is rejected, always;
//   - an "allow" rule exists -> that scope becomes an allow list: only the
//     matching addresses pass and every other address is rejected.
type IPRuleService struct {
	db  *gorm.DB
	cfg *config.Config
	log *slog.Logger

	mu       sync.RWMutex
	rules    []models.IPRule
	loadedAt time.Time
}

// NewIPRuleService builds the IP rule service.
func NewIPRuleService(db *gorm.DB, cfg *config.Config, log *slog.Logger) *IPRuleService {
	return &IPRuleService{db: db, cfg: cfg, log: log}
}

// List returns every rule (ordered by action and scope).
func (s *IPRuleService) List(ctx context.Context) ([]models.IPRule, error) {
	var rules []models.IPRule
	if err := s.db.WithContext(ctx).Order("action ASC, scope ASC, cidr ASC").Find(&rules).Error; err != nil {
		return nil, ErrInternal(fmt.Errorf("listing ip rules: %w", err))
	}
	return rules, nil
}

// Create stores a new rule and refreshes the cache.
func (s *IPRuleService) Create(ctx context.Context, rule *models.IPRule) error {
	if err := s.normalize(rule); err != nil {
		return err
	}
	if err := s.db.WithContext(ctx).Create(rule).Error; err != nil {
		return ErrInternal(fmt.Errorf("creating ip rule: %w", err))
	}
	s.invalidate()
	s.log.Warn("ip rule created", "id", rule.ID, "cidr", rule.CIDR, "action", rule.Action, "scope", rule.Scope)
	return nil
}

// Update saves an existing rule.
func (s *IPRuleService) Update(ctx context.Context, rule *models.IPRule) error {
	if err := s.normalize(rule); err != nil {
		return err
	}
	result := s.db.WithContext(ctx).Model(&models.IPRule{}).Where("id = ?", rule.ID).Updates(map[string]any{
		"cidr":       rule.CIDR,
		"action":     rule.Action,
		"scope":      rule.Scope,
		"note":       rule.Note,
		"enabled":    rule.Enabled,
		"updated_at": time.Now().UTC(),
	})
	if result.Error != nil {
		return ErrInternal(result.Error)
	}
	if result.RowsAffected == 0 {
		return ErrNotFound(i18n.CodeNotFound, fmt.Sprintf("ip rule %d does not exist", rule.ID))
	}
	s.invalidate()
	s.log.Warn("ip rule updated", "id", rule.ID, "cidr", rule.CIDR, "action", rule.Action, "scope", rule.Scope)
	return nil
}

// Delete removes a rule.
func (s *IPRuleService) Delete(ctx context.Context, id uint) error {
	result := s.db.WithContext(ctx).Delete(&models.IPRule{}, id)
	if result.Error != nil {
		return ErrInternal(result.Error)
	}
	if result.RowsAffected == 0 {
		return ErrNotFound(i18n.CodeNotFound, fmt.Sprintf("ip rule %d does not exist", id))
	}
	s.invalidate()
	s.log.Warn("ip rule deleted", "id", id)
	return nil
}
