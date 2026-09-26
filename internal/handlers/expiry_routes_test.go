package handlers

import (
	"log/slog"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/ivancarlosti/up/internal/config"
)

// TestExpiryRoutesRegistered makes sure the daily expiry endpoints are wired and
// that gin accepts the route tree (a conflicting wildcard panics at
// registration time, which a build alone does not catch).
func TestExpiryRoutesRegistered(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	container := &Container{
		Cfg: &config.Config{},
		Log: slog.Default(),
	}

	defer func() {
		if recovered := recover(); recovered != nil {
			t.Fatalf("registering the admin routes panicked: %v", recovered)
		}
	}()
	container.registerAdmin(engine)

	wanted := map[string]bool{
		"GET /api/admin/expiry":                      false,
		"PUT /api/admin/expiry":                      false,
		"POST /api/admin/expiry/run":                 false,
		"GET /api/admin/expiry/whois-parsers":        false,
		"POST /api/admin/expiry/whois-parsers":       false,
		"POST /api/admin/expiry/whois-parsers/test":  false,
		"PUT /api/admin/expiry/whois-parsers/:id":    false,
		"DELETE /api/admin/expiry/whois-parsers/:id": false,
		"POST /api/admin/expiry/whois-parsers/reset": false,
	}
	for _, route := range engine.Routes() {
		key := route.Method + " " + route.Path
		if _, ok := wanted[key]; ok {
			wanted[key] = true
		}
	}
	for route, found := range wanted {
		if !found {
			t.Fatalf("route %s is not registered", route)
		}
	}
}
