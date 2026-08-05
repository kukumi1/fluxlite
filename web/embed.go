// Package web serves the compiled single-page frontend from the binary, so
// deploying fluxlite means copying one file.
package web

import (
	"embed"
	"fmt"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

//go:embed all:dist
var dist embed.FS

// Handler serves the built frontend, falling back to index.html so client
// side routes survive a page reload.
func Handler() (http.Handler, error) {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		return nil, fmt.Errorf("locate frontend assets: %w", err)
	}
	if _, err := fs.Stat(sub, "index.html"); err != nil {
		return nil, fmt.Errorf("frontend not built: %w", err)
	}

	files := http.FileServer(http.FS(sub))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if name == "" {
			name = "index.html"
		}

		if _, err := fs.Stat(sub, name); err != nil {
			// Hashed asset names are immutable, so a miss there is a genuine
			// 404 rather than a client-side route.
			if strings.HasPrefix(name, "assets/") {
				http.NotFound(w, r)
				return
			}
			r = r.Clone(r.Context())
			r.URL.Path = "/"
		}
		files.ServeHTTP(w, r)
	}), nil
}
