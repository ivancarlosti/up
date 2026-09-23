// Package i18n exposes the stable error codes returned by the HTTP API.
//
// Up never translates messages on the server: every error response contains a
// machine readable "code" (plus a developer oriented "detail"), and the Vue
// frontend translates the code with vue-i18n. That keeps a single source of
// truth for en-US, pt-BR and es-MX.
package i18n

// Error codes. Every code has a matching key in web/src/locales/*.json under
// the "errors" namespace.
const (
	// Generic
	CodeInternal       = "ERR_INTERNAL"
	CodeValidation     = "ERR_VALIDATION"
	CodeInvalidPayload = "ERR_INVALID_PAYLOAD"
	CodeNotFound       = "ERR_NOT_FOUND"
	CodeAlreadyExists  = "ERR_ALREADY_EXISTS"
	CodeDatabase       = "ERR_DATABASE"
	CodeForbidden      = "ERR_FORBIDDEN"
	CodeRateLimited    = "ERR_RATE_LIMITED"
	CodeNotFoundRoute  = "ERR_ROUTE_NOT_FOUND"

	// Authentication
	CodeAuthRequired         = "ERR_AUTH_REQUIRED"
	CodeAuthInvalid          = "ERR_AUTH_INVALID_CREDENTIALS"
	CodeAuthRateLimited      = "ERR_AUTH_RATE_LIMITED"
	CodeAuthDomainNotAllowed = "ERR_AUTH_DOMAIN_NOT_ALLOWED"
	CodeAuthCaptchaFailed    = "ERR_RECAPTCHA_FAILED"
	CodeAuthCaptchaMissing   = "ERR_RECAPTCHA_MISSING"
	CodeAuthStateInvalid     = "ERR_AUTH_STATE_INVALID"
	CodeAuthProviderError    = "ERR_AUTH_PROVIDER_ERROR"
	CodeAuthMethodDisabled   = "ERR_AUTH_METHOD_DISABLED"

	// Monitors
	CodeMonitorNotFound    = "ERR_MONITOR_NOT_FOUND"
	CodeMonitorTypeInvalid = "ERR_MONITOR_TYPE_INVALID"
	CodeMonitorConfig      = "ERR_MONITOR_CONFIG_INVALID"

	// Monitor groups
	CodeMonitorGroupNotFound = "ERR_MONITOR_GROUP_NOT_FOUND"
	CodeMonitorGroupInvalid  = "ERR_MONITOR_GROUP_INVALID"

	// Monitor templates and the bulk importer
	CodeMonitorTemplateNotFound = "ERR_MONITOR_TEMPLATE_NOT_FOUND"
	CodeMonitorTemplateInvalid  = "ERR_MONITOR_TEMPLATE_INVALID"
	CodeMonitorBulkInvalid      = "ERR_MONITOR_BULK_INVALID"

	// Notifications
	CodeNotificationNotFound = "ERR_NOTIFICATION_NOT_FOUND"
	CodeNotificationConfig   = "ERR_NOTIFICATION_CONFIG_INVALID"
	CodeNotificationSend     = "ERR_NOTIFICATION_SEND_FAILED"

	// Cluster
	CodeClusterDisabled    = "ERR_CLUSTER_DISABLED"
	CodeClusterKeyInvalid  = "ERR_CLUSTER_KEY_INVALID"
	CodeClusterJoinFailed  = "ERR_CLUSTER_JOIN_FAILED"
	CodeClusterPrimarySelf = "ERR_CLUSTER_PRIMARY_IS_SELF"
	CodeClusterStrategy    = "ERR_CLUSTER_STRATEGY_INVALID"

	// Security
	CodeIPBlocked     = "ERR_IP_BLOCKED"
	CodeIPRuleInvalid = "ERR_IP_RULE_INVALID"
	CodeTokenRequired = "ERR_TOKEN_REQUIRED"
	CodeTokenInvalid  = "ERR_TOKEN_INVALID"
	CodeTokenExpired  = "ERR_TOKEN_EXPIRED"
	CodeTokenScope    = "ERR_TOKEN_SCOPE_INSUFFICIENT"

	// Status pages
	CodeStatusPageNotFound = "ERR_STATUS_PAGE_NOT_FOUND"
	CodeStatusPageHidden   = "ERR_STATUS_PAGE_NOT_PUBLIC"
	CodeSlugTaken          = "ERR_STATUS_PAGE_SLUG_TAKEN"
)

// AllCodes lists every code, which keeps the locale files in sync: the parity
// between this list and the "errors" namespace of web/src/locales/*.json is
// checked by web/scripts/i18n-check.mjs (npm run check:i18n).
func AllCodes() []string {
	return []string{
		CodeInternal, CodeValidation, CodeInvalidPayload, CodeNotFound, CodeAlreadyExists,
		CodeDatabase, CodeForbidden, CodeRateLimited, CodeNotFoundRoute,
		CodeAuthRequired, CodeAuthInvalid, CodeAuthRateLimited, CodeAuthDomainNotAllowed,
		CodeAuthCaptchaFailed, CodeAuthCaptchaMissing, CodeAuthStateInvalid, CodeAuthProviderError,
		CodeAuthMethodDisabled,
		CodeMonitorNotFound, CodeMonitorTypeInvalid, CodeMonitorConfig,
		CodeMonitorGroupNotFound, CodeMonitorGroupInvalid,
		CodeMonitorTemplateNotFound, CodeMonitorTemplateInvalid, CodeMonitorBulkInvalid,
		CodeNotificationNotFound, CodeNotificationConfig, CodeNotificationSend,
		CodeClusterDisabled, CodeClusterKeyInvalid, CodeClusterJoinFailed, CodeClusterPrimarySelf,
		CodeClusterStrategy,
		CodeIPBlocked, CodeIPRuleInvalid, CodeTokenRequired, CodeTokenInvalid, CodeTokenExpired,
		CodeTokenScope,
		CodeStatusPageNotFound, CodeStatusPageHidden, CodeSlugTaken,
	}
}
