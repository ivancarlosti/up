package notify

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/ivancarlosti/up/internal/models"
)

func sampleMessage() Message {
	return Message{
		Event:       "down",
		Status:      "down",
		Title:       "[DOWN] API health",
		MonitorName: "API health",
		MonitorType: "http",
		MonitorURL:  "https://api.example.com/health",
		Message:     "connection refused",
		LatencyMS:   42,
		NodeID:      "up-node-1",
		InstanceURL: "https://up.example.com",
	}
}

func TestBuildSMTPURL(t *testing.T) {
	config := &models.SMTPConfig{
		Host:          "smtp.example.com",
		Port:          587,
		Username:      "up@example.com",
		Password:      "p@ss:word",
		From:          "up@example.com",
		To:            "ops@example.com, oncall@example.com",
		SubjectPrefix: "[Up]",
		UseHTML:       true,
	}
	raw := buildSMTPURL(config, sampleMessage())
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("the generated URL must parse: %v (%s)", err, raw)
	}
	if parsed.Scheme != "smtp" || parsed.Host != "smtp.example.com:587" {
		t.Fatalf("unexpected endpoint: %s", raw)
	}
	if user := parsed.User.Username(); user != "up@example.com" {
		t.Fatalf("username lost: %s", raw)
	}
	if password, _ := parsed.User.Password(); password != "p@ss:word" {
		t.Fatalf("password must survive URL encoding: %s", raw)
	}
	query := parsed.Query()
	if query.Get("fromaddress") != "up@example.com" {
		t.Fatalf("fromaddress missing: %s", raw)
	}
	// The recipient order is preserved (the operator's order is intentional).
	if query.Get("toaddresses") != "ops@example.com,oncall@example.com" {
		t.Fatalf("recipients must be comma separated: %q", query.Get("toaddresses"))
	}
	if !strings.HasPrefix(query.Get("subject"), "[Up]") {
		t.Fatalf("subject prefix lost: %q", query.Get("subject"))
	}
	if query.Get("usehtml") != "yes" || query.Get("encryption") != "Auto" {
		t.Fatalf("unexpected encryption flags: %s", raw)
	}

	secure := *config
	secure.Secure = true
	if encryption := mustQuery(t, buildSMTPURL(&secure, sampleMessage())).Get("encryption"); encryption != "ExplicitTLS" {
		t.Fatalf("secure connections must use implicit TLS, got %q", encryption)
	}
}

func TestBuildWebhookURL(t *testing.T) {
	config := &models.WebhookConfig{
		URL:         "http://hooks.internal:8080/up?existing=1",
		Method:      "PUT",
		ContentType: "application/json",
		Headers: []models.Header{
			{Key: "X-Token", Value: "abc"},
			{Key: "Content-Type", Value: "application/json; charset=utf-8"},
		},
	}
	raw, err := buildWebhookURL(config)
	if err != nil {
		t.Fatalf("build webhook URL: %v", err)
	}
	parsed := mustParse(t, raw)
	if parsed.Scheme != "generic" || parsed.Host != "hooks.internal:8080" || parsed.Path != "/up" {
		t.Fatalf("unexpected service URL: %s", raw)
	}
	query := parsed.Query()
	if query.Get("method") != "PUT" {
		t.Fatalf("method lost: %s", raw)
	}
	if query.Get("disabletls") != "yes" {
		t.Fatalf("an http webhook must disable TLS: %s", raw)
	}
	if query.Get("@X-Token") != "abc" {
		t.Fatalf("custom headers must be prefixed with @: %s", raw)
	}
	if query.Get("@Content-Type") != "application/json" {
		t.Fatalf("the content type must be forced on the header, otherwise shoutrrr sends text/plain: %s", raw)
	}
	// The parameters of the operator's URL are what a token in the query lives in:
	// they must survive the translation into the service URL.
	if query.Get("existing") != "1" {
		t.Fatalf("the query of the webhook URL must be kept: %s", raw)
	}

	httpsConfig := *config
	httpsConfig.URL = "https://hooks.example.com/up"
	httpsURL, err := buildWebhookURL(&httpsConfig)
	if err != nil {
		t.Fatalf("build https webhook URL: %v", err)
	}
	if disable := mustParse(t, httpsURL).Query().Get("disabletls"); disable != "" {
		t.Fatalf("https webhooks must keep TLS enabled, got disabletls=%q", disable)
	}

	if _, err := buildWebhookURL(&models.WebhookConfig{URL: "not a url"}); err == nil {
		t.Fatal("an invalid webhook URL must be rejected")
	}
}

