// Package lighthouse scores a web page with Google Lighthouse through the
// PageSpeed Insights API.
//
// The package is deliberately free of database and HTTP-handler concerns (the
// same rule the expiry package follows): it owns the network call and the
// parsing of the answer, while the services package owns the storage, the
// schedule and the alerts. Everything it needs from the caller is a Target.
package lighthouse

import (
	"net/url"
	"strings"

	"github.com/ivancarlosti/up/internal/models"
)

// Target is one deduplicated unit of work: the page to score and the strategy
// to score it with.
//
// The two fields together are the identity of the work, exactly like the
// "host:port|sni" key of a certificate target: two monitors on the same page
// with the same strategy share ONE PageSpeed Insights call (and one API quota
// unit), while switching a monitor to desktop is a different target.
type Target struct {
	URL      string
	Strategy string
}

// Key is the deduplication identity of the target.
func (t Target) Key() string { return t.URL + "|" + t.Strategy }

// TargetFor builds the target of a monitor and reports whether it has one.
//
// It returns false for a monitor that does not measure its page, for a type
// PageSpeed Insights cannot audit and for a target that is not an absolute
// HTTP(S) URL. defaultStrategy is used when the monitor does not carry a
// strategy of its own (a row written before the field existed).
func TargetFor(monitor *models.Monitor, defaultStrategy string) (Target, bool) {
	if monitor == nil || !monitor.LighthouseEnabled || !monitor.Type.SupportsLighthouse() {
		return Target{}, false
	}
	page, ok := NormalizeURL(monitor.Config.URL)
	if !ok {
		return Target{}, false
	}
	strategy := strings.TrimSpace(monitor.Config.LighthouseStrategy)
	if strategy == "" {
		strategy = defaultStrategy
	}
	return Target{
		URL:      page,
		Strategy: models.NormalizeLighthouseStrategy(strategy),
	}, true
}

// IntervalFor resolves the cadence a monitor measures its page with: its own when
// it carries a valid one, the shared fallback of the job otherwise.
//
// The cadence is a property of the monitor (it travels with the strategy, in the
// configuration), but a monitor written before the field existed carries none:
// falling back to the job setting keeps such a row measuring exactly as it did,
// without a migration.
func IntervalFor(monitor *models.Monitor, fallback string) string {
	if monitor != nil {
		if raw := strings.TrimSpace(monitor.Config.LighthouseInterval); models.ValidLighthouseInterval(raw) {
			return models.NormalizeLighthouseInterval(raw)
		}
	}
	return models.NormalizeLighthouseInterval(fallback)
}

// NormalizeURL canonicalizes a monitor URL so two spellings of the same page
// deduplicate into one target.
//
// The scheme and the host are lower cased, a default port is dropped, the
// fragment is removed (it is never sent to a server) and an empty path becomes
// "/" so "https://example.com" and "https://example.com/" are the same target.
// Credentials are dropped on purpose: they are the monitor's business, never
// the API's. The query string is kept, because it often selects the page.
func NormalizeURL(raw string) (string, bool) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return "", false
	}
	parsed, err := url.Parse(value)
	if err != nil {
		return "", false
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" {
		return "", false
	}
	host := strings.ToLower(parsed.Hostname())
	if host == "" {
		return "", false
	}
	// An IPv6 literal keeps its brackets in an authority.
	authority := host
	if strings.Contains(host, ":") {
		authority = "[" + host + "]"
	}
	if port := parsed.Port(); port != "" && !isDefaultPort(scheme, port) {
		authority += ":" + port
	}
	path := parsed.EscapedPath()
	if path == "" {
		path = "/"
	}
	out := scheme + "://" + authority + path
	if parsed.RawQuery != "" {
		out += "?" + parsed.RawQuery
	}
	return out, true
}

// isDefaultPort reports whether the port is the implicit one of the scheme.
func isDefaultPort(scheme, port string) bool {
	return (scheme == "http" && port == "80") || (scheme == "https" && port == "443")
}

// Plan is the deduplicated worklist of one run.
type Plan struct {
	// Targets is every distinct target, in the order the monitors were read.
	Targets []Target
	// Monitors maps a target key to the monitors that share it (never empty).
	Monitors map[string][]*models.Monitor
}

// BuildPlan turns a monitor listing into the worklist of a run: one entry per
// distinct target, with the monitors that must receive its result.
//
// It is pure, which is what lets the daily job, the admin run-now and the
// per-monitor refresh share exactly the same definition of "the same target".
// defaultStrategy is passed to TargetFor.
func BuildPlan(monitors []*models.Monitor, defaultStrategy string) Plan {
	plan := Plan{Monitors: map[string][]*models.Monitor{}}
	for _, monitor := range monitors {
		target, ok := TargetFor(monitor, defaultStrategy)
		if !ok {
			continue
		}
		key := target.Key()
		if _, seen := plan.Monitors[key]; !seen {
			plan.Targets = append(plan.Targets, target)
		}
		plan.Monitors[key] = append(plan.Monitors[key], monitor)
	}
	return plan
}
