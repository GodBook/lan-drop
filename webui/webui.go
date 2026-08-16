// Package webui exposes the embedded web frontend as an fs.FS so every
// deliverable (CLI server, desktop app) shares one copy of the assets.
package webui

import (
	"embed"
	"io/fs"
)

//go:embed assets
var assetsFS embed.FS

// Assets returns the root of the embedded frontend (index.html, app.js, style.css).
func Assets() fs.FS {
	sub, err := fs.Sub(assetsFS, "assets")
	if err != nil {
		panic("webui: embedded assets missing: " + err.Error())
	}
	return sub
}
