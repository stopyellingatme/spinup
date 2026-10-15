package app

import (
	"bytes"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"time"
)

// WithSPA serves a static application from files, whose root contains index.html.
// GET and HEAD requests for client routes receive index.html without directory listings.
// API, health, metrics, version, and internal routes never receive the fallback.
func WithSPA(files fs.FS) Option {
	return WithRoutes(func(mux *http.ServeMux) {
		mux.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
			for _, prefix := range []string{"api", "healthz", "readyz", "metrics", "version", "_deploy", "_dev"} {
				if name == prefix || strings.HasPrefix(name, prefix+"/") {
					http.NotFound(w, r)
					return
				}
			}
			if r.Method != http.MethodGet && r.Method != http.MethodHead {
				w.Header().Set("Allow", "GET, HEAD")
				w.WriteHeader(http.StatusMethodNotAllowed)
				return
			}
			if files == nil {
				http.NotFound(w, r)
				return
			}
			data, err := fs.ReadFile(files, name)
			if err != nil {
				if strings.HasPrefix(name, "_app/") {
					http.NotFound(w, r)
					return
				}
				name = "index.html"
				data, err = fs.ReadFile(files, name)
				if err != nil {
					http.NotFound(w, r)
					return
				}
			}
			if name == "index.html" {
				w.Header().Set("Cache-Control", "no-cache")
				// SvelteKit bootstraps the SPA with an inline script.
				w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline'; object-src 'none'; base-uri 'self'; frame-ancestors 'none'")
			} else if strings.HasPrefix(name, "_app/immutable/") {
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			}
			http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(data))
		}))
	})
}
