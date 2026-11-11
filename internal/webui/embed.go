// Package webui embeds the compiled browser UI into the hawser binary.
//
// The production bundle is written to dist/app by `npm run build` in web/.
// A placeholder page is served when the bundle has not been built.
package webui

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var dist embed.FS

// FS returns the UI file system and whether the real bundle is present.
func FS() (fs.FS, bool) {
	if app, err := fs.Sub(dist, "dist/app"); err == nil {
		if _, err := fs.Stat(app, "index.html"); err == nil {
			return app, true
		}
	}
	sub, _ := fs.Sub(dist, "dist")
	return sub, false
}
