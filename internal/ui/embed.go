package ui

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed static
var static embed.FS

// Handler serves the embedded UI. Paths that match an embedded file are served
// directly; anything else falls back to index.html so client-side routes
// (/queue, /{owner}/{repo}/actions/runs/{id}, …) survive a page refresh.
func Handler() http.Handler {
	sub, err := fs.Sub(static, "static")
	if err != nil {
		panic(err) // embed is compile-time; cannot fail at runtime
	}
	fileServer := http.FileServer(http.FS(sub))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/")
		if name == "" {
			name = "index.html"
		}
		if f, err := sub.Open(name); err == nil {
			f.Close()
			fileServer.ServeHTTP(w, r)
			return
		}
		// SPA fallback: serve the app shell for client-side routes.
		index, err := fs.ReadFile(sub, "index.html")
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		w.WriteHeader(http.StatusOK)
		w.Write(index) //nolint:errcheck
	})
}
