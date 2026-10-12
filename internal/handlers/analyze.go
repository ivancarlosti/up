package handlers

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/ivancarlosti/up/internal/api"
	"github.com/ivancarlosti/up/internal/i18n"
	"github.com/ivancarlosti/up/internal/models"
	"github.com/ivancarlosti/up/internal/services"
)

// analyzePayload is the body of POST /api/v1/analyze.
//
// It embeds the flat monitor configuration (`url`, `method`, `headers`, `host`,
// `port`, `keyword`, ...) so the caller writes the exact same object a monitor
// carries: a payload that works here works in `POST /api/monitors`, minus the
// monitor-only fields (name, interval, tags), which an analysis does not need.
//
// The body is never stored: it is validated, probed and forgotten.
type analyzePayload struct {
	models.MonitorConfig
	// Type is the probe to run: http, keyword, tcp, dns, ssl or websocket.
	Type models.MonitorType `json:"type"`
	// TimeoutSeconds bounds one probe (default 10, max 300).
	TimeoutSeconds int `json:"timeout_seconds"`
	// Retries is how many extra attempts a DOWN uptime probe gets (0..5).
	Retries int `json:"retries"`
	// RetriesIntervalSeconds is the pause between two attempts (default 5,
	// max 60) and is only used when Retries is above zero.
	RetriesIntervalSeconds int `json:"retries_interval_seconds"`
	// UpsideDown inverts the uptime verdict, exactly like a monitor flag.
	UpsideDown bool `json:"upside_down"`
	// Checks lists the probes to run. Omitted means "uptime, plus certificate
	// when the type can read one".
	Checks []string `json:"checks"`
}

// apiAnalyze runs the requested probes against the target of the body and
// returns the observations without storing any of them.
//
// It is the script friendly counterpart of "check now": the caller does not need
// a monitor, so a target can be inspected before it is added, or by a pipeline
// that has no business in the monitor list.
func (h *Container) apiAnalyze(c *gin.Context) {
	if h.Analysis == nil {
		api.WriteError(c, http.StatusServiceUnavailable, i18n.CodeInternal, "the analysis endpoint is not wired")
		return
	}
	var payload analyzePayload
	if !bindJSON(c, &payload) {
		return
	}
	result, err := h.Analysis.Analyze(c.Request.Context(), services.AnalysisRequest{
		Type:                   payload.Type,
		Config:                 payload.MonitorConfig,
		TimeoutSeconds:         payload.TimeoutSeconds,
		Retries:                payload.Retries,
		RetriesIntervalSeconds: payload.RetriesIntervalSeconds,
		UpsideDown:             payload.UpsideDown,
		Checks:                 payload.Checks,
	})
	if err != nil {
		api.WriteServiceError(c, err)
		return
	}
	api.OK(c, result)
}
