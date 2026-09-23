// Package config loads and validates every environment variable of Up.
//
// The loader is intentionally strict: on boot the server prints a complete,
// human readable report of every missing or invalid variable and refuses to
// start, instead of failing later in a confusing way.
package config

import (
	"os"
	"strings"

	"github.com/joho/godotenv"
)

// AuthMethod selects how visitors authenticate against the dashboard.
type AuthMethod string

// Supported authentication methods (AUTH_METHOD).
const (
	AuthMethodNone     AuthMethod = "none"
	AuthMethodAccount  AuthMethod = "account"
	AuthMethodKeycloak AuthMethod = "keycloak"
)

// Cluster modes (CLUSTER_MODE).
const (
	// ClusterModeShared is the only mode implemented today: every node of the
	// cluster points at the SAME external MariaDB/MySQL database, which is what
	// keeps monitors, heartbeats, the aggregated state and the notification
	// lock synchronised without any message broker.
	ClusterModeShared = "shared"
	// ClusterModeFederated gives every node its own database and synchronises
	// the configuration over the peer API. It is specified, not implemented:
	// see docs/clustering-federated.md.
	ClusterModeFederated = "federated"
)

// SupportedLocales and SupportedThemes are the values accepted by
// DEFAULT_LOCALE / DEFAULT_THEME and by the Admin > Settings page.
var (
	SupportedLocales = []string{"en-US", "pt-BR", "es-MX"}
	SupportedThemes  = []string{"system", "light", "dark"}
)

// Config is the fully validated runtime configuration.
type Config struct {
	// --- App & reverse proxy ---------------------------------------------
	AppURL         string
	AppTrustProxy  bool
	TrustedProxies []string // CIDRs handed over to gin.SetTrustedProxies
	AppPort        int

	// --- Database (always external) --------------------------------------
	DBHost     string
	DBPort     int
	DBDatabase string
	DBUsername string
	DBPassword string
	DBSSL      bool

	// --- Authentication ---------------------------------------------------
	AuthMethod            AuthMethod
	AccountLogin          string
	AccountPassword       string
	RecaptchaClientID     string
	RecaptchaClientSecret string
	RecaptchaEnabled      bool

	KeycloakBaseURL               string
	KeycloakRealm                 string
	KeycloakClientID              string
	KeycloakClientSecret          string
	KeycloakRedirectURI           string
	KeycloakPostLogoutRedirectURI string
	KeycloakAccounts              []string
	KeycloakIssuer                string
	KeycloakCallbackPath          string

	// --- I18n & theme defaults -------------------------------------------
	DefaultLocale string
	DefaultTheme  string

	// --- Clustering -------------------------------------------------------
	ClusterEnabled bool
	// ClusterMode is CLUSTER_MODE: "shared" (one database for every node) or
	// "federated" (one database per node). Only "shared" is implemented; the
	// variable exists so an operator cannot silently configure a mode that is
	// not there yet.
	ClusterMode string
	// ClusterPeerAPI turns on the signed peer API (the /api/cluster/sync/...
	// endpoints) and the peer ping loop.
	//
	// It is deliberately independent of CLUSTER_MODE: the peer API can be
	// enabled in shared mode, where it cross-checks liveness over HTTP instead of
	// trusting a shared row. Federated mode will require it.
	ClusterPeerAPI bool
	// ClusterLeaderSettleSeconds is how long a peer must be continuously
	// reachable before it may take the leader role (and, later, the notification
	// duty) from the current holder. It stops a peer blip or a rejoining node
	// from taking over mid-incident.
	ClusterLeaderSettleSeconds int
	NodeID                     string
	NodeName                   string
	ClusterPrivateKey          string

	// --- Optional tuning --------------------------------------------------
	LogLevel string
	// SchedulerMaxConcurrent caps the heartbeats executed at the same instant.
	SchedulerMaxConcurrent int
	// SchedulerReconcileSeconds is how often each node compares its running
	// monitor workers with the database. It is what makes a monitor created or
	// deleted on another node of the cluster start (or stop) here, since the
	// CRUD handlers can only touch the process that served the request.
	SchedulerReconcileSeconds int
	HeartbeatRetentionDays    int
	// NotificationLogRetentionDays purges the delivery history
	// (notification_logs). 0 keeps every entry, the same convention as
	// HeartbeatRetentionDays: retention is opt-in so nothing is deleted behind
	// the operator's back.
	NotificationLogRetentionDays int
	SessionTTLHours              int
	SecurityBypassIPRules        bool
	SecurityLoginRateLimit       int
	SecurityPublicRateLimit      int
}

