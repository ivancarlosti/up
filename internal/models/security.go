package models

import (
	"fmt"
	"strings"
	"time"
)

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
	// SettingTimeFormat is the clock the UI renders: auto (the browser locale),
	// 12h or 24h. It is a display preference, never a stored timestamp format.
	SettingTimeFormat = "time_format"
	// SettingTimezone is the IANA zone the daily jobs schedule their run in
	// (Admin > Settings, "General settings"). The certificate/domain job and
	// the web performance job share it: they used to keep one key each
	// (SettingExpiryCheckTimezone and SettingLighthouseCheckTimezone), and
	// those two keys are still written so a node running an older binary keeps
	// reading the value the operator chose.
	SettingTimezone   = "timezone"
	SettingRateLogin  = "rate_limit_login"
	SettingRatePublic = "rate_limit_public"
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
	// Lighthouse web performance (Admin > Settings and the Web performance
	// page): when the daily PageSpeed Insights job runs, how it paces itself,
	// which key it uses and how much history it keeps.
	SettingLighthouseEnabled        = "lighthouse_enabled"
	SettingLighthouseCheckTime      = "lighthouse_check_time"
	SettingLighthouseCheckTimezone  = "lighthouse_check_timezone"
	SettingLighthouseRateLimitMS    = "lighthouse_rate_limit_ms"
	SettingLighthouseTimeoutSeconds = "lighthouse_timeout_seconds"
	// SettingLighthouseInterval is the cadence (hourly, 6h, 12h, daily, weekly)
	// of a monitor that does not carry its own; SettingLighthouseWeekday anchors
	// the weekly one. Both are key/value rows, so adding them needed no
	// migration.
	SettingLighthouseInterval = "lighthouse_interval"
	SettingLighthouseWeekday  = "lighthouse_weekday"
	// SettingLighthouseAPIKey seeds the `key` parameter of the PageSpeed
	// Insights API. The environment variable only fills it on the first boot;
	// the setting wins afterwards (an admin can rotate the key at runtime).
	SettingLighthouseAPIKey = "lighthouse_api_key"
	// SettingLighthouseRetentionDays bounds the run history the web performance
	// page charts. 0 means "never purge".
	SettingLighthouseRetentionDays = "lighthouse_retention_days"
	// SettingLighthouseLastRunDay is the day bucket (unix/86400) of the last
	// daily run, kept so a restart does not repeat the work.
	SettingLighthouseLastRunDay = "lighthouse_last_run_day"
	// SettingHeartbeatRetentionDays is how many days of heartbeat history are
	// kept (Admin > Settings). 0 means "never purge"; see
	// models.DefaultHeartbeatRetentionDays for the value used when the setting
	// and HEARTBEAT_RETENTION_DAYS are both absent.
	SettingHeartbeatRetentionDays = "heartbeat_retention_days"
	// SettingUptimeWindowHours is the period the uptime percentages and the
	// heartbeat bars cover. It is the global default (Admin > Settings) and the
	// fallback of every public status page.
	SettingUptimeWindowHours = "uptime_window_hours"
	// SettingRecaptchaClientID is the public site key of Google reCAPTCHA and
	// SettingRecaptchaClientSecret the matching server side secret. Both are
	// edited in Admin > Settings ("Login protection") and stored here only:
	// the login form and the public status page subscription form require a
	// captcha token as soon as both are set.
	//
	// They used to come from RECAPTCHA_CLIENTID / RECAPTCHA_CLIENTSECRET. The
	// variables are gone (an upgrade must re-enter the keys once), which is
	// why the database is the only source of truth: one value per cluster
	// row, rotable at runtime without a restart.
	SettingRecaptchaClientID     = "recaptcha_client_id"
	SettingRecaptchaClientSecret = "recaptcha_client_secret"
	// SettingRecaptchaMinScore is the lowest v3 score accepted. A v3 key
	// answers every execute with a score between 0 and 1 instead of a
	// challenge, so the threshold *is* the policy; the card lets an operator
	// tune it between MinRecaptchaMinScore and MaxRecaptchaMinScore.
	SettingRecaptchaMinScore = "recaptcha_min_score"
)

