package models

import "strings"

// The address family a probe prefers.
//
// A monitor that is only checked from one family cannot see a broken path on the
// other one: a host that answers over IPv4 while its TLS or HTTP service on IPv6
// is dead looks healthy forever. "alternate" answers exactly that - every
// execution flips between IPv4 and IPv6 - but it changes the verdict of a dual
// stack target whose second path is dead, so it is the opt-in and "auto" is the
// default:
//
//   - "auto"      the dialer's own choice, unchanged from before this setting
//     existed: the name is resolved once and its addresses are tried
//     with happy eyeballs (IPv6 first when the name has an AAAA and
//     the host has a usable IPv6 route, the other family 300 ms
//     later). The probe is up as soon as one family works, so a dead
//     family is hidden behind the working one - which is why the
//     default is a deliberate "the dialer knows best" and not a
//     promise that both paths were tested;
//   - "ipv4"/"ipv6" an explicit, HARD pin: the request is only ever dialed on
//     that family and fails when the target has no address in it
//     (what an operator wants to prove "this name really answers
//     here").
//
// "alternate" is a SOFT rotation: the family is only insisted on when the target
// really has an address in it, and a single stack target keeps working on every
// turn (it is never reported down for a family it does not have).
const (
	IPFamilyAuto      = "auto"
	IPFamilyAlternate = "alternate"
	IPFamily4         = "ipv4"
	IPFamily6         = "ipv6"
)

// DefaultIPFamily is the preference of every monitor that does not set one.
const DefaultIPFamily = IPFamilyAuto

// IPFamilies lists the accepted values in the order the UI shows them.
var IPFamilies = []string{IPFamilyAuto, IPFamilyAlternate, IPFamily4, IPFamily6}

// NormalizeIPFamily canonicalizes a stored or received value: the accepted
// spellings of a family become their canonical form and an empty value means the
// default (`auto`, the dialer's own choice), while anything else is returned
// trimmed and lowered so that ValidIPFamily can reject it. Normalize must not turn
// a typo into a silent default - the API answers 400 for it instead (see
// MonitorConfig.Validate).
func NormalizeIPFamily(raw string) string {
	switch trimmed := strings.ToLower(strings.TrimSpace(raw)); trimmed {
	case "":
		return DefaultIPFamily
	case IPFamilyAuto:
		return IPFamilyAuto
	case IPFamilyAlternate:
		return IPFamilyAlternate
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
