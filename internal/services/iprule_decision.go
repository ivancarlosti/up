package services

import (
	"context"
	"net"
	"time"

	"github.com/ivancarlosti/up/internal/i18n"
	"github.com/ivancarlosti/up/internal/models"
	"github.com/ivancarlosti/up/internal/utils"
)

// Decision evaluates the rules for a client address and a scope. It returns
// whether the request is allowed plus a short audit reason.
func (s *IPRuleService) Decision(ctx context.Context, clientIP string, scope models.IPRuleScope) (bool, string) {
	if s.cfg.SecurityBypassIPRules {
		return true, "ip rules bypassed by SECURITY_BYPASS_IP_RULES"
	}
	ip := net.ParseIP(clientIP)
	if ip == nil {
		// Unparsable addresses (unix sockets, malformed input) are not blocked:
		// blocking them would break unusual but valid deployments.
		return true, "unparsable client address"
	}

	rules := s.cached(ctx)
	if len(rules) == 0 {
		return true, "no ip rule configured"
	}

	hasAllow := false
	for _, rule := range rules {
		if !ruleApplies(rule, scope) {
			continue
		}
		if rule.Action == models.IPRuleAllow {
			hasAllow = true
		}
		if utils.MatchIP(rule.CIDR, ip) {
			if rule.Action == models.IPRuleDeny {
				return false, "matched the deny rule " + rule.CIDR
			}
			return true, "matched the allow rule " + rule.CIDR
		}
	}
	if hasAllow {
		return false, "the allow list of this scope does not include this address"
	}
	return true, "no matching ip rule"
}

// ruleApplies tells whether a rule is relevant for a scope.
func ruleApplies(rule models.IPRule, scope models.IPRuleScope) bool {
	if !rule.Enabled {
		return false
	}
	return rule.Scope == models.IPRuleScopeAll || rule.Scope == scope
}

// cached returns the enabled rules, refreshing them every ipRuleCacheTTL.
func (s *IPRuleService) cached(ctx context.Context) []models.IPRule {
	s.mu.RLock()
	if s.rules != nil && time.Since(s.loadedAt) < ipRuleCacheTTL {
		rules := s.rules
		s.mu.RUnlock()
		return rules
	}
	s.mu.RUnlock()

	var rules []models.IPRule
	if err := s.db.WithContext(ctx).Where("enabled = ?", true).Find(&rules).Error; err != nil {
		s.log.Error("could not load the ip rules", "error", err)
		rules = []models.IPRule{}
	}
	s.mu.Lock()
	s.rules = rules
	s.loadedAt = time.Now()
	s.mu.Unlock()
	return rules
}

// invalidate forces a reload on the next evaluation.
func (s *IPRuleService) invalidate() {
	s.mu.Lock()
	s.loadedAt = time.Time{}
	s.mu.Unlock()
}

// normalize validates a rule.
func (s *IPRuleService) normalize(rule *models.IPRule) error {
	rule.CIDR = trimmed(rule.CIDR, 64)
	rule.Note = trimmed(rule.Note, 255)
	if _, err := utils.ParseRule(rule.CIDR); err != nil {
		return ErrBadRequest(i18n.CodeIPRuleInvalid, err.Error())
	}
	if !rule.Action.Valid() {
		return ErrBadRequest(i18n.CodeIPRuleInvalid, "action must be allow or deny")
	}
	if rule.Scope == "" {
		rule.Scope = models.IPRuleScopeAll
	}
	if !rule.Scope.Valid() {
		return ErrBadRequest(i18n.CodeIPRuleInvalid, "scope must be all, dashboard, api or public")
	}
	return nil
}
