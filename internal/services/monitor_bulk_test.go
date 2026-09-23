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
	if monitor.Tags != "from-template" {
		t.Fatalf("tags from the template = %q", monitor.Tags)
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
