package handlers

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"golang.org/x/oauth2"

	"github.com/ivancarlosti/up/internal/config"
	"github.com/ivancarlosti/up/internal/i18n"
	"github.com/ivancarlosti/up/internal/services"
)

// silentLogger keeps the handler logs out of the test output.
func silentLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
}

// TestNewOIDCProviderDiscoversLogoutURL pins the discovery wiring: the
// end_session_endpoint is not part of the oauth2 endpoint set, so it has to be
// read from the raw discovery document, and the logout endpoint depends on it.
func TestNewOIDCProviderDiscoversLogoutURL(t *testing.T) {
	var discovery string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/.well-known/openid-configuration" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, discovery)
	}))
	defer server.Close()
	discovery = `{
		"issuer": "` + server.URL + `",
		"authorization_endpoint": "` + server.URL + `/protocol/openid-connect/auth",
		"token_endpoint": "` + server.URL + `/protocol/openid-connect/token",
		"jwks_uri": "` + server.URL + `/protocol/openid-connect/certs",
		"end_session_endpoint": "` + server.URL + `/protocol/openid-connect/logout"
	}`

	cfg := &config.Config{
		AuthMethod:                    config.AuthMethodKeycloak,
		KeycloakIssuer:                server.URL,
		KeycloakClientID:              "up-client",
		KeycloakRedirectURI:           "https://up.example.com/api/auth/callback",
		KeycloakPostLogoutRedirectURI: "https://up.example.com/login",
	}
	provider, err := NewOIDCProvider(context.Background(), cfg, silentLogger())
	if err != nil {
		t.Fatalf("NewOIDCProvider: %v", err)
	}
	if provider == nil {
		t.Fatal("NewOIDCProvider returned no provider for AUTH_METHOD=keycloak")
	}
	want := server.URL + "/protocol/openid-connect/logout"
	if provider.logoutURL != want {
		t.Fatalf("logoutURL = %q, want %q", provider.logoutURL, want)
	}
}

// TestLoginOptionsOnlyForwardsPromptLogin covers the prompt=login parameter of
// the login endpoint: it is what makes the provider show its form again instead
// of reusing the browser session, and every other value is dropped instead of
// travelling verbatim into the authorization URL. The rejection page no longer
// links it, but the endpoint still supports it. The PKCE/nonce options must
// survive either way.
func TestLoginOptionsOnlyForwardsPromptLogin(t *testing.T) {
	client := &oauth2.Config{
		ClientID: "up-client",
		Endpoint: oauth2.Endpoint{AuthURL: "https://sso.example.com/protocol/openid-connect/auth"},
	}
	cases := []struct {
		name      string
		prompt    string
		forwarded bool
	}{
		{name: "login is forwarded", prompt: "login", forwarded: true},
		{name: "select account is not supported by the provider", prompt: "select_account"},
		{name: "injection attempt is dropped", prompt: "../evil&x=1"},
		{name: "empty is dropped", prompt: ""},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			authURL := client.AuthCodeURL("state", loginOptions(testCase.prompt, "nonce", "verifier")...)
			if got := strings.Contains(authURL, "prompt=login"); got != testCase.forwarded {
				t.Fatalf("prompt=login in %q = %v, want %v", authURL, got, testCase.forwarded)
			}
			for _, option := range []string{"code_challenge_method=S256", "code_challenge=", "nonce=nonce", "state=state"} {
				if !strings.Contains(authURL, option) {
					t.Errorf("the authorization URL lost %q: %s", option, authURL)
				}
			}
		})
	}
}

// TestOIDCLogoutDropsEveryCookieAndHandsOverToTheProvider covers the endpoint
// used by the rejection page and the dashboard: the local cookies go away and
// the browser is handed over so the provider session is dropped as well.
func TestOIDCLogoutDropsEveryCookieAndHandsOverToTheProvider(t *testing.T) {
	gin.SetMode(gin.TestMode)
	logger := silentLogger()
	cfg := &config.Config{
		AppURL:                        "https://up.example.com",
		KeycloakClientID:              "up-client",
		KeycloakRealm:                 "up-realm",
		KeycloakPostLogoutRedirectURI: "https://up.example.com/login",
	}
	logoutURL := "https://sso.example.com/realms/up-realm/protocol/openid-connect/logout"
	handler := &Container{
		Cfg:      cfg,
		Log:      logger,
		Sessions: services.NewSessionService(cfg, nil, logger),
		OIDC:     &OIDCProvider{cfg: cfg, log: logger, logoutURL: logoutURL},
	}
	engine := gin.New()
	engine.GET("/api/auth/oidc/logout", handler.oidcLogout)

	request := httptest.NewRequest(http.MethodGet, "/api/auth/oidc/logout?redirect=/login", nil)
	request.AddCookie(&http.Cookie{Name: oidcIDTokenCookie, Value: "the.id.token"})
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusFound {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusFound)
	}
	location, err := url.Parse(recorder.Header().Get("Location"))
	if err != nil {
		t.Fatalf("Location is not a URL: %v", err)
	}
	if got := location.Scheme + "://" + location.Host + location.Path; got != logoutURL {
		t.Fatalf("redirected to %q, want %q", got, logoutURL)
	}
	query := location.Query()
	if query.Get("client_id") != "up-client" {
		t.Errorf("client_id = %q, want %q", query.Get("client_id"), "up-client")
	}
	if query.Get("post_logout_redirect_uri") != "https://up.example.com/login" {
		t.Errorf("post_logout_redirect_uri = %q", query.Get("post_logout_redirect_uri"))
	}
	// The hint is what makes the provider skip its confirmation page.
	if query.Get("id_token_hint") != "the.id.token" {
		t.Errorf("id_token_hint = %q, want %q", query.Get("id_token_hint"), "the.id.token")
	}

	cleared := map[string]bool{}
	for _, cookie := range recorder.Result().Cookies() {
		if cookie.MaxAge < 0 {
			cleared[cookie.Name] = true
		}
	}
	for _, name := range []string{services.SessionCookieName, oidcStateCookie, oidcIDTokenCookie} {
		if !cleared[name] {
			t.Errorf("cookie %s was not cleared", name)
		}
	}
}

