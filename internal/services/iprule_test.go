package services

import (
	"testing"

	"github.com/ivancarlosti/up/internal/models"
)

// TestRuleApplies locks the scope semantics: a rule scoped to "all" affects
// every surface, a scoped rule only its own scope, and disabled rules are
// ignored.
func TestRuleApplies(t *testing.T) {
	cases := []struct {
		name  string
		rule  models.IPRule
		scope models.IPRuleScope
		want  bool
	}{
		{"all rule affects the dashboard", models.IPRule{Scope: models.IPRuleScopeAll, Enabled: true}, models.IPRuleScopeDashboard, true},
		{"all rule affects the public surface", models.IPRule{Scope: models.IPRuleScopeAll, Enabled: true}, models.IPRuleScopePublic, true},
		{"api rule does not affect the dashboard", models.IPRule{Scope: models.IPRuleScopeAPI, Enabled: true}, models.IPRuleScopeDashboard, false},
		{"public rule affects the public surface", models.IPRule{Scope: models.IPRuleScopePublic, Enabled: true}, models.IPRuleScopePublic, true},
		{"disabled rules are ignored", models.IPRule{Scope: models.IPRuleScopeAll, Enabled: false}, models.IPRuleScopeAll, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ruleApplies(tc.rule, tc.scope); got != tc.want {
				t.Fatalf("ruleApplies(%s, %s) = %v, want %v", tc.rule.Scope, tc.scope, got, tc.want)
			}
		})
	}
}

func TestDefaultClusterSettings(t *testing.T) {
	settings := models.DefaultClusterSettings()
	if settings.FailureStrategy != models.FailureStrategyAllNodesFail {
		t.Fatalf("default failure strategy = %s", settings.FailureStrategy)
	}
	if settings.NodeUnavailableStrategy != models.NodeUnavailableIgnore {
		t.Fatalf("default node strategy = %s", settings.NodeUnavailableStrategy)
	}
	if settings.NotificationSender != models.NotificationSenderAnyWithLock {
		t.Fatalf("default sender = %s", settings.NotificationSender)
	}
	for _, strategy := range []models.FailureStrategy{models.FailureStrategyAnyNodeFails, models.FailureStrategyAllNodesFail, models.FailureStrategyQuorum} {
		if !strategy.Valid() {
			t.Errorf("strategy %s should be valid", strategy)
		}
	}
	if models.FailureStrategy("SOMETHING_ELSE").Valid() {
		t.Error("an unknown strategy must be rejected")
	}
}