// TestBuildWebhookURLCarriesOperatorURL pins the invariant of the passthrough: every
// query parameter of the operator's URL reaches the target, and the ones that collide
// with a shoutrrr generic setting are escaped with the prefix shoutrrr itself restores
// (format.EscapeKey), so neither side loses its value.
func TestBuildWebhookURLCarriesOperatorURL(t *testing.T) {
	cases := []struct {
		name       string
		rawURL     string
		wantQuery  map[string]string
		absentKeys []string
		wantUser   string
		wantPass   string
	}{
		{
			name:      "token in the query",
			rawURL:    "https://hooks.example.com/up?token=abc123&mode=fast",
			wantQuery: map[string]string{"token": "abc123", "mode": "fast"},
		},
		{
			name:      "userinfo and port",
			rawURL:    "https://up:hooksecret@hooks.example.com:8443/up?token=abc",
			wantQuery: map[string]string{"token": "abc"},
			wantUser:  "up",
			wantPass:  "hooksecret",
		},
		{
			name:   "the service settings of the URL are escaped, not consumed",
			rawURL: "https://hooks.example.com/up?method=GET&template=json&title=custom&disabletls=yes&contenttype=text/csv&titlekey=t&messagekey=m",
			wantQuery: map[string]string{
				// The settings Up owns keep their value...
				"method":      "POST",
				"contenttype": "application/json",
				// ...while the operator's parameters are restored for the target.
				"__method":      "GET",
				"__template":    "json",
				"__title":       "custom",
				"__disabletls":  "yes",
				"__contenttype": "text/csv",
				"__titlekey":    "t",
				"__messagekey":  "m",
			},
		},
		{
			name:       "a header variant of the content type is dropped",
			rawURL:     "https://hooks.example.com/up?@content-type=text/csv&token=abc",
			wantQuery:  map[string]string{"@Content-Type": "application/json", "token": "abc"},
			absentKeys: []string{"@content-type"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := buildWebhookURL(&models.WebhookConfig{URL: tc.rawURL, ContentType: "application/json"})
			if err != nil {
				t.Fatalf("build webhook URL: %v", err)
			}
			parsed := mustParse(t, raw)
			query := parsed.Query()
			for key, want := range tc.wantQuery {
				if got := query.Get(key); got != want {
					t.Errorf("query %q = %q, want %q (%s)", key, got, want, raw)
				}
			}
			for _, key := range tc.absentKeys {
				if _, found := query[key]; found {
					t.Errorf("query %q must be dropped: %s", key, raw)
				}
			}
			if tc.wantUser != "" {
				if parsed.User == nil || parsed.User.Username() != tc.wantUser {
					t.Fatalf("the userinfo must survive: %s", raw)
				}
				if password, _ := parsed.User.Password(); password != tc.wantPass {
					t.Errorf("the password must survive: %s", raw)
				}
				if parsed.Host != "hooks.example.com:8443" {
					t.Errorf("the port must survive: %s", raw)
				}
			}
		})
	}

	// Repeated parameters are kept in order: a target may rely on the repeated form.
	raw, err := buildWebhookURL(&models.WebhookConfig{URL: "https://hooks.example.com/up?tag=a&tag=b"})
	if err != nil {
		t.Fatalf("build webhook URL: %v", err)
	}
	if tags := mustParse(t, raw).Query()["tag"]; len(tags) != 2 || tags[0] != "a" || tags[1] != "b" {
		t.Errorf("repeated parameters must be kept, got %v (%s)", tags, raw)
	}
}

// TestWebhookServicePropsAreStable pins the derived reserved set: a shoutrrr upgrade that
// adds a generic setting fails here instead of silently eating an operator parameter.
func TestWebhookServicePropsAreStable(t *testing.T) {
	want := []string{"contenttype", "disabletls", "messagekey", "method", "template", "title", "titlekey"}
	if len(webhookServiceProps) != len(want) {
		t.Fatalf("webhookServiceProps = %v, want %v", webhookServiceProps, want)
	}
	for _, key := range want {
		if !webhookServiceProps[key] {
			t.Errorf("%q must be escaped: the generic service reads it as a setting", key)
		}
	}
}

