package handlers

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/ivancarlosti/up/internal/config"
	"github.com/ivancarlosti/up/internal/i18n"
	"github.com/ivancarlosti/up/internal/services"
)

// recaptchaEngine wires the two login protection handlers alone: the admin
// group needs a database and a session store, and what is under test here is
// the payload contract of the card, not the middleware.
func recaptchaEngine() *gin.Engine {
	gin.SetMode(gin.TestMode)
	container := &Container{
		Cfg:      &config.Config{},
		Log:      slog.Default(),
		Settings: &services.SettingService{},
	}
	engine := gin.New()
	engine.GET("/api/admin/recaptcha", container.adminRecaptcha)
	engine.PUT("/api/admin/recaptcha", container.updateAdminRecaptcha)
	return engine
}

// TestRecaptchaPayloadIsEmptyOnAFreshInstall pins what the settings card renders
// before an operator saves anything: no keys and the captcha reported off.
func TestRecaptchaPayloadIsEmptyOnAFreshInstall(t *testing.T) {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/admin/recaptcha", nil)
	recaptchaEngine().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}
	var payload struct {
		ClientID     string `json:"client_id"`
		ClientSecret string `json:"client_secret"`
		Enabled      bool   `json:"enabled"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v (%s)", err, recorder.Body.String())
	}
	if payload.ClientID != "" || payload.ClientSecret != "" || payload.Enabled {
		t.Fatalf("empty install answered %+v", payload)
	}
}

// TestUpdateRecaptchaRejectsAHalfPair covers the decision that both keys travel
// together: a lone site key or a lone secret would lock the login form, so the
// API refuses it before anything is stored. (Two whitespace-only halves are an
// empty pair, not a half one, and switch the captcha off: see
// models.TestRecaptchaSettingsNormalize, which does not need a database.)
func TestUpdateRecaptchaRejectsAHalfPair(t *testing.T) {
	bodies := []string{
		`{"client_id":"6LeIxAcTAAAAAJcZVRqyHh71UMIEGNQ_MXjiZKhI","client_secret":""}`,
		`{"client_id":"","client_secret":"6LeIxAcTAAAAAGG-vFI1TnRWxMZNFuojJ4WifJWe"}`,
		`{"client_id":"6LeIxAcTAAAAAJcZVRqyHh71UMIEGNQ_MXjiZKhI","client_secret":"   "}`,
	}
	engine := recaptchaEngine()
	for _, body := range bodies {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPut, "/api/admin/recaptcha", strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		engine.ServeHTTP(recorder, request)

		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("%s: status = %d, want 400", body, recorder.Code)
		}
		var failure struct {
			Code string `json:"code"`
		}
		if err := json.Unmarshal(recorder.Body.Bytes(), &failure); err != nil {
			t.Fatalf("%s: decode: %v", body, err)
		}
		if failure.Code != i18n.CodeValidation {
			t.Fatalf("%s: code = %q, want %q", body, failure.Code, i18n.CodeValidation)
		}
	}
}

// TestRecaptchaRoutesRegistered keeps the two endpoints wired: the settings card
// calls them on every visit, and a missing route is a 404 nobody notices until
// the captcha cannot be configured any more.
func TestRecaptchaRoutesRegistered(t *testing.T) {
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
		"GET /api/admin/recaptcha": false,
		"PUT /api/admin/recaptcha": false,
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