// Load reads the environment (optionally from a .env file), validates it and
// returns the configuration. Every problem found is reported at once.
func Load() (*Config, error) {
	// A missing .env file is perfectly fine (variables can also come from the
	// container environment); parse problems are reported by the caller.
	if _, err := os.Stat(".env"); err == nil {
		_ = godotenv.Load(".env")
	}

	cfg := &Config{
		AppURL:                trimURL(env("APP_URL", "http://localhost:3000")),
		AppPort:               envInt("APP_PORT", 3000),
		DBHost:                env("DB_HOST", ""),
		DBPort:                envInt("DB_PORT", 3306),
		DBDatabase:            env("DB_DATABASE", ""),
		DBUsername:            env("DB_USERNAME", ""),
		DBPassword:            env("DB_PASSWORD", ""),
		AuthMethod:            AuthMethod(strings.ToLower(env("AUTH_METHOD", string(AuthMethodAccount)))),
		AccountLogin:          env("ACCOUNT_LOGIN", ""),
		AccountPassword:       env("ACCOUNT_PASSWORD", ""),
		RecaptchaClientID:     env("RECAPTCHA_CLIENTID", ""),
		RecaptchaClientSecret: env("RECAPTCHA_CLIENTSECRET", ""),
		KeycloakBaseURL:       trimURL(env("KEYCLOAK_BASE_URL", "")),
		KeycloakRealm:         env("KEYCLOAK_REALM", ""),
		KeycloakClientID:      env("KEYCLOAK_CLIENT_ID", ""),
		KeycloakClientSecret:  env("KEYCLOAK_CLIENT_SECRET", ""),
		KeycloakRedirectURI:   strings.TrimSpace(env("KEYCLOAK_REDIRECT_URI", "")),
		// The default of KEYCLOAK_POST_LOGOUT_REDIRECT_URI depends on APP_URL,
		// so it is resolved in validate.go.
		KeycloakPostLogoutRedirectURI: strings.TrimSpace(env("KEYCLOAK_POST_LOGOUT_REDIRECT_URI", "")),
		DefaultLocale:                 env("DEFAULT_LOCALE", "en-US"),
		DefaultTheme:                  env("DEFAULT_THEME", "system"),
		ClusterEnabled:                mustBool("CLUSTER_ENABLED", false),
		ClusterMode:                   strings.ToLower(env("CLUSTER_MODE", ClusterModeShared)),
		ClusterPeerAPI:                mustBool("CLUSTER_PEER_API", false),
		ClusterLeaderSettleSeconds:    envInt("CLUSTER_LEADER_SETTLE_SECONDS", 60),
		NodeID:                        env("NODE_ID", "up-node-1"),
		NodeName:                      env("NODE_NAME", "Primary Node"),
		ClusterPrivateKey:             strings.TrimSpace(env("CLUSTER_PRIVATE_KEY", "")),
		LogLevel:                      strings.ToLower(env("LOG_LEVEL", "info")),
		SchedulerMaxConcurrent:        envInt("SCHEDULER_MAX_CONCURRENT", 20),
		SchedulerReconcileSeconds:     envInt("SCHEDULER_RECONCILE_SECONDS", 30),
		HeartbeatRetentionDays:        envInt("HEARTBEAT_RETENTION_DAYS", 0),
		NotificationLogRetentionDays:  envInt("NOTIFICATION_LOG_RETENTION_DAYS", 0),
		SessionTTLHours:               envInt("SESSION_TTL_HOURS", 720),
		SecurityBypassIPRules:         mustBool("SECURITY_BYPASS_IP_RULES", false),
		SecurityLoginRateLimit:        envInt("SECURITY_LOGIN_RATE_LIMIT", 20),
		SecurityPublicRateLimit:       envInt("SECURITY_PUBLIC_RATE_LIMIT", 240),
	}

	cfg.AppTrustProxy = mustBool("APP_TRUST_PROXY", false)
	cfg.DBSSL = mustBool("DB_SSL", false)

	if problems := cfg.validate(); len(problems) > 0 {
		return nil, &ValidationError{Problems: problems}
	}
	return cfg, nil
}
