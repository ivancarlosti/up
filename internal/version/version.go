// Package version exposes the build metadata injected at compile time through
// -ldflags (see the Dockerfile). The UI and the /api/version endpoint read it.
package version

import "strings"

// Values are replaced by the build flags:
//
//	-X github.com/ivancarlosti/up/internal/version.Version=<tag>
//	-X github.com/ivancarlosti/up/internal/version.Commit=<sha>
var (
	// Version is the semantic version of the build ("dev" for local builds).
	Version = "dev"
	// Commit is the git commit the build was produced from.
	Commit = "unknown"
)

// Readable returns a compact "version (commit)" string for logs and headers.
func Readable() string {
	if Commit == "" || Commit == "unknown" {
		return Version
	}
	short := Commit
	if len(short) > 12 {
		short = short[:12]
	}
	return strings.TrimSpace(Version + " (" + short + ")")
}
