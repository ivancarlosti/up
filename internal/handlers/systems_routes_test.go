package handlers

import (
	"log/slog"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/ivancarlosti/up/internal/config"
)

// TestSystemRoutesRegistered pins the hardware monitoring route tree.
//
// Gin resolves a static segment and a parameter at the same position in one pass,
// and a mistake there panics at registration time - which a build alone does not
// catch. The tree of this feature mixes both ("/systems/containers" and
// "/systems/:id"), and two of its paths carry a second parameter
// ("/systems/:id/containers/:name/metrics"), so registering them is worth a test of
// its own.
func TestSystemRoutesRegistered(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	container := &Container{Cfg: &config.Config{}, Log: slog.Default()}

	defer func() {
		if recovered := recover(); recovered != nil {
			t.Fatalf("registering the admin routes panicked: %v", recovered)
		}
	}()
	container.registerAdmin(engine)

	wanted := map[string]bool{
		"GET /api/systems":                              false,
		"POST /api/systems":                             false,
		"GET /api/systems/agent-key":                    false,
		"GET /api/systems/containers":                   false,
		"GET /api/systems/smart":                        false,
		"GET /api/systems/:id":                          false,
		"PUT /api/systems/:id":                          false,
		"DELETE /api/systems/:id":                       false,
		"POST /api/systems/:id/token":                   false,
		"GET /api/systems/:id/history":                  false,
		"GET /api/systems/:id/alerts":                   false,
		"PUT /api/systems/:id/alerts":                   false,
		"POST /api/systems/:id/alerts/reset":            false,
		"POST /api/systems/:id/alerts/copy":             false,
		"GET /api/systems/:id/metrics":                  false,
		"GET /api/systems/:id/containers":               false,
		"GET /api/systems/:id/containers/:name/metrics": false,
		"GET /api/systems/:id/smart":                    false,
		"POST /api/systems/:id/smart/refresh":           false,
	}
	for _, route := range engine.Routes() {
		key := route.Method + " " + route.Path
		if _, ok := wanted[key]; ok {
			wanted[key] = true
		}
	}
	for route, found := range wanted {
		if !found {
			t.Errorf("route %s is not registered", route)
		}
	}
}

// TestPublicSystemRoutesRegistered pins the read half of the hardware
// monitoring on the bearer token API (docs/public-api.md).
//
// The tree mixes a static segment ("/systems/overview") with a parameter
// ("/systems/:id") at the same position, which gin resolves in one pass and
// panics on at registration time when the order or the shape is wrong, so it is
// registered whole here through Container.Register and not only mounted in
// pieces by the handler tests.
func TestPublicSystemRoutesRegistered(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	container := &Container{Cfg: &config.Config{}, Log: slog.Default()}

	defer func() {
		if recovered := recover(); recovered != nil {
			t.Fatalf("registering the public routes panicked: %v", recovered)
		}
	}()
	container.Register(engine)

	wanted := map[string]bool{
		"GET /api/v1/systems":                false,
		"GET /api/v1/systems/overview":       false,
		"GET /api/v1/systems/:id":            false,
		"GET /api/v1/systems/:id/history":    false,
		"GET /api/v1/systems/:id/metrics":    false,
		"GET /api/v1/systems/:id/containers": false,
		"GET /api/v1/systems/:id/smart":      false,
	}
	for _, route := range engine.Routes() {
		key := route.Method + " " + route.Path
		if _, ok := wanted[key]; ok {
			wanted[key] = true
		}
	}
	for route, found := range wanted {
		if !found {
			t.Errorf("route %s is not registered", route)
		}
	}
}

// TestAgentConnectRouteIsUnauthenticated guards the one route the agent dials: it
// must be reachable without a session (the agent has none) and must never be
// swallowed by the SPA fallback.
func TestAgentConnectRouteIsUnauthenticated(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	container := &Container{Cfg: &config.Config{}, Log: slog.Default()}
	container.Register(engine)

	for _, route := range engine.Routes() {
		if route.Path == "/api/beszel/agent-connect" {
			if route.Method != "GET" {
				t.Fatalf("agent connect is registered as %s", route.Method)
			}
			return
		}
	}
	t.Fatal("/api/beszel/agent-connect is not registered")
}
