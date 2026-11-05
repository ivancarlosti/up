package handlers

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/ivancarlosti/up/internal/config"
	"github.com/ivancarlosti/up/internal/i18n"
	"github.com/ivancarlosti/up/internal/models"
	"github.com/ivancarlosti/up/internal/services"
)

// TestPublicSystemHidesAlertsAndSecrets pins the redaction contract of the
// public systems endpoints: what a bearer token gets is the decorated row the
// dashboard renders, minus the operator tuning and the one time credentials.
func TestPublicSystemHidesAlertsAndSecrets(t *testing.T) {
	view := services.SystemView{
		System: models.System{
			ID:          3,
			Name:        "docker-01",
			Host:        "10.0.0.14",
			Enabled:     true,
			TokenPrefix: "ab12cd",
			LastError:   "the agent refused the KEY",
			Status:      models.SystemStatusUp,
			OwnerNode:   "up-node-1",
			Connected:   true,
		},
		Token:          "the one time token",
		InstallCommand: "docker run -d --name up-agent ...",
		AgentKey:       "ssh-ed25519 AAAA...",
		Alerts: []models.SystemAlertRule{
			{Metric: models.SystemAlertCPU, Enabled: true, Threshold: 90, MinMinutes: 5},
		},
	}

	public := publicSystem(view)

	if public.Alerts != nil {
		t.Errorf("alerts = %+v, want them stripped", public.Alerts)
	}
	if public.Token != "" {
		t.Errorf("token = %q, want it stripped", public.Token)
	}
	if public.InstallCommand != "" {
		t.Errorf("install_command = %q, want it stripped", public.InstallCommand)
	}
	if public.AgentKey != "" {
		t.Errorf("agent_key = %q, want it stripped", public.AgentKey)
	}

	// Everything the caller is meant to read survives: the row, the runtime
	// decoration and the diagnostics that explain a machine that is down.
	if public.ID != 3 || public.Name != "docker-01" || public.Host != "10.0.0.14" || !public.Enabled {
		t.Errorf("the row changed: %+v", public.System)
	}
	if public.Status != models.SystemStatusUp || !public.Connected || public.OwnerNode != "up-node-1" {
		t.Errorf("the decoration changed: status=%q connected=%v owner=%q",
			public.Status, public.Connected, public.OwnerNode)
	}
	if public.TokenPrefix != "ab12cd" || public.LastError == "" {
		t.Errorf("the diagnostics changed: prefix=%q error=%q", public.TokenPrefix, public.LastError)
	}

	// The copy must not mutate the view the dashboard is going to render.
	if len(view.Alerts) != 1 || view.Token == "" || view.AgentKey == "" {
		t.Errorf("the source view was mutated: %+v", view)
	}
}

// TestPublicSystemEndpointsWithoutTheService checks the answer of a process
// where the hardware monitoring is not wired: the same 503 the dashboard gives,
// never a 500 and never a partially filled payload.
func TestPublicSystemEndpointsWithoutTheService(t *testing.T) {
	gin.SetMode(gin.TestMode)
	container := &Container{Cfg: &config.Config{}, Log: analyzeTestLogger()}
	engine := gin.New()
	engine.GET("/api/v1/systems", container.apiListSystems)
	engine.GET("/api/v1/systems/overview", container.apiSystemsOverview)
	engine.GET("/api/v1/systems/:id", container.apiGetSystem)
	engine.GET("/api/v1/systems/:id/history", container.apiSystemHistory)
	engine.GET("/api/v1/systems/:id/metrics", container.apiSystemMetrics)
	engine.GET("/api/v1/systems/:id/containers", container.apiSystemContainers)
	engine.GET("/api/v1/systems/:id/smart", container.apiSystemSmart)

	paths := []string{
		"/api/v1/systems",
		"/api/v1/systems/overview",
		"/api/v1/systems/4",
		"/api/v1/systems/4/history",
		"/api/v1/systems/4/metrics",
		"/api/v1/systems/4/containers",
		"/api/v1/systems/4/smart",
	}
	for _, path := range paths {
		recorder := httptest.NewRecorder()
		engine.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
		if recorder.Code != http.StatusServiceUnavailable {
			t.Errorf("%s answered %d, want 503", path, recorder.Code)
			continue
		}
		body := decodeAPIError(t, recorder)
		if body.Code != i18n.CodeValidation {
			t.Errorf("%s answered code %q, want %q", path, body.Code, i18n.CodeValidation)
		}
	}
}
