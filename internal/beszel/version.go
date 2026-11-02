package beszel

import (
	"fmt"
	"strconv"
	"strings"
)

// MinimumAgentVersion is the oldest agent this build accepts.
//
// 0.18 is the first release whose response envelope carries the request id (the
// generic `Data` field and the fingerprint handshake this package relies on), and
// the version the protocol was verified against. An older agent is refused at the
// handshake with a clear message instead of being silently misread.
const MinimumAgentVersion = "0.18.0"

// Version is a dotted semantic version, enough of one to compare agent builds.
//
// It is written here rather than pulled from a dependency: the only question
// asked is "is this agent at least X.Y.Z", and a full semver library would be a
// dependency for a comparison of three integers.
type Version struct {
	Major int
	Minor int
	Patch int
}

// ParseVersion reads a version reported by an agent (for example "0.21.1").
//
// A leading "beszel-agent-" and a pre-release suffix ("0.21.1-rc1") are
// tolerated: the agent reports plain versions today, and refusing a suffix would
// lock out a pre-release build over a detail that changes nothing here.
func ParseVersion(raw string) (Version, error) {
	value := strings.TrimSpace(raw)
	value = strings.TrimPrefix(value, "beszel-agent-")
	if index := strings.IndexAny(value, "-+"); index >= 0 {
		value = value[:index]
	}
	parts := strings.Split(value, ".")
	if len(parts) < 2 {
		return Version{}, fmt.Errorf("unreadable version %q", raw)
	}
	numbers := make([]int, 3)
	for i := 0; i < len(parts) && i < 3; i++ {
		number, err := strconv.Atoi(strings.TrimSpace(parts[i]))
		if err != nil || number < 0 {
			return Version{}, fmt.Errorf("unreadable version %q", raw)
		}
		numbers[i] = number
	}
	return Version{Major: numbers[0], Minor: numbers[1], Patch: numbers[2]}, nil
}

// AtLeast reports whether this version is greater than or equal to other.
func (v Version) AtLeast(other Version) bool {
	if v.Major != other.Major {
		return v.Major > other.Major
	}
	if v.Minor != other.Minor {
		return v.Minor > other.Minor
	}
	return v.Patch >= other.Patch
}

// String renders the version back as "X.Y.Z".
func (v Version) String() string {
	return fmt.Sprintf("%d.%d.%d", v.Major, v.Minor, v.Patch)
}

// Supported reports whether an agent version may connect.
func Supported(raw string) (Version, error) {
	version, err := ParseVersion(raw)
	if err != nil {
		return Version{}, err
	}
	minimum, err := ParseVersion(MinimumAgentVersion)
	if err != nil {
		return Version{}, err
	}
	if !version.AtLeast(minimum) {
		return version, fmt.Errorf("agent %s is older than %s", version, minimum)
	}
	return version, nil
}
