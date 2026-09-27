package models

import "strings"

// The address family a probe prefers.
//
// A monitor that is only checked from one family cannot see a broken path on the
// other one: a host that answers over IPv4 while its TLS or HTTP service on IPv6
// is dead looks healthy forever. "alternate" is therefore the default - every
// execution flips between IPv4 and IPv6 - and the other values are the opt-outs:
//
//   - "auto"      no preference at all: net.Dialer picks the family of the first
//     resolved address and tries the other one after 300 ms (happy
//     eyeballs), which is the classic behavior and can hide a dead
//     family behind a working one;
//   - "ipv4"/"ipv6" an explicit, HARD pin: the request is only ever dialed on
//     that family and fails when the target has no address in it
//     (what an operator wants to prove "this name really answers
//     here").
//
// "alternate" is a SOFT rotation: the family is only insisted on when the target
// really has an address in it, and a single stack target keeps working on every
// turn (it is never reported down for a family it does not have).
const (
	IPFamilyAlternate = "alternate"
	IPFamilyAuto      = "auto"
	IPFamily4         = "ipv4"
	IPFamily6         = "ipv6"
)

// DefaultIPFamily is the preference of every monitor that does not set one.
const DefaultIPFamily = IPFamilyAlternate

// IPFamilies lists the accepted values in the order the UI shows them.
var IPFamilies = []string{IPFamilyAlternate, IPFamilyAuto, IPFamily4, IPFamily6}

// NormalizeIPFamily canonicalizes a stored or received value: the accepted
// spellings of a family become their canonical form and an empty value means the
// default, while anything else is returned trimmed and lowered so that
// ValidIPFamily can reject it. Normalize must not turn a typo into a silent
// default - the API answers 400 for it instead (see MonitorConfig.Validate).
func NormalizeIPFamily(raw string) string {
	switch trimmed := strings.ToLower(strings.TrimSpace(raw)); trimmed {
	case "":
		return DefaultIPFamily
	case IPFamilyAlternate:
		return IPFamilyAlternate
	case IPFamilyAuto:
		return IPFamilyAuto
	case IPFamily4, "4", "v4", "tcp4":
		return IPFamily4
	case IPFamily6, "6", "v6", "tcp6":
		return IPFamily6
	default:
		return trimmed
	}
}

// ValidIPFamily reports whether raw is one of IPFamilies. An empty value is
// valid on purpose: a payload that omits the field keeps the default.
func ValidIPFamily(raw string) bool {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return true
	}
	return containsString(IPFamilies, trimmed)
}

// NextIPFamily is the family of the next execution of an alternating probe. It
// flips between the two families; anything that is not an explicit IPv4 (an
// empty stored value included) advances to IPv4, so a monitor created on any turn
// is checked on both families after two executions.
func NextIPFamily(current string) string {
	if NormalizeIPFamily(current) == IPFamily4 {
		return IPFamily6
	}
	return IPFamily4
}

// NetworkFor returns the net.Dialer network of a family: "4" or "6" (to be
// appended to "tcp"/"udp"), or "" when the value does not name one - which is
// where the dialer decides the family itself.
func NetworkFor(family string) string {
	switch NormalizeIPFamily(family) {
	case IPFamily4:
		return "4"
	case IPFamily6:
		return "6"
	default:
		return ""
	}
}
