package config

import (
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
)

// ---------------------------------------------------------------------------
// environment helpers
// ---------------------------------------------------------------------------

func env(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok {
		return strings.TrimSpace(v)
	}
	return fallback
}

func envInt(key string, fallback int) int {
	raw := env(key, "")
	if raw == "" {
		return fallback
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		return fallback
	}
	return v
}

// mustBool parses the boolean tokens accepted by the .env files. Empty values
// fall back to the default; non boolean values are reported by validate().
func mustBool(key string, fallback bool) bool {
	raw := strings.ToLower(env(key, ""))
	switch raw {
	case "":
		return fallback
	case "true", "yes", "1", "on":
		return true
	case "false", "no", "0", "off":
		return false
	}
	return fallback
}

func isBoolToken(raw string) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "true", "yes", "1", "on", "false", "no", "0", "off":
		return true
	}
	return false
}

func trimURL(raw string) string {
	return strings.TrimRight(strings.TrimSpace(raw), "/")
}

func contains(list []string, value string) bool {
	for _, item := range list {
		if item == value {
			return true
		}
	}
	return false
}

func isAbsHTTPURL(raw string) bool {
	if raw == "" {
		return false
	}
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	return u.Host != "" && (u.Scheme == "http" || u.Scheme == "https")
}

// splitAccounts parses KEYCLOAK_ACCOUNTS into normalized, lower case entries.
// Accepted forms (space separated):
//
//	you@example.com   exact address
//	@empresa.com      every address of the domain
//	empresa.com       same as above, "@" is optional
//	domain2.com       additional domains can be mixed with exact addresses
func splitAccounts(raw string) []string {
	out := make([]string, 0, 4)
	for _, entry := range strings.Fields(raw) {
		entry = strings.ToLower(strings.TrimSpace(entry))
		if entry == "" {
			continue
		}
		if strings.HasPrefix(entry, "@") {
			entry = entry[1:]
		}
		entry = strings.TrimPrefix(entry, "*.")
		if entry == "" {
			continue
		}
		out = append(out, entry)
	}
	return out
}

// ---------------------------------------------------------------------------
// derived values
// ---------------------------------------------------------------------------

// DSN builds the MariaDB/MySQL connection string used by GORM.
//
// Note: a password containing "@" or "/" must be URL encoded in DB_PASSWORD.
func (c *Config) DSN() string {
	params := "charset=utf8mb4&parseTime=true&loc=UTC&timeout=10s&readTimeout=30s&writeTimeout=30s"
	if c.DBSSL {
		params += "&tls=true"
	}
	return fmt.Sprintf("%s:%s@tcp(%s:%d)/%s?%s",
		c.DBUsername, c.DBPassword, c.DBHost, c.DBPort, c.DBDatabase, params)
}

// SafeDSN is the DSN without credentials, safe for logs.
func (c *Config) SafeDSN() string {
	return fmt.Sprintf("%s@tcp(%s:%d)/%s", c.DBUsername, c.DBHost, c.DBPort, c.DBDatabase)
}

// SecureCookies reports whether session cookies must carry the Secure flag
// (true whenever APP_URL uses https).
func (c *Config) SecureCookies() bool {
	return strings.HasPrefix(c.AppURL, "https://")
}

// CookieDomain is empty, which means "the host that served the request". It
// exists so the session code has a single place to look at.
func (c *Config) CookieDomain() string { return "" }

// AuthEnabled reports whether the dashboard requires a login.
func (c *Config) AuthEnabled() bool { return c.AuthMethod != AuthMethodNone }

// Summary renders the boot banner (never contains secrets).
func (c *Config) Summary() string {
	parts := []string{
		fmt.Sprintf("app_url=%s", c.AppURL),
		fmt.Sprintf("port=%d", c.AppPort),
		fmt.Sprintf("trust_proxy=%t", c.AppTrustProxy),
		fmt.Sprintf("database=%s", c.SafeDSN()),
		fmt.Sprintf("db_ssl=%t", c.DBSSL),
		fmt.Sprintf("auth=%s", c.AuthMethod),
		fmt.Sprintf("locale=%s", c.DefaultLocale),
		fmt.Sprintf("theme=%s", c.DefaultTheme),
		fmt.Sprintf("cluster=%t", c.ClusterEnabled),
		fmt.Sprintf("retention_days=%d", c.HeartbeatRetentionDays),
	}
	if c.ClusterEnabled {
		parts = append(parts, fmt.Sprintf("node_id=%s", c.NodeID), fmt.Sprintf("node_name=%q", c.NodeName))
	}
	return strings.Join(parts, " ")
}