// TestOIDCLogoutFallsBackToTheAppWithoutProviderLogout covers a provider that
// does not advertise an end_session_endpoint: only the local session can be
// cleared, so the browser must land on the login screen instead of failing.
func TestOIDCLogoutFallsBackToTheAppWithoutProviderLogout(t *testing.T) {
	gin.SetMode(gin.TestMode)
	logger := silentLogger()
	cfg := &config.Config{AppURL: "https://up.example.com"}
	handler := &Container{
		Cfg:      cfg,
		Log:      logger,
		Sessions: services.NewSessionService(cfg, nil, logger),
		OIDC:     &OIDCProvider{cfg: cfg, log: logger},
	}
	engine := gin.New()
	engine.GET("/api/auth/oidc/logout", handler.oidcLogout)

	for _, target := range []string{"/api/auth/oidc/logout", "/api/auth/oidc/logout?redirect=login"} {
		recorder := httptest.NewRecorder()
		engine.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, target, nil))
		if recorder.Code != http.StatusFound {
			t.Fatalf("%s: status = %d, want %d", target, recorder.Code, http.StatusFound)
		}
		if got := recorder.Header().Get("Location"); got != "https://up.example.com/login" {
			t.Fatalf("%s: Location = %q, want %q", target, got, "https://up.example.com/login")
		}
	}
}

// TestRenderOIDCErrorOffersOnlyTheProviderLogout pins the rejection page: it
// offers the logout handoff (the action that actually clears the provider
// cookie) and not the forced re-login, which was confusing and unreliable with
// Keycloak. The provider message is still escaped (with the standard library
// escaper, the sanitizer recognized by `go/reflected-xss`).
func TestRenderOIDCErrorOffersOnlyTheProviderLogout(t *testing.T) {
	gin.SetMode(gin.TestMode)
	handler := &Container{
		Cfg: &config.Config{AppURL: "https://up.example.com", KeycloakRealm: "up-realm"},
		Log: silentLogger(),
	}
	engine := gin.New()
	engine.GET("/callback", func(c *gin.Context) {
		// The real callback stores the ID token before it renders the page.
		handler.storeIDToken(c, "the.id.token")
		handler.renderOIDCError(c, http.StatusForbidden, i18n.CodeAuthDomainNotAllowed,
			`the account "ivan.almeida@kuarup.com.br" <b> is not allowed & not invited`)
	})
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/callback", nil))

	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusForbidden)
	}
	// The hint has to survive the HTML response, otherwise "Sign out" would
	// fall back to the provider confirmation page.
	hint := false
	for _, cookie := range recorder.Result().Cookies() {
		if cookie.Name == oidcIDTokenCookie && cookie.Value == "the.id.token" {
			hint = true
		}
	}
	if !hint {
		t.Error("the error page did not carry the ID token cookie")
	}
	body := recorder.Body.String()
	for _, needle := range []string{
		"https://up.example.com/api/auth/oidc/logout?redirect=/login",
		"Sign out of up-realm",
		"&#34;ivan.almeida@kuarup.com.br&#34; &lt;b&gt; is not allowed &amp; not invited",
		i18n.CodeAuthDomainNotAllowed,
	} {
		if !strings.Contains(body, needle) {
			t.Errorf("the error page is missing %q:\n%s", needle, body)
		}
	}
	for _, gone := range []string{"/api/auth/oidc/login", "Sign in with another account"} {
		if strings.Contains(body, gone) {
			t.Errorf("the error page still offers %q:\n%s", gone, body)
		}
	}
}

// TestStoreIDTokenKeepsTheHintHttpOnly covers the cookie that carries the
// id_token_hint: it must be short lived, HttpOnly and Secure when the app runs
// on https, since it is a bearer credential for the logout call.
func TestStoreIDTokenKeepsTheHintHttpOnly(t *testing.T) {
	gin.SetMode(gin.TestMode)
	handler := &Container{Cfg: &config.Config{AppURL: "https://up.example.com"}, Log: silentLogger()}
	engine := gin.New()
	engine.GET("/store", func(c *gin.Context) { handler.storeIDToken(c, "the.id.token") })
	engine.GET("/empty", func(c *gin.Context) { handler.storeIDToken(c, "") })

	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/store", nil))
	cookies := recorder.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("got %d cookies, want 1", len(cookies))
	}
	cookie := cookies[0]
	if cookie.Name != oidcIDTokenCookie || cookie.Value != "the.id.token" {
		t.Fatalf("cookie = %s=%q", cookie.Name, cookie.Value)
	}
	if !cookie.HttpOnly || !cookie.Secure {
		t.Errorf("the ID token cookie must be HttpOnly and Secure: %+v", cookie)
	}
	if cookie.MaxAge != oidcIDTokenCookieTTL {
		t.Errorf("MaxAge = %d, want %d", cookie.MaxAge, oidcIDTokenCookieTTL)
	}
	if cookie.SameSite != http.SameSiteLaxMode {
		t.Errorf("SameSite = %v, want Lax", cookie.SameSite)
	}

	// An empty token must not create a bogus cookie.
	recorder = httptest.NewRecorder()
	engine.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/empty", nil))
	if got := recorder.Result().Cookies(); len(got) != 0 {
		t.Errorf("an empty ID token wrote %d cookie(s)", len(got))
	}
}
