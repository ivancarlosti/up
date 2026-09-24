package config

import (
	"fmt"
	"os"
	"strings"
)

// validate checks every rule described in docker/.env.example and returns the
// complete list of problems (empty means the configuration is usable).
func (c *Config) validate() []string {
	var problems []string

	// Boolean typo detection: true/false/yes/no/1/0/on/off are accepted,
	// anything else that is neither empty nor a boolean token is a mistake.
	for _, key := range []string{"APP_TRUST_PROXY", "DB_SSL", "CLUSTER_ENABLED", "CLUSTER_PEER_API", "CLUSTER_SYNC_NOTIFICATIONS", "SECURITY_BYPASS_IP_RULES"} {
		if raw := strings.TrimSpace(os.Getenv(key)); raw != "" && !isBoolToken(raw) {
			problems = append(problems, fmt.Sprintf("%s must be true or false, got %q", key, raw))
		}
	}

	// APP_TRUST_PROXY also accepts a comma separated list of CIDRs, which is
	// the recommended setting when only part of the traffic is proxied.
	rawProxy := strings.TrimSpace(os.Getenv("APP_TRUST_PROXY"))
	switch {
	case c.AppTrustProxy && (rawProxy == "" || isBoolToken(rawProxy)):
		c.TrustedProxies = []string{"0.0.0.0/0", "::/0"}
	case rawProxy != "" && !isBoolToken(rawProxy):
		for _, cidr := range strings.Split(rawProxy, ",") {
			if cidr = strings.TrimSpace(cidr); cidr != "" {
				c.TrustedProxies = append(c.TrustedProxies, cidr)
			}
		}
		if len(c.TrustedProxies) > 0 {
			c.AppTrustProxy = true
		} else {
			problems = append(problems, "APP_TRUST_PROXY must be true, false or a list of CIDRs")
		}
	}

	// --- App ---------------------------------------------------------------
	if !isAbsHTTPURL(c.AppURL) {
		problems = append(problems, fmt.Sprintf("APP_URL must be a valid absolute http(s) URL, got %q", c.AppURL))
	}
	if c.AppPort < 1 || c.AppPort > 65535 {
		problems = append(problems, fmt.Sprintf("APP_PORT must be between 1 and 65535, got %d", c.AppPort))
	}

	// --- Database ----------------------------------------------------------
	if c.DBHost == "" {
		problems = append(problems, "DB_HOST is required (use host.docker.internal when running in Docker)")
	}
	if c.DBDatabase == "" {
		problems = append(problems, "DB_DATABASE is required")
	}
	if c.DBUsername == "" {
		problems = append(problems, "DB_USERNAME is required")
	}
	if c.DBPort < 1 || c.DBPort > 65535 {
		problems = append(problems, fmt.Sprintf("DB_PORT must be between 1 and 65535, got %d", c.DBPort))
	}

	// --- Authentication ----------------------------------------------------
	c.RecaptchaEnabled = c.RecaptchaClientID != "" && c.RecaptchaClientSecret != ""
	switch c.AuthMethod {
	case AuthMethodNone:
		// Nothing to validate: the dashboard is fully open.
	case AuthMethodAccount:
		if c.AccountLogin == "" {
			problems = append(problems, "ACCOUNT_LOGIN is required when AUTH_METHOD=account")
		}
		if c.AccountPassword == "" {
			problems = append(problems, "ACCOUNT_PASSWORD is required when AUTH_METHOD=account")
		}
	case AuthMethodKeycloak:
		for _, pair := range [][2]string{
			{"KEYCLOAK_BASE_URL", c.KeycloakBaseURL},
			{"KEYCLOAK_REALM", c.KeycloakRealm},
			{"KEYCLOAK_CLIENT_ID", c.KeycloakClientID},
			{"KEYCLOAK_CLIENT_SECRET", c.KeycloakClientSecret},
		} {
			if pair[1] == "" {
				problems = append(problems, pair[0]+" is required when AUTH_METHOD=keycloak")
			}
		}
		c.KeycloakAccounts = splitAccounts(env("KEYCLOAK_ACCOUNTS", ""))
		if len(c.KeycloakAccounts) == 0 {
			problems = append(problems,
				"KEYCLOAK_ACCOUNTS is required when AUTH_METHOD=keycloak (e.g. admin@example.com @empresa.com domain2.com)")
		}
		if c.KeycloakBaseURL != "" && !isAbsHTTPURL(c.KeycloakBaseURL) {
			problems = append(problems, fmt.Sprintf("KEYCLOAK_BASE_URL must be a valid absolute http(s) URL, got %q", c.KeycloakBaseURL))
		}
		if c.KeycloakBaseURL != "" && c.KeycloakRealm != "" {
			c.KeycloakIssuer = c.KeycloakBaseURL + "/realms/" + c.KeycloakRealm
		}
		c.KeycloakCallbackPath = "/api/auth/callback"
		if c.KeycloakRedirectURI == "" {
			// Derived from APP_URL exactly as required by the specification.
			c.KeycloakRedirectURI = c.AppURL + c.KeycloakCallbackPath
		}
		if !isAbsHTTPURL(c.KeycloakRedirectURI) {
			problems = append(problems, fmt.Sprintf("KEYCLOAK_REDIRECT_URI must be a valid absolute http(s) URL, got %q", c.KeycloakRedirectURI))
		}
		if c.KeycloakPostLogoutRedirectURI == "" {
			// Where the provider sends the browser back after the logout: the
			// SPA login screen (see handlers/oidcLogout).
			c.KeycloakPostLogoutRedirectURI = c.AppURL + "/login"
		}
		if !isAbsHTTPURL(c.KeycloakPostLogoutRedirectURI) {
			problems = append(problems, fmt.Sprintf("KEYCLOAK_POST_LOGOUT_REDIRECT_URI must be a valid absolute http(s) URL, got %q", c.KeycloakPostLogoutRedirectURI))
		}
	default:
		problems = append(problems, fmt.Sprintf("AUTH_METHOD must be none, account or keycloak, got %q", c.AuthMethod))
	}

	// --- Locale & theme ----------------------------------------------------
	if !contains(SupportedLocales, c.DefaultLocale) {
		problems = append(problems, fmt.Sprintf("DEFAULT_LOCALE must be one of %s, got %q",
			strings.Join(SupportedLocales, ", "), c.DefaultLocale))
	}
	if !contains(SupportedThemes, c.DefaultTheme) {
		problems = append(problems, fmt.Sprintf("DEFAULT_THEME must be one of %s, got %q",
			strings.Join(SupportedThemes, ", "), c.DefaultTheme))
	}

	// --- Clustering --------------------------------------------------------
	switch c.ClusterMode {
	case ClusterModeShared:
		// The shared database does the synchronising for us.
	case ClusterModeFederated:
		if !c.ClusterEnabled {
			problems = append(problems, "CLUSTER_MODE=federated requires CLUSTER_ENABLED=true")
		}
		// The peer API is what makes the mode real: it is how a node publishes what it
		// changed and how it pulls what the others changed. A federated node without it
		// would claim to be part of a cluster, take part in nothing, and diverge in
		// silence — which is the failure this mode exists to avoid, so it is refused at
		// boot rather than warned about (docs/clustering-modes.md, section 20).
		if !c.ClusterPeerAPI {
			problems = append(problems,
				"CLUSTER_MODE=federated requires CLUSTER_PEER_API=true")
		}
		// NODE_ID is not checked here: it is already required by
		// CLUSTER_ENABLED=true just below, and federated mode requires the
		// clustering to be enabled. Reporting it twice would only add noise.
	default:
		problems = append(problems, fmt.Sprintf("CLUSTER_MODE must be %s or %s, got %q",
			ClusterModeShared, ClusterModeFederated, c.ClusterMode))
	}

	if c.ClusterEnabled {
		if strings.TrimSpace(c.NodeID) == "" {
			problems = append(problems, "NODE_ID is required when CLUSTER_ENABLED=true")
		}
		if strings.TrimSpace(c.NodeName) == "" {
			c.NodeName = c.NodeID
		}
	}
	// The peer API is node to node only, so it needs the cluster identity the
	// signature is built on.
	if c.ClusterPeerAPI {
		if !c.ClusterEnabled {
			problems = append(problems, "CLUSTER_PEER_API=true requires CLUSTER_ENABLED=true")
		}
		if strings.TrimSpace(c.NodeID) == "" {
			problems = append(problems, "CLUSTER_PEER_API=true requires NODE_ID (it signs every peer request)")
		}
	}
	// A negative settle time would mean "trust instantly", which is the documented
	// default value: read it as 0 rather than refusing to boot.
	if c.ClusterLeaderSettleSeconds < 0 {
		c.ClusterLeaderSettleSeconds = 0
	}
	// The federated leadership and the notification election are derived from the local
	// view, so a typo in either must fail loudly: falling back silently would leave a
	// cluster that believes it is alerting while nobody owns anything, or two nodes that
	// each believe they do.
	switch c.ClusterLeaderMode {
	case ClusterLeaderLowestID:
	case ClusterLeaderExplicit:
		if strings.TrimSpace(c.ClusterLeaderNodeID) == "" {
			problems = append(problems, "CLUSTER_LEADER_ELECTION=explicit requires CLUSTER_LEADER_NODE_ID")
		}
	default:
		problems = append(problems, "CLUSTER_LEADER_ELECTION must be "+ClusterLeaderLowestID+" or "+ClusterLeaderExplicit)
	}
	switch c.ClusterNotifyElection {
	case NotifyElectionLeader, NotifyElectionHash, NotifyElectionOrigin:
	default:
		problems = append(problems,
			"CLUSTER_NOTIFY_ELECTION must be "+NotifyElectionLeader+", "+NotifyElectionHash+" or "+NotifyElectionOrigin)
	}

	// --- Synchronisation tuning --------------------------------------------
	// The floors mirror the ones used elsewhere: a pull faster than a few seconds
	// would only add load, a huge batch would stall a peer, and a manifest pass
	// more often than every minute is noise.
	if c.ClusterSyncSeconds < 5 {
		c.ClusterSyncSeconds = 5
	}
	if c.ClusterSyncBatch < 1 {
		c.ClusterSyncBatch = 500
	}
	if c.ClusterSyncBatch > 5000 {
		c.ClusterSyncBatch = 5000
	}
	if c.ClusterSyncManifestSeconds < 60 {
		c.ClusterSyncManifestSeconds = 60
	}
	if c.ClusterSyncTombstoneDays < 1 {
		c.ClusterSyncTombstoneDays = 30
	}
	if strings.ContainsAny(c.NodeID, " \t/\\") {
		problems = append(problems, fmt.Sprintf("NODE_ID must not contain spaces or slashes, got %q", c.NodeID))
	}

	// --- Tuning ------------------------------------------------------------
	if !contains([]string{"debug", "info", "warn", "error"}, c.LogLevel) {
		problems = append(problems, fmt.Sprintf("LOG_LEVEL must be debug, info, warn or error, got %q", c.LogLevel))
	}
	if c.SchedulerMaxConcurrent < 1 {
		c.SchedulerMaxConcurrent = 1
	}
	// The reconciliation loop compares the workers with the database: below a
	// few seconds it would only add database load.
	if c.SchedulerReconcileSeconds < 5 {
		c.SchedulerReconcileSeconds = 5
	}
	if c.SessionTTLHours < 1 {
		c.SessionTTLHours = 720
	}
	// A negative retention would mean "delete everything": read it as the
	// documented default, which keeps the history.
	if c.NotificationLogRetentionDays < 0 {
		c.NotificationLogRetentionDays = 0
	}
	if c.SecurityLoginRateLimit < 1 {
		c.SecurityLoginRateLimit = 20
	}
	if c.SecurityPublicRateLimit < 1 {
		c.SecurityPublicRateLimit = 240
	}

	return problems
}

// ValidationError groups every environment problem detected at boot.
type ValidationError struct {
	Problems []string
}

// Error renders a readable multi-line report.
func (e *ValidationError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%d environment problem(s) found:\n", len(e.Problems))
	for i, p := range e.Problems {
		fmt.Fprintf(&b, "  %d. %s\n", i+1, p)
	}
	b.WriteString("\nFix the .env file (docker/.env.example is the complete reference) and start the server again.")
	return b.String()
}
