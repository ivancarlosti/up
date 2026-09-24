package models

import "time"

// ---------------------------------------------------------------------------
// Settings (key/value runtime configuration shared by every node)
// ---------------------------------------------------------------------------

// Well known setting keys.
const (
	SettingDefaultLocale     = "default_locale"
	SettingDefaultTheme      = "default_theme"
	SettingSessionSecret     = "session_secret"
	SettingClusterPrivateKey = "cluster_private_key"
	SettingAppName           = "app_name"
	SettingRateLogin         = "rate_limit_login"
	SettingRatePublic        = "rate_limit_public"
	// Expiry job (Admin > TLD/SSL expiration): when the daily certificate and
	// domain checks run, which registries are queried and how fast.
	SettingExpiryCheckTime      = "expiry_check_time"
	SettingExpiryCheckTimezone  = "expiry_check_timezone"
	SettingExpiryRDAPEnabled    = "expiry_rdap_enabled"
	SettingExpiryWHOISEnabled   = "expiry_whois_enabled"
	SettingExpiryRateLimitMS    = "expiry_rate_limit_ms"
	SettingExpiryTimeoutSeconds = "expiry_timeout_seconds"
	// SettingExpiryLastRunDay is the day bucket (unix/86400) of the last daily
	// run, kept so a restart does not repeat the work and a process that was
	// down at the scheduled time catches up on the next tick.
	SettingExpiryLastRunDay = "expiry_last_run_day"
)

// Setting is a single key/value pair. Because the table lives in the shared
// external database, every node reads the same runtime configuration.
//
// Note: the primary key column is named "setting_key" ("key" is a reserved word
// in MySQL/MariaDB and would require backticks in every query).
type Setting struct {
	Key       string    `gorm:"column:setting_key;primaryKey;size:64" json:"key"`
	Value     string    `gorm:"type:text" json:"value"`
	UpdatedAt time.Time `json:"updated_at"`
}

// ---------------------------------------------------------------------------
// Public API tokens
// ---------------------------------------------------------------------------

// TokenScope is the permission granted to an API token.
type TokenScope string

// Available token scopes.
const (
	// ScopeRead allows reading monitors, heartbeats, statistics and status.
	ScopeRead TokenScope = "read"
	// ScopeWrite additionally allows pausing, resuming, creating and deleting
	// monitors.
	ScopeWrite TokenScope = "write"
)

// AllTokenScopes lists every scope offered by the UI and the API.
func AllTokenScopes() []TokenScope {
	return []TokenScope{ScopeRead, ScopeWrite}
}

// APIToken is a bearer credential for the public REST API (/api/v1/*). Only
// the SHA-256 hash of the secret is stored; the plain value is shown once,
// right after creation.
type APIToken struct {
	ID   uint   `gorm:"primaryKey" json:"id"`
	Name string `gorm:"size:150;not null" json:"name"`
	// Prefix is the public lookup part of the token ("up_xxxxxxxx").
	Prefix string `gorm:"size:24;not null;index" json:"prefix"`
	// TokenHash is the SHA-256 hash of the full token.
	TokenHash string `gorm:"size:64;not null;index" json:"-"`

	Scopes     string     `gorm:"size:255;not null;default:read" json:"-"`
	ExpiresAt  *time.Time `json:"expires_at"`
	LastUsedAt *time.Time `json:"last_used_at"`
	LastUsedIP string     `gorm:"size:64" json:"last_used_ip"`
	RevokedAt  *time.Time `json:"revoked_at"`
	CreatedAt  time.Time  `json:"created_at"`

	// ScopeList is the scopes field exposed as a slice in the API.
	ScopeList []string `gorm:"-" json:"scopes"`
	// PlainToken is only populated in the response of the creation endpoint.
	PlainToken string `gorm:"-" json:"token,omitempty"`
}

// HasScope reports whether the token grants the requested scope.
func (t *APIToken) HasScope(scope TokenScope) bool {
	for _, s := range SplitList(t.Scopes) {
		if s == string(scope) {
			return true
		}
	}
	return false
}

// Expired reports whether the token can no longer be used.
func (t *APIToken) Expired(now time.Time) bool {
	if t.RevokedAt != nil {
		return true
	}
	return t.ExpiresAt != nil && now.After(*t.ExpiresAt)
}

// ---------------------------------------------------------------------------
// IP allow / deny rules
// ---------------------------------------------------------------------------

// IPRuleAction is what happens with a matching address.
type IPRuleAction string

// IP rule actions.
const (
	IPRuleAllow IPRuleAction = "allow"
	IPRuleDeny  IPRuleAction = "deny"
)

// Valid reports whether the action is known.
func (a IPRuleAction) Valid() bool { return a == IPRuleAllow || a == IPRuleDeny }

// IPRuleScope limits a rule to part of the application.
type IPRuleScope string

// IP rule scopes.
const (
	// IPRuleScopeAll affects every request.
	IPRuleScopeAll IPRuleScope = "all"
	// IPRuleScopeDashboard affects the admin UI and its REST API.
	IPRuleScopeDashboard IPRuleScope = "dashboard"
	// IPRuleScopeAPI affects the token based public API (/api/v1).
	IPRuleScopeAPI IPRuleScope = "api"
	// IPRuleScopePublic affects the public status pages and health endpoints.
	IPRuleScopePublic IPRuleScope = "public"
)

// AllIPRuleScopes lists every scope offered by the UI.
func AllIPRuleScopes() []IPRuleScope {
	return []IPRuleScope{IPRuleScopeAll, IPRuleScopeDashboard, IPRuleScopeAPI, IPRuleScopePublic}
}

// Valid reports whether the scope is known.
func (s IPRuleScope) Valid() bool {
	switch s {
	case IPRuleScopeAll, IPRuleScopeDashboard, IPRuleScopeAPI, IPRuleScopePublic:
		return true
	}
	return false
}

// IPRule is one entry of the allow/deny list. With no rules at all every
// address is allowed (the default), which keeps the out-of-the-box behaviour
// fully open. As soon as an "allow" rule exists for a scope, that scope becomes
// an allow list: only the matching addresses pass and everything else is
// rejected. "deny" rules always win over "allow" rules.
type IPRule struct {
	ID uint `gorm:"primaryKey" json:"id"`
	// CIDR accepts a single address ("203.0.113.7") or a network
	// ("203.0.113.0/24", "2001:db8::/32"). The column is pinned explicitly
	// because the default snake_case conversion would produce "c_id_r".
	CIDR    string       `gorm:"column:cidr;size:64;not null" json:"cidr"`
	Action  IPRuleAction `gorm:"size:10;not null;index" json:"action"`
	Scope   IPRuleScope  `gorm:"size:20;not null;default:all;index" json:"scope"`
	Note    string       `gorm:"size:255" json:"note"`
	Enabled bool         `gorm:"not null;default:true" json:"enabled"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}