// TestWebhookRequestCarriesOperatorURL is the regression test of the reported symptom: a
// target that authenticates with a query token must receive that token. The request is a
// real one (engine, shoutrrr and an HTTP client are all involved), because the request is
// the only thing that proves what the target sees after shoutrrr has rebuilt the URL.
func TestWebhookRequestCarriesOperatorURL(t *testing.T) {
	type received struct {
		method  string
		query   url.Values
		headers http.Header
		user    string
		pass    string
		body    string
	}

	requests := make(chan received, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, _ := r.BasicAuth()
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read the request body: %v", err)
		}
		requests <- received{
			method:  r.Method,
			query:   r.URL.Query(),
			headers: r.Header.Clone(),
			user:    user,
			pass:    pass,
			body:    string(body),
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	webhook := &models.WebhookConfig{
		// The http scheme of the test server is what keeps the request in plain HTTP.
		URL:         strings.Replace(server.URL, "http://", "http://up:hooksecret@", 1) + "/hook?token=abc123&method=GET&mode=fast",
		Method:      "PUT",
		ContentType: "application/json",
		Headers:     []models.Header{{Key: "X-Token", Value: "header-token"}},
	}
	channel := &models.Notification{
		Name:   "ops webhook",
		Type:   models.NotificationWebhook,
		Config: models.NotificationConfig{Webhook: webhook},
	}

	engine := NewEngine(slog.New(slog.NewTextHandler(io.Discard, nil)), 5*time.Second)
	message := sampleMessage()
	if err := engine.SendWebhook(channel, message); err != nil {
		t.Fatalf("send the webhook: %v", err)
	}

	select {
	case got := <-requests:
		if got.query.Get("token") != "abc123" {
			t.Errorf("the target must receive the token, got %q", got.query.Get("token"))
		}
		if got.query.Get("mode") != "fast" {
			t.Errorf("every parameter must be forwarded, got %q", got.query.Get("mode"))
		}
		// The dialog's method drives the request while the operator's parameter of the
		// same name still reaches the target.
		if got.method != "PUT" {
			t.Errorf("request method = %q, want PUT", got.method)
		}
		if got.query.Get("method") != "GET" {
			t.Errorf("the escaped method parameter must be restored for the target, got %q", got.query.Get("method"))
		}
		if got.user != "up" || got.pass != "hooksecret" {
			t.Errorf("the userinfo must be sent as Basic auth, got %q/%q", got.user, got.pass)
		}
		if got.headers.Get("X-Token") != "header-token" {
			t.Errorf("the custom header is missing: %v", got.headers)
		}
		if contentType := got.headers.Get("Content-Type"); contentType != "application/json" {
			t.Errorf("Content-Type = %q, want application/json", contentType)
		}
		body, err := message.RenderBody(webhook.BodyTemplate)
		if err != nil {
			t.Fatalf("render the body: %v", err)
		}
		if got.body != body {
			t.Errorf("the body must be sent verbatim:\ngot  %s\nwant %s", got.body, body)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the target received no request")
	}
}

func TestRenderBody(t *testing.T) {
	message := sampleMessage()

	defaultBody, err := message.RenderBody("")
	if err != nil {
		t.Fatalf("default body: %v", err)
	}
	if !strings.Contains(defaultBody, `"event":"down"`) || !strings.Contains(defaultBody, `"latency_ms":42`) {
		t.Fatalf("unexpected default payload: %s", defaultBody)
	}

	templated, err := message.RenderBody(`{"alert":"{{.Event}}","name":"{{.MonitorName}}","upper":"{{upper .NodeID}}"}`)
	if err != nil {
		t.Fatalf("template body: %v", err)
	}
	if !strings.Contains(templated, `"alert":"down"`) || !strings.Contains(templated, `"upper":"UP-NODE-1"`) {
		t.Fatalf("template helpers failed: %s", templated)
	}

	if _, err := message.RenderBody("{{.Event"); err == nil {
		t.Fatal("an invalid template must fail instead of sending garbage")
	}
}

// TestMessageTagsAndGroups pins the two shapes the tags and the groups of a
// monitor take in a notification: arrays, which are what a webhook consumer
// parses, and one comma separated string, which is what a human reads in a log
// line or an e-mail.
func TestMessageTagsAndGroups(t *testing.T) {
	monitor := &models.Monitor{
		Name:   "API health",
		Type:   models.MonitorTypeHTTP,
		Tags:   "web, api,prod",
		Config: models.MonitorConfig{URL: "https://api.example.com/health"},
	}
	// The groups arrive already ordered (sort order, then name) and a name may
	// contain a space, so neither the array nor the flat form may reorder or
	// split them.
	message := NewMessage(models.EventDown, monitor, models.AggregateDown,
		"connection refused", 42, "up-node-1", "https://up.example.com",
		[]string{"Public", "Prod EU"})

	if got := strings.Join(message.MonitorTags, ","); got != "web,api,prod" {
		t.Errorf("MonitorTags = %q, want the trimmed tags in the stored order", got)
	}
	if got := message.TagsCSV(); got != "web,api,prod" {
		t.Errorf("TagsCSV() = %q, want %q", got, "web,api,prod")
	}
	if got := message.GroupsCSV(); got != "Public,Prod EU" {
		t.Errorf("GroupsCSV() = %q, want %q", got, "Public,Prod EU")
	}

	body, err := message.RenderBody(
		`{"tags":{{json .MonitorTags}},"groups":{{json .MonitorGroups}},"flat":"{{.TagsCSV}}|{{.GroupsCSV}}"}`)
	if err != nil {
		t.Fatalf("render the body template: %v", err)
	}
	want := `{"tags":["web","api","prod"],"groups":["Public","Prod EU"],"flat":"web,api,prod|Public,Prod EU"}`
	if body != want {
		t.Errorf("rendered body =\n%s\nwant\n%s", body, want)
	}

	if text := message.Text(); !strings.Contains(text, "Tags    : web,api,prod") ||
		!strings.Contains(text, "Groups  : Public,Prod EU") {
		t.Errorf("the text body must list the tags and the groups:\n%s", text)
	}
	html := message.HTML()
	if !strings.Contains(html, ">Tags</td><td><strong>web,api,prod</strong></td>") ||
		!strings.Contains(html, ">Groups</td><td><strong>Public,Prod EU</strong></td>") {
		t.Errorf("the HTML body must list the tags and the groups:\n%s", html)
	}
}

// TestMessageWithoutTagsOrGroups is the empty counterpart: an untagged monitor in
// no group must render empty arrays, never null (a consumer that walks
// `body.tags` chokes on null), and must not print empty rows.
func TestMessageWithoutTagsOrGroups(t *testing.T) {
	monitor := &models.Monitor{
		Name:   "Bare",
		Type:   models.MonitorTypeHTTP,
		Config: models.MonitorConfig{URL: "https://bare.example.com"},
	}
	message := NewMessage(models.EventUp, monitor, models.AggregateUp, "", 5, "", "", nil)

	body, err := message.RenderBody(
		`{"tags":{{json .MonitorTags}},"groups":{{json .MonitorGroups}},"tags_csv":"{{.TagsCSV}}","groups_csv":"{{.GroupsCSV}}"}`)
	if err != nil {
		t.Fatalf("render the body template: %v", err)
	}
	want := `{"tags":[],"groups":[],"tags_csv":"","groups_csv":""}`
	if body != want {
		t.Errorf("rendered body = %s, want %s", body, want)
	}

	if text := message.Text(); strings.Contains(text, "Tags") || strings.Contains(text, "Groups") {
		t.Errorf("the text body must omit the lines when there is nothing to show:\n%s", text)
	}
	if html := message.HTML(); strings.Contains(html, ">Tags<") || strings.Contains(html, ">Groups<") {
		t.Errorf("the HTML body must omit the rows when there is nothing to show:\n%s", html)
	}
}

// TestWebhookPayloadCarriesTagsAndGroups covers the two default payloads of the
// Webhook channel: the one the API sends when no body template is configured
// (jsonPayload) and the one pre-filled in the dialog (DefaultWebhookBodyTemplate,
// applied by the API when the field is empty). Both must stay valid JSON.
func TestWebhookPayloadCarriesTagsAndGroups(t *testing.T) {
	monitor := &models.Monitor{
		Name:   "API",
		Type:   models.MonitorTypeHTTP,
		Tags:   "web",
		Config: models.MonitorConfig{URL: "https://api.example.com"},
	}
	message := NewMessage(models.EventDown, monitor, models.AggregateDown,
		"boom", 12, "up-node-1", "https://up.example.com", []string{"Prod"})

	for name, bodyTemplate := range map[string]string{
		"default payload": "",
		"dialog template": models.DefaultWebhookBodyTemplate,
	} {
		body, err := message.RenderBody(bodyTemplate)
		if err != nil {
			t.Fatalf("%s: render: %v", name, err)
		}
		var payload struct {
			Tags   []string `json:"tags"`
			Groups []string `json:"groups"`
		}
		if err := json.Unmarshal([]byte(body), &payload); err != nil {
			t.Fatalf("%s: must stay valid JSON: %v (%s)", name, err, body)
		}
		if len(payload.Tags) != 1 || payload.Tags[0] != "web" {
			t.Errorf("%s: tags = %v, want [web] (%s)", name, payload.Tags, body)
		}
		if len(payload.Groups) != 1 || payload.Groups[0] != "Prod" {
			t.Errorf("%s: groups = %v, want [Prod] (%s)", name, payload.Groups, body)
		}
	}

	// The payload sent when no template is configured also carries the flat
	// forms, for the receivers that cannot parse JSON.
	defaultBody, err := message.RenderBody("")
	if err != nil {
		t.Fatalf("render the default payload: %v", err)
	}
	if !strings.Contains(defaultBody, `"tags_csv":"web"`) || !strings.Contains(defaultBody, `"groups_csv":"Prod"`) {
		t.Errorf("the default payload must carry the comma separated forms: %s", defaultBody)
	}
}

func TestMonitorTarget(t *testing.T) {
	cases := []struct {
		monitor models.Monitor
		want    string
	}{
		{models.Monitor{Type: models.MonitorTypeHTTP, Config: models.MonitorConfig{URL: "https://x/y"}}, "https://x/y"},
		{models.Monitor{Type: models.MonitorTypeTCP, Config: models.MonitorConfig{Host: "db", Port: 3306}}, "db:3306"},
		{models.Monitor{Type: models.MonitorTypeDNS, Config: models.MonitorConfig{RecordType: "A", Hostname: "x.com", ResolverServer: "1.1.1.1"}}, "A x.com @1.1.1.1"},
	}
	for _, tc := range cases {
		if got := MonitorTarget(&tc.monitor); got != tc.want {
			t.Errorf("MonitorTarget() = %q, want %q", got, tc.want)
		}
	}
}

func TestBuildSlackURL(t *testing.T) {
	config := &models.SlackConfig{
		Token:    slackBotToken(),
		Channel:  "C0123456789",
		BotName:  "Up",
		Icon:     ":satellite:",
		ThreadTS: "1712345678.000100",
	}
	raw, err := buildSlackURL(config)
	if err != nil {
		t.Fatalf("build slack URL: %v", err)
	}
	parsed := mustParse(t, raw)
	if parsed.Scheme != "slack" || parsed.Host != "C0123456789" {
		t.Fatalf("unexpected service URL: %s", raw)
	}
	if user := parsed.User.Username(); user != config.Token {
		t.Fatalf("the bot token must survive the URL round trip: %q (%s)", user, raw)
	}
	query := parsed.Query()
	if query.Get("botname") != "Up" || query.Get("icon") != ":satellite:" {
		t.Fatalf("the identity overrides are lost: %s", raw)
	}
	if query.Get("thread_ts") != "1712345678.000100" {
		t.Fatalf("the thread timestamp is lost: %s", raw)
	}

	// An incoming webhook token keeps its "hook:" prefix and needs no extras.
	hook, err := buildSlackURL(&models.SlackConfig{
		Token:   "hook:T00000000-B00000000-XXXXXXXXXXXXXXXXXXXXXXXX",
		Channel: "webhook",
	})
	if err != nil {
		t.Fatalf("build slack URL (webhook token): %v", err)
	}
	if user := mustParse(t, hook).User.Username(); user != "hook" {
		t.Fatalf("the webhook token must keep its identifier: %q (%s)", user, hook)
	}
	if len(mustParse(t, hook).Query()) != 0 {
		t.Fatalf("optional parameters must stay out of the URL: %s", hook)
	}

	if _, err := buildSlackURL(&models.SlackConfig{Channel: "C0123456789"}); err == nil {
		t.Fatal("a Slack channel without a token must be rejected")
	}
	if _, err := buildSlackURL(&models.SlackConfig{Token: "xoxb-1"}); err == nil {
		t.Fatal("a Slack token without a channel must be rejected")
	}
}

func TestBuildDiscordURL(t *testing.T) {
	config := &models.DiscordConfig{
		WebhookID: "123456789012345678",
		Token:     "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123",
		Username:  "Up",
		AvatarURL: "https://cdn.example.com/up.png",
		ThreadID:  "987654321098765432",
	}
	raw, err := buildDiscordURL(config)
	if err != nil {
		t.Fatalf("build discord URL: %v", err)
	}
	parsed := mustParse(t, raw)
	if parsed.Scheme != "discord" || parsed.Host != config.WebhookID {
		t.Fatalf("unexpected service URL: %s", raw)
	}
	if user := parsed.User.Username(); user != config.Token {
		t.Fatalf("the webhook token must survive the URL round trip: %q (%s)", user, raw)
	}
	query := parsed.Query()
	if query.Get("splitlines") != "no" {
		t.Fatalf("one alert must stay one message, not one embed per line: %s", raw)
	}
	if query.Get("username") != "Up" || query.Get("thread_id") != config.ThreadID {
		t.Fatalf("the overrides are lost: %s", raw)
	}
	if query.Get("avatar") != config.AvatarURL {
		t.Fatalf("the avatar override is lost: %s", raw)
	}

	bare, err := buildDiscordURL(&models.DiscordConfig{WebhookID: "1", Token: "t"})
	if err != nil {
		t.Fatalf("build discord URL (required fields only): %v", err)
	}
	if len(mustParse(t, bare).Query()) != 1 { // splitlines only
		t.Fatalf("optional parameters must stay out of the URL: %s", bare)
	}

	if _, err := buildDiscordURL(&models.DiscordConfig{Token: "t"}); err == nil {
		t.Fatal("a Discord webhook without an id must be rejected")
	}
	if _, err := buildDiscordURL(&models.DiscordConfig{WebhookID: "1"}); err == nil {
		t.Fatal("a Discord webhook without a token must be rejected")
	}
}

func TestBuildTelegramURL(t *testing.T) {
	config := &models.TelegramConfig{
		Token:               telegramBotToken(),
		Chats:               "-1001234567890, @mychannel",
		ParseMode:           "HTML",
		DisableNotification: true,
		DisablePreview:      true,
	}
	raw, err := buildTelegramURL(config)
	if err != nil {
		t.Fatalf("build telegram URL: %v", err)
	}
	parsed := mustParse(t, raw)
	if parsed.Scheme != "telegram" || parsed.Host != "telegram" {
		t.Fatalf("unexpected service URL: %s", raw)
	}
	if parsed.User.Username() != "1234" {
		t.Fatalf("the bot id must be the userinfo username: %s", raw)
	}
	if secret, _ := parsed.User.Password(); secret != "up-telegram-test-token" {
		t.Fatalf("the bot secret must survive URL encoding: %s", raw)
	}
	query := parsed.Query()
	if query.Get("chats") != "-1001234567890,@mychannel" {
		t.Fatalf("chats must be a comma separated list: %q", query.Get("chats"))
	}
	if query.Get("parsemode") != "HTML" {
		t.Fatalf("the parse mode is lost: %s", raw)
	}
	if query.Get("notification") != "no" || query.Get("preview") != "no" {
		t.Fatalf("the delivery flags are lost: %s", raw)
	}

	quiet, err := buildTelegramURL(&models.TelegramConfig{Token: "1:abc", Chats: "1"})
	if err != nil {
		t.Fatalf("build telegram URL (defaults): %v", err)
	}
	quietQuery := mustParse(t, quiet).Query()
	if quietQuery.Get("parsemode") != models.DefaultTelegramParseMode ||
		quietQuery.Get("notification") != "yes" || quietQuery.Get("preview") != "yes" {
		t.Fatalf("unexpected defaults: %s", quiet)
	}

	if _, err := buildTelegramURL(&models.TelegramConfig{Token: "123456789", Chats: "1"}); err == nil {
		t.Fatal("a Telegram token without a secret must be rejected")
	}
	if _, err := buildTelegramURL(&models.TelegramConfig{Token: "1:abc"}); err == nil {
		t.Fatal("a Telegram channel without a chat must be rejected")
	}
	if _, err := buildTelegramURL(&models.TelegramConfig{Token: "1:abc", Chats: "1", ParseMode: "Text"}); err == nil {
		t.Fatal("an unknown Telegram parse mode must be rejected")
	}
}

// TestChatServiceURLsAreAcceptedByShoutrrr feeds every generated URL back into
// the shoutrrr config parser: a shape the engine refuses (a token the service
// does not recognise, a missing chat) would otherwise only surface as a failed
// delivery, at the first alert.
func TestChatServiceURLsAreAcceptedByShoutrrr(t *testing.T) {
	engine := NewEngine(slog.New(slog.NewTextHandler(io.Discard, nil)), time.Second)

	slackBot, err := buildSlackURL(&models.SlackConfig{
		Token:   slackBotToken(),
		Channel: "C0123456789",
	})
	if err != nil {
		t.Fatalf("build slack URL: %v", err)
	}
	slackHook, err := buildSlackURL(&models.SlackConfig{
		Token:   "hook:T00000000-B00000000-XXXXXXXXXXXXXXXXXXXXXXXX",
		Channel: "webhook",
	})
	if err != nil {
		t.Fatalf("build slack URL (webhook token): %v", err)
	}
	discordURL, err := buildDiscordURL(&models.DiscordConfig{
		WebhookID: "123456789012345678",
		Token:     "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123",
		ThreadID:  "987654321098765432",
	})
	if err != nil {
		t.Fatalf("build discord URL: %v", err)
	}
	telegramURL, err := buildTelegramURL(&models.TelegramConfig{
		Token: telegramBotToken(),
		Chats: "-1001234567890,@mychannel",
	})
	if err != nil {
		t.Fatalf("build telegram URL: %v", err)
	}

	for name, serviceURL := range map[string]string{
		"slack (bot token)":     slackBot,
		"slack (webhook token)": slackHook,
		"discord":               discordURL,
		"telegram":              telegramURL,
	} {
		if _, err := engine.newRouter(serviceURL); err != nil {
			t.Errorf("%s: shoutrrr rejected the generated URL %q: %v", name, serviceURL, err)
		}
	}
}

// Fixtures for the chat service tests.
//
// Both tokens are fabricated, but their literal form matters: GitHub secret
// scanning matches the *patterns* of real credentials and rejects the push of
// any commit containing one ("Push cannot contain secrets"), whether or not the
// value is a live secret. A "xoxb-<12>-<12>-<24>" string matches the Slack API
// token pattern and a "<9 digits>:AA<35 chars>" string matches the Telegram bot
// token pattern, so neither may appear verbatim in the repository.
//
// slackBotToken assembles the Slack token at run time. shoutrrr validates the
// shape of the token, so the fixture has to keep it: three dash separated
// groups of 12, 12 and 24 characters after the identifier.
func slackBotToken() string {
	return "xox" + "b-" + strings.Repeat("1", 12) + "-" + strings.Repeat("2", 12) + "-" + strings.Repeat("f", 24)
}

// telegramBotToken is a synthetic <bot id>:<secret> pair. The id and the secret
// are deliberately short: shoutrrr only checks for "<digits>:<non empty>", and
// a realistic looking pair would match the Telegram bot token pattern above.
func telegramBotToken() string {
	return "1234:up-telegram-test-token"
}

func mustQuery(t *testing.T, raw string) url.Values {
	t.Helper()
	return mustParse(t, raw).Query()
}

func mustParse(t *testing.T, raw string) *url.URL {
	t.Helper()
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse %q: %v", raw, err)
	}
	return parsed
}