const (
	// DefaultRecaptchaMinScore is Google's recommended starting threshold and
	// the value stored when an operator never touched the field.
	DefaultRecaptchaMinScore = 0.5
	// MinRecaptchaMinScore and MaxRecaptchaMinScore bound the accepted
	// threshold. Below 0.1 the captcha accepts almost everything (clearing the
	// keys is the way to switch it off); above 0.9 ordinary visitors start to
	// be rejected.
	MinRecaptchaMinScore = 0.1
	MaxRecaptchaMinScore = 0.9
)

const (
	// RecaptchaActionLogin and RecaptchaActionStatusSubscribe are the v3 action
	// names the frontend mints the tokens with, one per protected form. The
	// server compares the action carried by the token with the action of the
	// endpoint, so a token harvested from the public subscription form cannot
	// unlock the login. Google accepts alphanumerics, slashes and underscores
	// only, and the action must not be user specific.
	RecaptchaActionLogin           = "login"
	RecaptchaActionStatusSubscribe = "status_page_subscribe"
)

// RecaptchaSettings is the Google reCAPTCHA configuration of the public forms
// (Admin > Settings). An empty pair means "no captcha": enablement is derived
// from the values themselves, so a half filled configuration is never stored.
//
// The instance speaks reCAPTCHA v3 (score based): the browser never renders a
// widget, it asks grecaptcha for a token when the form is submitted, and the
// server accepts it once the score reaches MinScore.
type RecaptchaSettings struct {
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
	// MinScore is the smallest v3 score accepted, see MinRecaptchaMinScore.
	MinScore float64 `json:"min_score"`
}

// Normalize trims what an operator pasted from the reCAPTCHA console: the
// keys are copied by hand, and a trailing newline is the usual accident. A zero
// threshold means the payload did not carry one (it predates the field), so it
// falls back to Google's recommendation instead of accepting every visitor.
func (r *RecaptchaSettings) Normalize() {
	r.ClientID = strings.TrimSpace(r.ClientID)
	r.ClientSecret = strings.TrimSpace(r.ClientSecret)
	if r.MinScore == 0 {
		r.MinScore = DefaultRecaptchaMinScore
	}
}

// Validate reports the first problem with the configuration. Both halves are
// required together: a site key without a secret would hand out tokens the
// server cannot verify, and a secret without a site key would demand a token no
// client can mint. The threshold has to stay inside the supported band.
//
// An empty pair is always valid: clearing the keys is how an operator switches
// the captcha off, and the threshold means nothing without them.
func (r RecaptchaSettings) Validate() string {
	if r.ClientID == "" && r.ClientSecret == "" {
		return ""
	}
	if r.ClientID == "" || r.ClientSecret == "" {
		return "both the reCAPTCHA site key and the secret are required, or neither"
	}
	if r.MinScore == 0 {
		// Zero is what a payload without the field decodes to; Normalize turns
		// it into the default, so Validate does not treat it as out of range.
		return ""
	}
	if r.MinScore < MinRecaptchaMinScore || r.MinScore > MaxRecaptchaMinScore {
		return fmt.Sprintf("the minimum score must be between %.1f and %.1f",
			float64(MinRecaptchaMinScore), float64(MaxRecaptchaMinScore))
	}
	return ""
}

// Enabled reports whether the captcha is configured and must be enforced.
func (r RecaptchaSettings) Enabled() bool {
	return r.ClientID != "" && r.ClientSecret != ""
}

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
	// ScopeRead allows reading monitors, heartbeats, statistics and status, and
	// running an on-demand analysis of a target (POST /api/v1/analyze), which
	// stores nothing.
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
