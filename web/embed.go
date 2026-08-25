package web

import (
	"embed"
	"io/fs"
	"mime"
	"net/http"
)

//go:embed static/*
var staticFiles embed.FS

func init() {
	// Go's table does not carry this one, so the manifest would go out as
	// text/plain and a browser would decline to treat the page as installable.
	// Installing matters more here than it looks: on iOS it is what exempts the
	// site from Safari evicting storage for anything not visited in seven days,
	// which is shorter than a test campaign.
	//
	// The error is ignored deliberately — it can only be a malformed type
	// literal, which is a compile-time mistake, and a store that refuses to
	// start over a MIME table entry would be worse than one that serves a
	// slightly wrong header.
	_ = mime.AddExtensionType(".webmanifest", "application/manifest+json")
}

// StaticHandler returns an http.Handler that serves the embedded static files.
func StaticHandler() http.Handler {
	sub, _ := fs.Sub(staticFiles, "static")
	return http.FileServer(http.FS(sub))
}
