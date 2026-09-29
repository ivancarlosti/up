package expiry

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"syscall"
	"time"

	"github.com/ivancarlosti/up/internal/models"
)

// registryDialProbeBudget bounds the diagnostic pass of a failed "auto" dial.
//
// It runs after the lookup already failed, so it must stay small: the daily job
// walks dozens of registries and the caller's timeout is the real budget.
const registryDialProbeBudget = 5 * time.Second

// registryDialProbeTimeout caps ONE probe address of the diagnostic pass.
const registryDialProbeTimeout = 2 * time.Second

// registryDialer is the dialer of every registry connection (WHOIS port 43, RDAP
// port 443). KeepAlive matches http.DefaultTransport so an RDAP connection reused
// across lookups is not dropped behind our back.
func registryDialer(timeout time.Duration) *net.Dialer {
	if timeout <= 0 {
		timeout = models.DefaultExpiryTimeoutSeconds * time.Second
	}
	return &net.Dialer{Timeout: timeout, KeepAlive: 30 * time.Second}
}

// familyNetwork is the network of a single resolved address.
func familyNetwork(ip net.IP) string {
	if ip.To4() != nil {
		return "tcp4"
	}
	return "tcp6"
}

// dialRegistry opens a TCP connection to a registry endpoint.
//
// The dial is always "tcp", so Go's happy eyeballs applies: the family of the
// first resolved address is tried at once and the other one follows after 300 ms
// sharing the one deadline. There is no per-lookup way to choose a family on
// purpose: a registry that only answers on IPv6 (whois.nic.io on port 43) is
// unreachable from a container whose network has no IPv6 route, and the fix is to
// give the container such a route (network_mode: host), not to pin a family that
// is known to be dead.
//
// The reason a failing dial is so hard to read is exactly that happy eyeballs
// reports ONLY the error of the family it tried first, so a blackholed IPv4 path
// hides the fact that the container has no IPv6 route at all (the Docker
// default) - the operator sees "dial tcp 52.37.99.5:43: i/o timeout" while the
// host that runs Docker answers fine because IT reaches the registry over IPv6.
// registryDialError re-dials every resolved address to name them all.
func dialRegistry(ctx context.Context, address string, timeout time.Duration) (net.Conn, error) {
	conn, err := registryDialer(timeout).DialContext(ctx, "tcp", address)
	if err == nil {
		return conn, nil
	}
	if ctx.Err() != nil {
		// A cancelled context must not be kept alive by diagnostics.
		return nil, err
	}
	return nil, registryDialError(ctx, address, err)
}

// dialAttempt is one probe of the diagnostic pass (a nil error means the address
// answered).
type dialAttempt struct {
	ip  net.IP
	err error
}

// registryDialError explains a failed "auto" dial: it probes every address the
// name resolves to, one family at a time and inside its own small budget, and
// joins their errors.
func registryDialError(ctx context.Context, address string, cause error) error {
	host, port, splitErr := net.SplitHostPort(address)
	if splitErr != nil {
		return cause
	}
	probeCtx, cancel := context.WithTimeout(ctx, registryDialProbeBudget)
	defer cancel()
	resolved, lookupErr := net.DefaultResolver.LookupIPAddr(probeCtx, host)
	if lookupErr != nil || len(resolved) == 0 {
		return fmt.Errorf("%w (the name %q resolved to no address: %v)", cause, host, lookupErr)
	}
	deadline, _ := probeCtx.Deadline()
	attempts := make([]dialAttempt, 0, len(resolved))
	for index, entry := range resolved {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			break
		}
		// Each address gets an equal share of what is left, capped so a single
		// dead address cannot eat the whole pass.
		per := remaining / time.Duration(len(resolved)-index)
		if per > registryDialProbeTimeout {
			per = registryDialProbeTimeout
		}
		target := net.JoinHostPort(entry.IP.String(), port)
		attemptCtx, attemptCancel := context.WithTimeout(probeCtx, per)
		conn, dialErr := registryDialer(per).DialContext(attemptCtx, familyNetwork(entry.IP), target)
		attemptCancel()
		if dialErr == nil {
			_ = conn.Close()
		}
		attempts = append(attempts, dialAttempt{ip: entry.IP, err: dialErr})
	}
	if len(attempts) == 0 {
		return cause
	}
	return errors.New(describeDialAttempts(host, attempts))
}

// describeDialAttempts is the message the operator (and the "test parser" box)
// reads: every resolved address with its own error, plus the hint that turns
// "i/o timeout" into an actionable instruction.
func describeDialAttempts(host string, attempts []dialAttempt) string {
	parts := make([]string, 0, len(attempts))
	reached := ""
	ipv6Attempts, ipv6Unreachable := 0, 0
	for _, attempt := range attempts {
		if attempt.err == nil {
			reached = attempt.ip.String()
			parts = append(parts, attempt.ip.String()+": connected")
			continue
		}
		parts = append(parts, attempt.err.Error())
		if attempt.ip.To4() != nil {
			continue
		}
		ipv6Attempts++
		if unreachableNetwork(attempt.err) {
			ipv6Unreachable++
		}
	}
	message := fmt.Sprintf("dial %s failed: %s", host, strings.Join(parts, "; "))
	if reached != "" {
		return message + " - " + reached + " answered on a retry: the first attempt was probably rate limited, try again"
	}
	if ipv6Attempts > 0 && ipv6Unreachable == ipv6Attempts {
		return message + " - this host has no IPv6 route (Docker networks are IPv4-only by default), while the registry answers on IPv6 only: run the container on the host network (network_mode: host, see docker-compose-host.yml), as described in the Network egress section of the README"
	}
	return message
}

// unreachableNetwork reports whether an error means "this host has no route to
// that family", as opposed to a timeout or a refused connection.
func unreachableNetwork(err error) bool {
	return errors.Is(err, syscall.ENETUNREACH) || errors.Is(err, syscall.EHOSTUNREACH)
}
