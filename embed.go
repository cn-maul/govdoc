// Package govdoc embeds the built web UI.
//
// # Why this package is at the module root
//
// //go:embed resolves patterns relative to the declaring package's directory
// and rejects "..", so the file that embeds web/dist must itself live at or
// above web/dist. The module root is the only such place.
//
// Keeping the embed here (rather than in internal/server) has a second
// benefit: internal/server stays free of any knowledge of where the UI comes
// from. It accepts an fs.FS, so tests can serve a fixture directory and front
// end development can point at web/dist on disk.
package govdoc

import (
	"embed"
	"io/fs"
)

// WebDist holds the built front end.
//
// The pattern is "all:web/dist" so that Vite's dot-files and nested asset
// directories are included; the default "web/dist" form would skip files
// whose names begin with "_" or ".".
//
//go:embed all:web/dist
var WebDist embed.FS

// UI returns the UI filesystem rooted at web/dist, so index.html sits at the
// top level and the static handler needs no path prefix knowledge.
func UI() (fs.FS, error) {
	return fs.Sub(WebDist, "web/dist")
}
