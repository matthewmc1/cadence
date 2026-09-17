// Package web embeds the built SPA so a single static binary serves both the
// API and the web app (the private-deployment shape: one origin, no CDN).
//
// `make web-embed` (or the Dockerfile) writes Vite's output to ./dist. In a
// fresh clone dist/ holds only a .gitkeep — the `all:` prefix lets go:embed
// accept that, so `go build ./...` stays green with or without a web build;
// Dist() reports whether a real build is present.
package web

import (
	"embed"
	"io/fs"
	"mime"
)

//go:embed all:dist
var dist embed.FS

func init() {
	// Go's built-in mime table lacks the web-font types and the final image
	// (distroless) has no /etc/mime.types to fall back on. Without these the
	// self-hosted fonts would go out as application/octet-stream and be
	// refused by `X-Content-Type-Options: nosniff`.
	_ = mime.AddExtensionType(".woff2", "font/woff2")
	_ = mime.AddExtensionType(".woff", "font/woff")
	// The PWA manifest likewise: browsers expect application/manifest+json.
	_ = mime.AddExtensionType(".webmanifest", "application/manifest+json")
}

// Dist returns the built SPA rooted at its index.html, or ok=false when no
// build has been embedded (API-only binary, e.g. `go run` during development
// with the Vite dev server serving the app on its own port).
func Dist() (fsys fs.FS, ok bool) {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		return nil, false
	}
	if _, err := fs.Stat(sub, "index.html"); err != nil {
		return nil, false
	}
	return sub, true
}
