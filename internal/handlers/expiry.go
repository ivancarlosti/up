package handlers

import (
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/ivancarlosti/up/internal/api"
	"github.com/ivancarlosti/up/internal/expiry"
	"github.com/ivancarlosti/up/internal/i18n"
	"github.com/ivancarlosti/up/internal/models"
	"github.com/ivancarlosti/up/internal/services"
)

// expirySettings returns the daily expiry job configuration, the TLD rules and
// when the next run is due.
func (h *Container) expirySettings(c *gin.Context) {
	if h.Expiry == nil || h.WhoisParsers == nil {
		api.WriteError(c, http.StatusServiceUnavailable, i18n.CodeInternal, "the expiry job is not wired")
		return
	}
	settings := h.Expiry.Settings()
	parsers, err := h.WhoisParsers.All(c.Request.Context())
	if err != nil {
		api.WriteServiceError(c, err)
		return
	}
	next := models.NextDailyRun(time.Now().UTC(), settings.CheckTime, settings.CheckTimezone)
	api.OK(c, gin.H{
		"settings":     settings,
		"parsers":      parsers,
		"defaults":     models.DefaultExpirySettings(),
		"last_run_day": h.Settings.ExpiryLastRunDay(),
		"next_run":     next,
	})
}

// updateExpirySettings stores the daily expiry job configuration.
func (h *Container) updateExpirySettings(c *gin.Context) {
	var payload models.ExpirySettings
	if !bindJSON(c, &payload) {
		return
	}
	if problem := payload.Validate(); problem != "" {
		api.WriteError(c, http.StatusBadRequest, i18n.CodeValidation, problem)
		return
	}
	if err := h.Settings.SetExpirySettings(c.Request.Context(), payload); err != nil {
		api.WriteServiceError(c, err)
		return
	}
	if h.Expiry != nil {
		api.OK(c, h.Expiry.Settings())
		return
	}
	api.OK(c, payload)
}

// runExpiryNow refreshes every deduplicated certificate and domain target and
// evaluates the reminders, without waiting for the daily schedule.
func (h *Container) runExpiryNow(c *gin.Context) {
	if h.Expiry == nil {
		api.WriteError(c, http.StatusServiceUnavailable, i18n.CodeInternal, "the expiry job is not wired")
		return
	}
	if err := h.Expiry.RunNow(c.Request.Context()); err != nil {
		api.WriteServiceError(c, err)
		return
	}
	api.OK(c, gin.H{"ran": true, "at": time.Now().UTC()})
}

// expiryTargets lists the deduplicated worklist of the daily job, with the
// observation stored for each monitor (what a run would look up, and when it was
// last refreshed).
func (h *Container) expiryTargets(c *gin.Context) {
	if h.Expiry == nil {
		api.WriteError(c, http.StatusServiceUnavailable, i18n.CodeInternal, "the expiry job is not wired")
		return
	}
	targets, err := h.Expiry.Targets(c.Request.Context())
	if err != nil {
		api.WriteServiceError(c, err)
		return
	}
	api.OK(c, gin.H{"targets": targets, "count": len(targets)})
}

// refreshExpiryTarget refreshes a single target of the worklist ("check now" for
// one endpoint or one domain) and reports how many monitors it updated.
func (h *Container) refreshExpiryTarget(c *gin.Context) {
	if h.Expiry == nil {
		api.WriteError(c, http.StatusServiceUnavailable, i18n.CodeInternal, "the expiry job is not wired")
		return
	}
	var payload struct {
		Kind   string `json:"kind"`
		Target string `json:"target"`
	}
	if !bindJSON(c, &payload) {
		return
	}
	refreshed, err := h.Expiry.RunTarget(c.Request.Context(), services.ExpiryTargetKind(payload.Kind), payload.Target)
	if err != nil {
		api.WriteServiceError(c, err)
		return
	}
	api.OK(c, gin.H{
		"ran": true, "kind": payload.Kind, "target": payload.Target,
		"monitors": refreshed, "at": time.Now().UTC(),
	})
}

// listWhoisParsers returns every configured per-TLD rule.
func (h *Container) listWhoisParsers(c *gin.Context) {
	parsers, err := h.WhoisParsers.All(c.Request.Context())
	if err != nil {
		api.WriteServiceError(c, err)
		return
	}
	api.OK(c, parsers)
}

