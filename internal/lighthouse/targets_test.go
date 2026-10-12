package lighthouse

import (
	"testing"

	"github.com/ivancarlosti/up/internal/models"
)

// TestNormalizeURL pins the canonicalization that makes the deduplication work:
// two spellings of the same page must collapse into one API call.
func TestNormalizeURL(t *testing.T) {
	cases := []struct {
		raw  string
		want string
	}{
		{"https://example.com", "https://example.com/"},
		{"https://example.com/", "https://example.com/"},
		{"https://Example.COM/Path?A=1#frag", "https://example.com/Path?A=1"},
		{"https://example.com:443/", "https://example.com/"},
		{"http://example.com:80/", "http://example.com/"},
		{"https://example.com:8443/app", "https://example.com:8443/app"},
		{"https://user:secret@example.com/app", "https://example.com/app"},
		{"https://[2001:DB8::1]:8443/app", "https://[2001:db8::1]:8443/app"},
		{"https://[2001:db8::1]/app", "https://[2001:db8::1]/app"},
		// A WebSocket monitor and a bare host have no page to audit.
		{"wss://example.com/socket", ""},
		{"example.com", ""},
		{"", ""},
	}
	for _, testCase := range cases {
		got, ok := NormalizeURL(testCase.raw)
		if testCase.want == "" {
			if ok {
				t.Fatalf("NormalizeURL(%q) = %q, want no target", testCase.raw, got)
			}
			continue
		}
		if !ok || got != testCase.want {
			t.Fatalf("NormalizeURL(%q) = %q (ok=%v), want %q", testCase.raw, got, ok, testCase.want)
		}
	}
}

// TestTargetFor checks who measures and with which strategy: only the HTTP(s)
// types, only when the switch is on, and mobile unless the monitor says
// otherwise.
func TestTargetFor(t *testing.T) {
	measure := &models.Monitor{
		Type:              models.MonitorTypeHTTP,
		LighthouseEnabled: true,
		Config:            models.MonitorConfig{URL: "https://Example.com/"},
	}
	target, ok := TargetFor(measure, models.DefaultLighthouseStrategy)
	if !ok || target.URL != "https://example.com/" || target.Strategy != models.LighthouseStrategyMobile {
		t.Fatalf("target = %+v (ok=%v)", target, ok)
	}
	if target.Key() != "https://example.com/|mobile" {
		t.Fatalf("key = %q", target.Key())
	}

	desktop := &models.Monitor{
		Type:              models.MonitorTypeKeyword,
		LighthouseEnabled: true,
		Config:            models.MonitorConfig{URL: "https://example.com/", LighthouseStrategy: "Desktop"},
	}
	desktopTarget, ok := TargetFor(desktop, models.DefaultLighthouseStrategy)
	if !ok || desktopTarget.Strategy != models.LighthouseStrategyDesktop {
		t.Fatalf("strategy = %q (ok=%v)", desktopTarget.Strategy, ok)
	}

	// A type PageSpeed Insights cannot audit, a monitor that does not measure
	// and a target without an HTTP(S) URL have no target at all.
	others := []*models.Monitor{
		{Type: models.MonitorTypeWebSocket, LighthouseEnabled: true, Config: models.MonitorConfig{URL: "wss://example.com"}},
		{Type: models.MonitorTypeTCP, LighthouseEnabled: true, Config: models.MonitorConfig{Host: "example.com", Port: 443}},
		{Type: models.MonitorTypeHTTP, LighthouseEnabled: false, Config: models.MonitorConfig{URL: "https://example.com"}},
		{Type: models.MonitorTypeHTTP, LighthouseEnabled: true, Config: models.MonitorConfig{URL: "not a url"}},
		nil,
	}
	for _, monitor := range others {
		if _, ok := TargetFor(monitor, models.DefaultLighthouseStrategy); ok {
			t.Fatalf("monitor %+v must have no lighthouse target", monitor)
		}
	}
}

// TestIntervalFor checks where the cadence of a monitor comes from: its own
// configuration when it carries one, the shared job setting otherwise (a row
// written before the field existed, or an unknown value).
func TestIntervalFor(t *testing.T) {
	own := &models.Monitor{Config: models.MonitorConfig{LighthouseInterval: models.LighthouseIntervalHourly}}
	if got := IntervalFor(own, models.LighthouseIntervalDaily); got != models.LighthouseIntervalHourly {
		t.Fatalf("interval = %q", got)
	}

	// A monitor without a cadence follows the job.
	inherited := &models.Monitor{}
	if got := IntervalFor(inherited, models.LighthouseIntervalEvery6H); got != models.LighthouseIntervalEvery6H {
		t.Fatalf("inherited interval = %q", got)
	}
	// An unknown value is never trusted as is.
	bogus := &models.Monitor{Config: models.MonitorConfig{LighthouseInterval: "minutely"}}
	if got := IntervalFor(bogus, models.LighthouseIntervalWeekly); got != models.LighthouseIntervalWeekly {
		t.Fatalf("bogus interval = %q", got)
	}
	// A stored spelling is normalized.
	upper := &models.Monitor{Config: models.MonitorConfig{LighthouseInterval: " 12H "}}
	if got := IntervalFor(upper, models.LighthouseIntervalDaily); got != models.LighthouseIntervalEvery12H {
		t.Fatalf("normalized interval = %q", got)
	}
	// A nil monitor must not panic: the caller may hand over an empty plan.
	if got := IntervalFor(nil, ""); got != models.DefaultLighthouseInterval {
		t.Fatalf("nil interval = %q", got)
	}
}

// TestBuildPlan checks the worklist of a run: one entry per distinct target,
// with every monitor that must receive its result.
func TestBuildPlan(t *testing.T) {
	monitors := []*models.Monitor{
		{ID: 1, Type: models.MonitorTypeHTTP, LighthouseEnabled: true, Config: models.MonitorConfig{URL: "https://example.com"}},
		{ID: 2, Type: models.MonitorTypeKeyword, LighthouseEnabled: true, Config: models.MonitorConfig{URL: "https://EXAMPLE.com/"}},
		{ID: 3, Type: models.MonitorTypeHTTP, LighthouseEnabled: false, Config: models.MonitorConfig{URL: "https://other.example.com"}},
		{ID: 4, Type: models.MonitorTypeHTTP, LighthouseEnabled: true, Config: models.MonitorConfig{URL: "https://example.com", LighthouseStrategy: "desktop"}},
	}
	plan := BuildPlan(monitors, models.DefaultLighthouseStrategy)
	if len(plan.Targets) != 2 {
		t.Fatalf("targets = %+v", plan.Targets)
	}
	// The order follows the monitors, so a run is deterministic.
	if plan.Targets[0].Key() != "https://example.com/|mobile" || plan.Targets[1].Key() != "https://example.com/|desktop" {
		t.Fatalf("targets = %+v", plan.Targets)
	}
	if len(plan.Monitors["https://example.com/|mobile"]) != 2 {
		t.Fatalf("the two mobile monitors must share one target: %+v", plan.Monitors)
	}
	if len(plan.Monitors["https://example.com/|desktop"]) != 1 {
		t.Fatalf("the desktop monitor is its own target: %+v", plan.Monitors)
	}
}
