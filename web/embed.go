// Package web embeds the compiled frontend (web/dist) into the Go binary, so
// the final image is a single self contained executable.
//
// The directory must contain at least an index.html placeholder for `go build`
// to work before the frontend has been built; the Docker image replaces it with
// the real Vite build output (see the Dockerfile, stage 1 and 2).
package web

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var embedded embed.FS

// Dist returns the embedded frontend rooted at the dist directory.
func Dist() (fs.FS, error) {
	return fs.Sub(embedded, "dist")
}

// Available reports whether a usable index.html is embedded.
func Available() bool {
	sub, err := Dist()
	if err != nil {
		return false
	}
	if _, err := fs.Stat(sub, "index.html"); err != nil {
		return false
	}
	return true
}
