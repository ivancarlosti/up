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

// Leader derivation (CLUSTER_LEADER_ELECTION) for federated mode, where `nodes.is_primary`
// does not exist because there is no shared row to hold it.
const (
	// ClusterLeaderLowestID makes the leader the settled node with the smallest
	// node_id. Every node computes the same winner from its own view.
	ClusterLeaderLowestID = "lowest_id"
	// ClusterLeaderExplicit pins the leader to CLUSTER_LEADER_NODE_ID.
	ClusterLeaderExplicit = "explicit"
)

// Notification election (CLUSTER_NOTIFY_ELECTION) for federated mode, where the
// `notification_locks` unique index cannot elect a single sender any more.
const (
	// NotifyElectionLeader gives the duty to the derived leader: at most one alert per
	// transition, and alerting pauses for one settle period when that node dies.
	NotifyElectionLeader = "leader"
	// NotifyElectionHash spreads the duty per monitor with rendezvous hashing over the
	// monitor uuid. A split view can still double-send, so the clocks must agree.
	NotifyElectionHash = "hash"
	// NotifyElectionOrigin gives every monitor to the node that created it: no
	// duplicates from a split view, at the cost of a single alerting point.
	NotifyElectionOrigin = "origin"
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
	// --- Synchronisation (federated mode) ---------------------------------
	// ClusterSyncSeconds is how often a node pulls the changes of each peer.
	ClusterSyncSeconds int
	// ClusterSyncBatch caps how many changes one pull may carry.
	ClusterSyncBatch int
	// ClusterSyncManifestSeconds is how often the checksums are compared to
	// detect a divergence the incremental path missed.
	ClusterSyncManifestSeconds int
	// ClusterSyncTombstoneDays is how long a deleted identity is remembered. A
	// peer whose cursor points before the pruned region falls back to a full
	// snapshot.
	ClusterSyncTombstoneDays int
	// ClusterSyncNotifications synchronises the delivery channels and their
	// links. It defaults to ON in federated mode, because the node that owns the
	// notification election is the one that sends: a leader without the channel
	// would drop the alert silently. The payload carries credentials, so the
	// peers must run over TLS.
	ClusterSyncNotifications bool
	// ClusterLeaderMode is how the leader is derived when there is no shared row
	// (CLUSTER_LEADER_ELECTION): lowest_id (the settled node with the smallest
	// node_id) or explicit (CLUSTER_LEADER_NODE_ID). It only applies in federated
	// mode. `hash` is deliberately NOT a leader mode: IsPrimary() is one boolean for
	// the whole node, while hash ownership is per monitor, so it belongs to the
	// notification election (CLUSTER_NOTIFY_ELECTION).
	ClusterLeaderMode string
	// ClusterLeaderNodeID pins the leader when ClusterLeaderMode is explicit.
	ClusterLeaderNodeID string
	// ClusterNotifyElection decides which node alerts in federated mode: leader
	// (the derived leader), hash (rendezvous over the monitor uuid) or origin (the
	// node that created the monitor). See docs/clustering-federated.md, section 8.
	ClusterNotifyElection string
	NodeID                string
	NodeName              string
	ClusterPrivateKey     string

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
		ClusterLeaderMode:             strings.ToLower(strings.TrimSpace(env("CLUSTER_LEADER_ELECTION", ClusterLeaderLowestID))),
		ClusterLeaderNodeID:           strings.TrimSpace(env("CLUSTER_LEADER_NODE_ID", "")),
		ClusterNotifyElection:         strings.ToLower(strings.TrimSpace(env("CLUSTER_NOTIFY_ELECTION", NotifyElectionLeader))),
		ClusterSyncSeconds:            envInt("CLUSTER_SYNC_SECONDS", 15),
		ClusterSyncBatch:              envInt("CLUSTER_SYNC_BATCH", 500),
		ClusterSyncManifestSeconds:    envInt("CLUSTER_SYNC_MANIFEST_SECONDS", 600),
		ClusterSyncTombstoneDays:      envInt("CLUSTER_SYNC_TOMBSTONE_DAYS", 30),
		ClusterSyncNotifications:      mustBool("CLUSTER_SYNC_NOTIFICATIONS", false),
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

	// The channel synchronisation default depends on the mode: federated mode
	// needs every node to hold the channels, because the leader is the one that
	// sends and a leader without the channel would drop the alert silently. An
	// explicit value always wins.
	if raw := strings.TrimSpace(os.Getenv("CLUSTER_SYNC_NOTIFICATIONS")); raw == "" {
		cfg.ClusterSyncNotifications = cfg.ClusterMode == ClusterModeFederated
	}

	cfg.AppTrustProxy = mustBool("APP_TRUST_PROXY", false)
	cfg.DBSSL = mustBool("DB_SSL", false)

	if problems := cfg.validate(); len(problems) > 0 {
		return nil, &ValidationError{Problems: problems}
	}
	return cfg, nil
}
