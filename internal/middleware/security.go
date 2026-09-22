package middleware

import (
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/ivancarlosti/up/internal/api"
	"github.com/ivancarlosti/up/internal/config"
	"github.com/ivancarlosti/up/internal/i18n"
	"github.com/ivancarlosti/up/internal/models"
	"github.com/ivancarlosti/up/internal/services"
)

// IPFilter enforces the ip_rules table for a given scope. With no rules at all
// every address is allowed, which is the out of the box behaviour.
func IPFilter(rules *services.IPRuleService, scope models.IPRuleScope) gin.HandlerFunc {
	return func(c *gin.Context) {
		ip := ClientIP(c)
		allowed, reason := rules.Decision(c.Request.Context(), ip, scope)
		if !allowed {
			api.WriteError(c, http.StatusForbidden, i18n.CodeIPBlocked, reason)
			return
		}
		c.Next()
	}
}

// RequireSession protects the admin API. With AUTH_METHOD=none every request is
// accepted as the anonymous administrator (see SessionService.CurrentIdentity).
func RequireSession(sessions *services.SessionService) gin.HandlerFunc {
	return func(c *gin.Context) {
		identity, ok := sessions.CurrentIdentity(c.Request.Context(), c.Request)
		if !ok {
			api.Unauthorized(c, "authentication required")
			return
		}
		c.Set(CtxIdentity, identity)
		c.Next()
	}
}

// RequireToken protects the public REST API (/api/v1) with a bearer token.
func RequireToken(tokens *services.TokenService, scope models.TokenScope) gin.HandlerFunc {
	return func(c *gin.Context) {
		token, err := tokens.Authenticate(c.Request.Context(), bearerToken(c), ClientIP(c), scope)
		if err != nil {
			api.WriteServiceError(c, err)
			return
		}
		c.Set(CtxToken, token)
		c.Next()
	}
}

// ---------------------------------------------------------------------------
// Rate limiting
// ---------------------------------------------------------------------------

// rateLimiter is a tiny in-memory token bucket keyed by client address. It is
// enough to blunt brute force attempts against the login endpoint and the
// public API without adding an external dependency.
type rateLimiter struct {
	mu      sync.Mutex
	buckets map[string]*bucket
	limit   int
	window  time.Duration
	lastGC  time.Time
}

type bucket struct {
	count   int
	resetAt time.Time
}

// RateLimit allows at most limit requests per minute per client address.
func RateLimit(limit int, window time.Duration) gin.HandlerFunc {
	if limit <= 0 {
		return func(c *gin.Context) { c.Next() }
	}
	if window <= 0 {
		window = time.Minute
	}
	limiter := &rateLimiter{
		buckets: map[string]*bucket{},
		limit:   limit,
		window:  window,
		lastGC:  time.Now(),
	}

	return func(c *gin.Context) {
		if !limiter.allow(ClientIP(c)) {
			c.Header("Retry-After", "60")
			api.WriteError(c, http.StatusTooManyRequests, i18n.CodeRateLimited,
				"too many requests, please try again later")
			return
		}
		c.Next()
	}
}

// allow consumes one token for the address.
func (l *rateLimiter) allow(key string) bool {
	now := time.Now()

	l.mu.Lock()
	defer l.mu.Unlock()

	if now.Sub(l.lastGC) > 5*time.Minute {
		for address, entry := range l.buckets {
			if now.After(entry.resetAt) {
				delete(l.buckets, address)
			}
		}
		l.lastGC = now
	}

	entry, ok := l.buckets[key]
	if !ok || now.After(entry.resetAt) {
		l.buckets[key] = &bucket{count: 1, resetAt: now.Add(l.window)}
		return true
	}
	if entry.count >= l.limit {
		return false
	}
	entry.count++
	return true
}

// TrustedProxyConfig translates APP_TRUST_PROXY into gin trusted proxies.
func TrustedProxyConfig(cfg *config.Config, engine *gin.Engine) {
	if !cfg.AppTrustProxy {
		_ = engine.SetTrustedProxies(nil)
		return
	}
	proxies := cfg.TrustedProxies
	if len(proxies) == 0 {
		proxies = []string{"0.0.0.0/0", "::/0"}
	}
	_ = engine.SetTrustedProxies(proxies)
}

// contextWithTimeout is a helper for middlewares that call the database.
func contextWithTimeout(parent context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(parent, 5*time.Second)
}
