package services

import (
	"strings"
	"testing"

	"github.com/ivancarlosti/up/internal/models"
)

// TestParseBulkText covers the pasted spreadsheet format: delimiters, headers,
// comments, quoted cells and the line numbers the preview shows.
func TestParseBulkText(t *testing.T) {
	cases := []struct {
		name  string
		text  string
		lines []int
		first BulkRow
	}{
		{
			name:  "comma separated with header",
			text:  "name,url\nAPI,https://api.example.com/health\nSite,https://example.com\n",
			lines: []int{2, 3},
			first: BulkRow{Line: 2, Name: "API", Target: "https://api.example.com/health"},
		},
		{
			name:  "semicolon separated",
			text:  "API;https://api.example.com\n",
			lines: []int{1},
			first: BulkRow{Line: 1, Name: "API", Target: "https://api.example.com"},
		},
		{
			name:  "tab separated from a spreadsheet",
			text:  "API\thttps://api.example.com\n",
			lines: []int{1},
			first: BulkRow{Line: 1, Name: "API", Target: "https://api.example.com"},
		},
		{
			name:  "comments and blank lines are skipped",
			text:  "# my endpoints\n\nAPI,https://api.example.com\n   \n",
			lines: []int{3},
			first: BulkRow{Line: 3, Name: "API", Target: "https://api.example.com"},
		},
		{
			name:  "quoted cells with the delimiter inside",
			text:  "\"API, primary\",https://api.example.com\n",
			lines: []int{1},
			first: BulkRow{Line: 1, Name: "API, primary", Target: "https://api.example.com"},
		},
		{
			name:  "optional columns",
			text:  "Site,example.com,keyword,maintenance,tag-a,30\n",
			lines: []int{1},
			first: BulkRow{Line: 1, Name: "Site", Target: "example.com", Type: "keyword", Keyword: "maintenance", Tags: "tag-a", Interval: "30"},
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			rows := ParseBulkText(testCase.text)
			if len(rows) != len(testCase.lines) {
				t.Fatalf("got %d rows (%+v), want %d", len(rows), rows, len(testCase.lines))
			}
			for i, line := range testCase.lines {
				if rows[i].Line != line {
					t.Fatalf("row %d has line %d, want %d", i, rows[i].Line, line)
				}
			}
			if rows[0] != testCase.first {
				t.Fatalf("first row = %+v, want %+v", rows[0], testCase.first)
			}
		})
	}
}

// TestParseBulkTextErrors documents the messages the operator sees in the preview.
func TestParseBulkTextErrors(t *testing.T) {
	rows := ParseBulkText("Name,https://example.com\n,https://example.com/api\nSolo\nSite,example.com,planet\n")
	if len(rows) != 4 {
		t.Fatalf("got %d rows, want 4", len(rows))
	}
	if rows[0].Error != "" {
		t.Fatalf("the first row is valid, got %q", rows[0].Error)
	}
	if rows[1].Error != "name is required" {
		t.Fatalf("missing name: got %q", rows[1].Error)
	}
	if rows[2].Error != "the target (second column) is required" {
		t.Fatalf("missing target: got %q", rows[2].Error)
	}
	if !strings.Contains(rows[3].Error, "unknown type planet") {
		t.Fatalf("unknown type: got %q", rows[3].Error)
	}
}

