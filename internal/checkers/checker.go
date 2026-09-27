// Package checkers implements the probe engines used by the scheduler: HTTP,
// HTTP Keyword, TCP and DNS.
//
// Every checker returns a Result with the heartbeat status, the measured
// latency, the optional status code and a short human readable message. The
// result is later stored in the heartbeats table and merged with the votes of
// the other cluster nodes.
package checkers

import (
	"context"
	"time"

	"github.com/ivancarlosti/up/internal/models"
)

// Result is the outcome of a single probe.
type Result struct {
	Status     models.HeartbeatStatus
	LatencyMS  int64
	StatusCode int
	Message    string
	// Certificate is the TLS certificate read by the probe (nil when the
	// monitor does not watch it or the target does not speak TLS).
	Certificate *models.CertificateInfo
}

// Check runs the probe matching the monitor type.
//
// turn is the rotation state of an alternating monitor (`config.ip_family ==
// "alternate"`): the scheduler hands the worker's current family so that half of
// the executions test the IPv6 path and half the IPv4 one; an empty turn means
// "whatever this configuration asks for", which is every execution of a monitor
// that does not rotate - `auto`, the default, included. See planFor for the exact
// rules.
//
// Upside down monitors are handled here: the probed outcome is inverted so the
// rest of the pipeline (voting, notifications, uptime) stays untouched.
func Check(ctx context.Context, monitor *models.Monitor, turn string) Result {
	timeout := time.Duration(monitor.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	plan := planFor(monitor.Config.IPFamily, turn)
	var result Result
	switch monitor.Type {
	case models.MonitorTypeHTTP:
		result = checkHTTP(ctx, monitor, plan)
	case models.MonitorTypeKeyword:
		result = checkKeyword(ctx, monitor, plan)
	case models.MonitorTypeTCP:
		result = checkTCP(ctx, monitor, plan)
	case models.MonitorTypeDNS:
		result = checkDNS(ctx, monitor, plan)
	case models.MonitorTypeSSL:
		result = checkSSL(ctx, monitor, plan)
	default:
		result = Result{Status: models.StatusDown, Message: "unsupported monitor type " + string(monitor.Type)}
	}

	if monitor.UpsideDown {
		switch result.Status {
		case models.StatusUp:
			result.Status = models.StatusDown
			result.Message = "upside down: " + result.Message
		case models.StatusDown:
			result.Status = models.StatusUp
			result.Message = "upside down: " + result.Message
		}
	}
	return result
}

// timeoutFromContext returns the remaining budget of the check.
func timeoutFromContext(ctx context.Context) time.Duration {
	if deadline, ok := ctx.Deadline(); ok {
		if remaining := time.Until(deadline); remaining > 0 {
			return remaining
		}
	}
	return 10 * time.Second
}
