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
	"github.com/ivancarlosti/up/internal/models"
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
// before an operator saves anything: no keys, the captcha reported off and the
// threshold at Google's recommendation so the field is never empty.
func TestRecaptchaPayloadIsEmptyOnAFreshInstall(t *testing.T) {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/admin/recaptcha", nil)
	recaptchaEngine().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}
	var payload struct {
		ClientID     string  `json:"client_id"`
		ClientSecret string  `json:"client_secret"`
		MinScore     float64 `json:"min_score"`
		Enabled      bool    `json:"enabled"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v (%s)", err, recorder.Body.String())
	}
	if payload.ClientID != "" || payload.ClientSecret != "" || payload.Enabled {
		t.Fatalf("empty install answered %+v", payload)
	}
	if payload.MinScore != models.DefaultRecaptchaMinScore {
		t.Fatalf("min_score = %v, want %v", payload.MinScore, models.DefaultRecaptchaMinScore)
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

// TestUpdateRecaptchaRejectsAnUnsupportedScore keeps the threshold inside the
// band the card offers: a value near 0 would accept every visitor and a value
// near 1 would reject ordinary ones, so the API refuses it instead of storing a
// number that silently breaks the login form. (An absent field is a different
// case: it arrives as 0 and becomes Google's 0.5, see
// models.TestRecaptchaSettingsMinScore.)
func TestUpdateRecaptchaRejectsAnUnsupportedScore(t *testing.T) {
	bodies := []string{
		`{"client_id":"6Le-site","client_secret":"6Le-secret","min_score":0.05}`,
		`{"client_id":"6Le-site","client_secret":"6Le-secret","min_score":0.95}`,
		`{"client_id":"6Le-site","client_secret":"6Le-secret","min_score":1}`,
	}
	engine := recaptchaEngine()
	for _, body := range bodies {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPut, "/api/admin/recaptcha", strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		engine.ServeHTTP(recorder, request)

		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("%s: status = %d, want 400 (%s)", body, recorder.Code, recorder.Body.String())
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

// TestRecaptchaVerdict covers the policy applied to a Site Verify answer. The
// table is what the login and the subscription form share: `success` alone is
// not enough with v3, because the token also names the action it was minted for
// and carries the score the operator tuned in Admin > Settings.
func TestRecaptchaVerdict(t *testing.T) {
	cases := []struct {
		name       string
		success    bool
		score      float64
		action     string
		expected   string
		errorCodes []string
		minScore   float64
		want       string
	}{
		{name: "accepted", success: true, score: 0.9, action: models.RecaptchaActionLogin,
			expected: models.RecaptchaActionLogin, minScore: 0.5},
		{name: "at the threshold", success: true, score: 0.5, action: models.RecaptchaActionLogin,
			expected: models.RecaptchaActionLogin, minScore: 0.5},
		{name: "below the threshold", success: true, score: 0.2, action: models.RecaptchaActionLogin,
			expected: models.RecaptchaActionLogin, minScore: 0.5, want: "below the minimum"},
		{name: "another action", success: true, score: 0.9, action: models.RecaptchaActionStatusSubscribe,
			expected: models.RecaptchaActionLogin, minScore: 0.5, want: "the endpoint expects"},
		{name: "no action reported", success: true, score: 0.9, action: "",
			expected: models.RecaptchaActionLogin, minScore: 0.5},
		{name: "v2 answer without a score", success: true, score: 0, action: "",
			expected: models.RecaptchaActionLogin, minScore: 0.9},
		{name: "google says no", success: false, errorCodes: []string{"timeout-or-duplicate"},
			expected: models.RecaptchaActionLogin, minScore: 0.5, want: "timeout-or-duplicate"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			reason := recaptchaVerdict(test.success, test.score, test.action, test.expected,
				test.errorCodes, test.minScore)
			switch {
			case test.want == "" && reason != "":
				t.Fatalf("rejected: %q", reason)
			case test.want != "" && !strings.Contains(reason, test.want):
				t.Fatalf("reason = %q, want it to mention %q", reason, test.want)
			}
		})
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