// TestMonitorFromBulkRow checks the target mapping per monitor type: this is the
// piece that turns "name,host" into a real monitor.
func TestMonitorFromBulkRow(t *testing.T) {
	httpTemplate := &models.MonitorTemplate{
		Type:     models.MonitorTypeHTTP,
		Config:   models.MonitorConfig{Method: "HEAD", AcceptedStatusCodes: "200-299"},
		Defaults: models.TemplateDefaults{IntervalSeconds: 60, TimeoutSeconds: 10, RunOn: "all", Tags: "from-template"},
	}

	monitor, err := monitorFromBulkRow(httpTemplate, BulkRow{Line: 1, Name: "API", Target: "https://api.example.com/health"}, BulkOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if monitor.Config.URL != "https://api.example.com/health" {
		t.Fatalf("url = %q", monitor.Config.URL)
	}
	if monitor.Config.Method != "HEAD" {
		t.Fatalf("the template config must be kept, method = %q", monitor.Config.Method)
	}
	// A template no longer sets the tags: they belong to the monitor, so two
	// monitors can follow one template with different tags.
	if monitor.Tags != "" {
		t.Fatalf("a template must not set the tags, got %q", monitor.Tags)
	}

	// The url of an HTTP monitor must be absolute: a bare host would make every
	// check fail with a confusing error.
	if _, err := monitorFromBulkRow(httpTemplate, BulkRow{Name: "API", Target: "api.example.com"}, BulkOptions{}); err == nil {
		t.Fatal("a url without a scheme must be rejected")
	}

	// A row can override the type, the keyword and the tags.
	keyword, err := monitorFromBulkRow(httpTemplate, BulkRow{
		Name: "Site", Target: "https://example.com", Type: "keyword", Keyword: "maintenance", Tags: "row-tag",
	}, BulkOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if keyword.Type != models.MonitorTypeKeyword || keyword.Config.Keyword != "maintenance" || keyword.Tags != "row-tag" {
		t.Fatalf("row overrides ignored: %+v", keyword)
	}
	if keyword.Config.Method != "HEAD" {
		t.Fatal("the row override must not drop the template configuration")
	}
	if _, err := monitorFromBulkRow(httpTemplate, BulkRow{Name: "Site", Target: "https://example.com", Type: "keyword"}, BulkOptions{}); err == nil {
		t.Fatal("a keyword monitor without a keyword must be rejected")
	}

	// The row interval overrides the template one.
	interval, err := monitorFromBulkRow(httpTemplate, BulkRow{Name: "API", Target: "https://api.example.com", Interval: "30"}, BulkOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if interval.IntervalSeconds != 30 {
		t.Fatalf("interval = %d, want 30", interval.IntervalSeconds)
	}
	if _, err := monitorFromBulkRow(httpTemplate, BulkRow{Name: "API", Target: "https://api.example.com", Interval: "1"}, BulkOptions{}); err == nil {
		t.Fatal("an interval below 5 must be rejected")
	}
}

// TestSplitHostPort keeps the parsing of `host` / `host:port` honest: the port of
// the template is only a fallback.
func TestSplitHostPort(t *testing.T) {
	if host, port, err := splitHostPort("db.example.com:3306", 0); err != nil || host != "db.example.com" || port != 3306 {
		t.Fatalf("host:port = %q:%d err=%v", host, port, err)
	}
	if host, port, err := splitHostPort("db.example.com", 5432); err != nil || host != "db.example.com" || port != 5432 {
		t.Fatalf("fallback port = %q:%d err=%v", host, port, err)
	}
	if _, _, err := splitHostPort("db.example.com:abc", 0); err == nil {
		t.Fatal("a non numeric port must be rejected")
	}
	if _, _, err := splitHostPort(":3306", 0); err == nil {
		t.Fatal("an empty host must be rejected")
	}
	if _, _, err := splitHostPort("db.example.com", 0); err == nil {
		t.Fatal("a missing port without a template fallback must be rejected")
	}
}

// TestMonitorFromBulkRowCoversEveryType documents that the bulk importer speaks
// every probe type: each template type parses its own target shape, and the ssl
// type (like tcp) takes the port of the template, then 443 when it has none.
func TestMonitorFromBulkRowCoversEveryType(t *testing.T) {
	defaults := models.TemplateDefaults{IntervalSeconds: 60, TimeoutSeconds: 10, RunOn: "all"}

	// HTTP and Keyword: the target is an absolute url and the keyword comes from
	// the row.
	httpTemplate := &models.MonitorTemplate{UUID: "http-template", Type: models.MonitorTypeHTTP, Defaults: defaults}
	httpRow, err := monitorFromBulkRow(httpTemplate, BulkRow{Name: "API", Target: "https://api.example.com"}, BulkOptions{})
	if err != nil || httpRow.Config.URL != "https://api.example.com" {
		t.Fatalf("http row = %+v err=%v", httpRow, err)
	}
	keywordRow, err := monitorFromBulkRow(httpTemplate, BulkRow{Name: "Site", Target: "https://example.com", Type: "keyword", Keyword: "sign in"}, BulkOptions{})
	if err != nil || keywordRow.Type != models.MonitorTypeKeyword || keywordRow.Config.Keyword != "sign in" {
		t.Fatalf("keyword row = %+v err=%v", keywordRow, err)
	}

	// TCP: host:port, the port of the template being the fallback of the row.
	tcpTemplate := &models.MonitorTemplate{UUID: "tcp-template", Type: models.MonitorTypeTCP, Config: models.MonitorConfig{Port: 5432}, Defaults: defaults}
	tcpRow, err := monitorFromBulkRow(tcpTemplate, BulkRow{Name: "MariaDB", Target: "db.internal"}, BulkOptions{})
	if err != nil || tcpRow.Config.Host != "db.internal" || tcpRow.Config.Port != 5432 {
		t.Fatalf("tcp row = %+v err=%v", tcpRow, err)
	}
	tcpRow, err = monitorFromBulkRow(tcpTemplate, BulkRow{Name: "MariaDB", Target: "db.internal:3306"}, BulkOptions{})
	if err != nil || tcpRow.Config.Port != 3306 {
		t.Fatalf("the port of the row must win: %+v err=%v", tcpRow, err)
	}

	// SSL: host:port as well, the template port first and DefaultSSLPort (443)
	// when the template does not give one.
	sslTemplate := &models.MonitorTemplate{UUID: "ssl-template", Type: models.MonitorTypeSSL, Config: models.MonitorConfig{Port: 993}, Defaults: defaults}
	sslRow, err := monitorFromBulkRow(sslTemplate, BulkRow{Name: "IMAPS", Target: "mail.example.com"}, BulkOptions{})
	if err != nil || sslRow.Type != models.MonitorTypeSSL || sslRow.Config.Host != "mail.example.com" || sslRow.Config.Port != 993 {
		t.Fatalf("ssl row = %+v err=%v", sslRow, err)
	}
	sslDefault := &models.MonitorTemplate{UUID: "ssl-default", Type: models.MonitorTypeSSL, Defaults: defaults}
	sslRow, err = monitorFromBulkRow(sslDefault, BulkRow{Name: "Site", Target: "example.com"}, BulkOptions{})
	if err != nil || sslRow.Config.Port != models.DefaultSSLPort {
		t.Fatalf("the ssl default port must be 443: %+v err=%v", sslRow, err)
	}
	if _, err := monitorFromBulkRow(sslDefault, BulkRow{Name: "Site", Target: "example.com:abc"}, BulkOptions{}); err == nil {
		t.Fatal("a non numeric port must be reported as invalid")
	}

	// DNS: the target is the name to resolve.
	dnsTemplate := &models.MonitorTemplate{UUID: "dns-template", Type: models.MonitorTypeDNS, Defaults: defaults}
	dnsRow, err := monitorFromBulkRow(dnsTemplate, BulkRow{Name: "example.com A", Target: "example.com"}, BulkOptions{})
	if err != nil || dnsRow.Config.Hostname != "example.com" || dnsRow.Config.RecordType != "A" {
		t.Fatalf("dns row = %+v err=%v", dnsRow, err)
	}

	// A row that keeps the type of its template follows it.
	for _, monitor := range []*models.Monitor{httpRow, tcpRow, dnsRow, sslRow} {
		if monitor.TemplateUUID == "" {
			t.Fatalf("%s must follow the template: %+v", monitor.Name, monitor)
		}
	}
}

// TestMonitorFromBulkRowTypeOverride documents what the `type` column of a row
// changes: the monitor is created with the type of the row, keeps the scheduling
// defaults of the template, drops the options of the template type and is NOT
// linked to it (a link between two different types is what the API rejects).
func TestMonitorFromBulkRowTypeOverride(t *testing.T) {
	template := &models.MonitorTemplate{
		UUID: "template-uuid",
		Type: models.MonitorTypeHTTP,
		Config: models.MonitorConfig{
			Method: "HEAD", AcceptedStatusCodes: "200", Headers: []models.Header{{Key: "X", Value: "1"}},
		},
		Defaults: models.TemplateDefaults{
			IntervalSeconds: 120, TimeoutSeconds: 5, Retries: 2, RetriesIntervalSeconds: 30,
			RunOn: "all", CertWatch: true, CertNotify: true, CertWarnDays: "7",
			NotificationIDs: []uint{3},
		},
	}

	row, err := monitorFromBulkRow(template, BulkRow{Name: "MariaDB", Target: "db.internal:3306", Type: "tcp"}, BulkOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if row.Type != models.MonitorTypeTCP || row.Config.Host != "db.internal" || row.Config.Port != 3306 {
		t.Fatalf("the type and the target of the row must win: %+v", row)
	}
	if row.IntervalSeconds != 120 || row.TimeoutSeconds != 5 || row.Retries != 2 || row.RetriesIntervalSeconds != 30 {
		t.Fatalf("the scheduling defaults must be kept: %+v", row)
	}
	if row.Config.Method != "" || row.Config.AcceptedStatusCodes != "" || len(row.Config.Headers) != 0 {
		t.Fatalf("the options of the template type must be dropped: %+v", row.Config)
	}
	if row.TemplateUUID != "" {
		t.Fatalf("an overridden row cannot follow the template, got %q", row.TemplateUUID)
	}
	// A certificate watch only exists for the types that can read one: the API
	// would reject the monitor otherwise.
	if row.CertWatch || row.CertNotify || row.CertWarnDays != "" {
		t.Fatalf("the certificate switches of an http template must not reach a tcp monitor: %+v", row)
	}

	// A row that keeps the type of its template follows it and inherits its
	// certificate defaults.
	same, err := monitorFromBulkRow(template, BulkRow{Name: "API", Target: "https://api.example.com"}, BulkOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if same.TemplateUUID != "template-uuid" || same.Config.Method != "HEAD" {
		t.Fatalf("a row of the template type must follow it: %+v", same)
	}
	if !same.CertWatch || !same.CertNotify || same.CertWarnDays != "7" {
		t.Fatalf("the certificate defaults must be kept for a supported type: %+v", same)
	}
}

// TestMonitorTargetKeyPerType documents the identity the bulk importer uses to
// recognise a duplicate. Without the ssl case every ssl row shared the empty key,
// so all but the first one were reported as duplicates.
func TestMonitorTargetKeyPerType(t *testing.T) {
	cases := []struct {
		monitor *models.Monitor
		want    string
	}{
		{&models.Monitor{Type: models.MonitorTypeHTTP, Config: models.MonitorConfig{URL: "https://API.example.com/x"}}, "https://api.example.com/x"},
		{&models.Monitor{Type: models.MonitorTypeKeyword, Config: models.MonitorConfig{URL: "https://example.com"}}, "https://example.com"},
		{&models.Monitor{Type: models.MonitorTypeTCP, Config: models.MonitorConfig{Host: "DB.internal", Port: 3306}}, "db.internal:3306"},
		{&models.Monitor{Type: models.MonitorTypeSSL, Config: models.MonitorConfig{Host: "mail.example.com", Port: 993}}, "mail.example.com:993"},
		{&models.Monitor{Type: models.MonitorTypeDNS, Config: models.MonitorConfig{Hostname: "Example.com"}}, "example.com"},
	}
	for _, testCase := range cases {
		if got := monitorTargetKey(testCase.monitor); got != testCase.want {
			t.Errorf("%s target key = %q, want %q", testCase.monitor.Type, got, testCase.want)
		}
	}
}
