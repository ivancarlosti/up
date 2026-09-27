package checkers

import (
	"context"
	"net"
	"time"

	"github.com/ivancarlosti/up/internal/models"
)

// probePlan is the resolved dialing plan of a single probe.
//
// It is what `config.ip_family` plus the turn of the worker (models.IPFamily,
// models.NextIPFamily) boil down to, and it is deliberately small: which family,
// if any, and whether that family is negotiable.
type probePlan struct {
	// family is the family the probe insists on: "ipv4", "ipv6" or "" when the
	// dialer is free to choose (happy eyeballs).
	family string
	// hard is true when family must be honored even if the target has no address
	// in it - the explicit operator choice (`ipv4`/`ipv6`), which must fail
	// instead of silently testing the other path.
	hard bool
}

// planFor builds the plan of one probe.
//
// preference is `config.ip_family` and turn is the rotation state of the worker
// ("ipv4" or "ipv6", empty when the monitor does not rotate):
//
//   - `ipv4`/`ipv6` -> a hard pin, the turn is ignored;
//   - `alternate` -> a soft pin of the turn: the family is only used when the
//     target has an address in it, so a single stack target keeps working on
//     every execution;
//   - `auto` -> no pin at all. Anything else (an unreadable value the API would
//     have rejected) lands here too, which is the safe direction: the probe keeps
//     dialing the way it always did.
func planFor(preference, turn string) probePlan {
	switch models.NormalizeIPFamily(preference) {
	case models.IPFamily4:
		return probePlan{family: models.IPFamily4, hard: true}
	case models.IPFamily6:
		return probePlan{family: models.IPFamily6, hard: true}
	case models.IPFamilyAlternate:
		switch models.NormalizeIPFamily(turn) {
		case models.IPFamily4:
			return probePlan{family: models.IPFamily4}
		case models.IPFamily6:
			return probePlan{family: models.IPFamily6}
		}
		return probePlan{}
	default: // auto
		return probePlan{}
	}
}

// literalFamily is the family of an IP literal. An IPv4-mapped IPv6 address
// (::ffff:52.37.99.5) is an IPv4 address, exactly like net.Dialer sees it.
func literalFamily(ip net.IP) string {
	if ip.To4() != nil {
		return models.IPFamily4
	}
	return models.IPFamily6
}

// probeTarget resolves the plan against one address and returns the network and
// the address the dialer must use.
//
// A negotiable ("soft") family is dropped as soon as the target has no address in
// it, and the dialer then gets the plain host name back, so a single stack target
// is tested on the family it has instead of being reported down for the one it
// does not. A hard family is always kept: the dialer's own error then names the
// family the operator asked for.
//
// For a name, the chosen address is dialed as a literal, so the probe tests
// exactly one path instead of leaving the choice to happy eyeballs.
func probeTarget(ctx context.Context, plan probePlan, address string) (string, string) {
	if plan.family == "" {
		return "tcp", address
	}
	network := "tcp" + models.NetworkFor(plan.family)
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return network, address
	}
	if literal := net.ParseIP(host); literal != nil {
		if plan.hard || literalFamily(literal) == plan.family {
			return network, address
		}
		return "tcp", address
	}
	// A name: one lookup decides whether the family exists at all. A failed
	// lookup is not fatal here - the dialer reports it much better than this
	// function could.
	ips, lookupErr := net.DefaultResolver.LookupIPAddr(ctx, host)
	if lookupErr == nil {
		for _, entry := range ips {
			if literalFamily(entry.IP) == plan.family {
				return network, net.JoinHostPort(entry.IP.String(), port)
			}
		}
	}
	if plan.hard {
		return network, address
	}
	return "tcp", address
}

// dialProbe opens the connection of one probe following the plan.
func dialProbe(ctx context.Context, plan probePlan, address string, timeout time.Duration) (net.Conn, error) {
	network, target := probeTarget(ctx, plan, address)
	if timeout <= 0 {
		timeout = timeoutFromContext(ctx)
	}
	dialer := &net.Dialer{Timeout: timeout, KeepAlive: 30 * time.Second}
	return dialer.DialContext(ctx, network, target)
}