// createWhoisParser stores a new rule.
func (h *Container) createWhoisParser(c *gin.Context) {
	var parser models.WhoisParser
	if !bindJSON(c, &parser) {
		return
	}
	created, err := h.WhoisParsers.Create(c.Request.Context(), &parser)
	if err != nil {
		api.WriteServiceError(c, err)
		return
	}
	api.Created(c, created)
}

// updateWhoisParser saves an existing rule.
func (h *Container) updateWhoisParser(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	var parser models.WhoisParser
	if !bindJSON(c, &parser) {
		return
	}
	updated, err := h.WhoisParsers.Update(c.Request.Context(), id, &parser)
	if err != nil {
		api.WriteServiceError(c, err)
		return
	}
	api.OK(c, updated)
}

// deleteWhoisParser removes a rule.
func (h *Container) deleteWhoisParser(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	if err := h.WhoisParsers.Delete(c.Request.Context(), id); err != nil {
		api.WriteServiceError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// resetWhoisParsers restores the built-in TLD rules, erasing every edit the
// operator made (the confirmation alert of the admin page is what asks for it).
func (h *Container) resetWhoisParsers(c *gin.Context) {
	if h.WhoisParsers == nil {
		api.WriteError(c, http.StatusServiceUnavailable, i18n.CodeInternal, "the whois parser service is not wired")
		return
	}
	parsers, err := h.WhoisParsers.Reset(c.Request.Context())
	if err != nil {
		api.WriteServiceError(c, err)
		return
	}
	api.OK(c, gin.H{"parsers": parsers, "count": len(parsers)})
}

// testWhoisParser runs a candidate rule against a pasted response (offline, the
// "raw" field) or against a live domain lookup.
func (h *Container) testWhoisParser(c *gin.Context) {
	var payload struct {
		Domain          string `json:"domain"`
		Server          string `json:"server"`
		TLD             string `json:"tld"`
		ExpiryRegex     string `json:"expiry_regex"`
		DateLayouts     string `json:"date_layouts"`
		NotFoundPattern string `json:"not_found_pattern"`
		Raw             string `json:"raw"`
	}
	if !bindJSON(c, &payload) {
		return
	}
	parser := &models.WhoisParser{
		TLD:             payload.TLD,
		Server:          payload.Server,
		ExpiryRegex:     payload.ExpiryRegex,
		DateLayouts:     payload.DateLayouts,
		NotFoundPattern: payload.NotFoundPattern,
		Enabled:         true,
	}
	parser.Normalize()
	if problem := parser.Validate(); problem != "" {
		api.WriteError(c, http.StatusBadRequest, i18n.CodeValidation, problem)
		return
	}

	if strings.TrimSpace(payload.Raw) != "" {
		expiresAt, notFound, err := expiry.ParseWhois(parser, payload.Raw)
		api.OK(c, whoisTestResult("", expiresAt, notFound, err))
		return
	}
	if strings.TrimSpace(payload.Domain) == "" {
		api.WriteError(c, http.StatusBadRequest, i18n.CodeValidation, "domain (or raw) is required")
		return
	}
	if h.Domains == nil {
		api.WriteError(c, http.StatusServiceUnavailable, i18n.CodeInternal, "the domain service is not wired")
		return
	}
	raw, expiresAt, notFound, err := h.Domains.Resolver().
		WHOISTest(c.Request.Context(), payload.Domain, parser, payload.Server)
	if len(raw) > 20000 {
		raw = raw[:20000]
	}
	api.OK(c, whoisTestResult(raw, expiresAt, notFound, err))
}

// whoisTestResult shapes the answer of the test endpoint.
func whoisTestResult(raw string, expiresAt time.Time, notFound bool, err error) gin.H {
	out := gin.H{
		"raw":       raw,
		"not_found": notFound,
		"ok":        err == nil,
	}
	if !expiresAt.IsZero() {
		out["expires_at"] = expiresAt
		out["days_left"] = models.DaysLeft(expiresAt, time.Now().UTC())
	}
	if err != nil {
		out["error"] = err.Error()
	}
	return out
}
